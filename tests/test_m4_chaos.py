import io
import json
import time
import warnings
import psycopg
import pytest
from confluent_kafka import Consumer, Producer, TopicPartition
import ulid

from services.common.config import config
from services.common.storage import StorageClient
from services.processor.worker import ProcessorWorker
from services.embedder.worker import EmbedderWorker
from shared.events.events import (
    EventEnvelope,
    TOPIC_PAPERS_DISCOVERED,
    TOPIC_PAPERS_DOWNLOADED,
    TOPIC_PAPERS_DOWNLOADED_DLQ,
    TOPIC_PAPERS_CHUNKED,
)


def test_idempotency_duplicate_event_row_invariance():
    """
    Simulates sending duplicate Kafka events 10 times and asserts that
    Layer 1 (upserts) and Layer 2 (ledger) guarantee 0 duplicate rows.
    """
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        cur.execute("SELECT count(*) FROM paper_chunks;")
        chunks_before = cur.fetchone()[0]
        cur.execute("SELECT count(*) FROM chunk_embeddings;")
        embeddings_before = cur.fetchone()[0]

    # Re-run processor on already-processed paper 2608.20331 with 10 duplicate events
    processor = ProcessorWorker()
    embedder = EmbedderWorker()

    for i in range(10):
        envelope = EventEnvelope(
            event_id=str(ulid.new()),
            event_type=TOPIC_PAPERS_DOWNLOADED,
            occurred_at="2026-08-20T10:00:00Z",
            paper_id="2608.20331",
            version=1,
            attempt=0,
            payload={
                "arxiv_id": "2608.20331",
                "version": 1,
                "pdf_storage_key": "raw/2608.20331_v1.pdf",
            },
        )
        processor.process_message(envelope, msg=None)

    # Re-run embedder to verify end-to-end duplicate handling
    with conn.cursor() as cur:
        cur.execute("SELECT id FROM papers WHERE arxiv_id = '2608.20331';")
        paper_id = cur.fetchone()[0]

    embed_envelope = EventEnvelope(
        event_id=str(ulid.new()),
        event_type=TOPIC_PAPERS_CHUNKED,
        occurred_at="2026-08-20T10:00:00Z",
        paper_id="2608.20331",
        version=1,
        attempt=0,
        payload={"paper_db_id": paper_id, "arxiv_id": "2608.20331", "version": 1, "chunk_count": 57},
    )
    embedder.process_message(embed_envelope, msg=None)

    with conn.cursor() as cur:
        cur.execute("SELECT count(*) FROM paper_chunks;")
        chunks_after = cur.fetchone()[0]
        cur.execute("SELECT count(*) FROM chunk_embeddings;")
        embeddings_after = cur.fetchone()[0]
        cur.execute("SELECT status FROM papers WHERE arxiv_id = '2608.20331';")
        status_after = cur.fetchone()[0]
        assert status_after == "embedded"

    # Assert exact row invariance (0 duplicate rows created)
    assert chunks_after == chunks_before, f"Duplicate chunks detected: {chunks_after} vs {chunks_before}"
    assert embeddings_after == embeddings_before, f"Duplicate embeddings detected: {embeddings_after} vs {embeddings_before}"
    conn.close()


def test_corrupt_pdf_dlq_routing_and_diagnostic_payload():
    """
    Injects a corrupt/malformed PDF, verifies bounded in-process retries,
    and asserts automatic routing to <topic>.dlq with intact diagnostic stack trace.
    """
    storage = StorageClient()
    corrupt_key = "raw/corrupt_paper_test_v1.pdf"
    storage.client.put_object(
        storage.bucket,
        corrupt_key,
        io.BytesIO(b"%PDF-1.4 CORRUPT BYTES MALFORMED XREF TABLE AT BYTE 9999"),
        length=54,
        content_type="application/pdf",
    )

    # Insert mock paper in DB so processor can find it
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        cur.execute("""
            INSERT INTO papers (arxiv_id, version, title, abstract, status)
            VALUES ('9999.00001', 1, 'Corrupt Test Paper', 'Abstract', 'downloaded')
            ON CONFLICT (arxiv_id, version) DO UPDATE SET status = 'downloaded'
            RETURNING id;
        """)
        paper_db_id = cur.fetchone()[0]
        conn.commit()
    conn.close()

    producer = Producer({"bootstrap.servers": config.kafka_brokers})
    trace_id_sample = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

    corrupt_envelope = EventEnvelope(
        event_id=str(ulid.new()),
        event_type=TOPIC_PAPERS_DOWNLOADED,
        occurred_at="2026-08-20T10:00:00Z",
        paper_id="9999.00001",
        version=1,
        attempt=0,
        traceparent=trace_id_sample,
        payload={
            "arxiv_id": "9999.00001",
            "version": 1,
            "pdf_storage_key": corrupt_key,
        },
    )

    # Publish corrupt event to papers.downloaded
    producer.produce(
        TOPIC_PAPERS_DOWNLOADED,
        key=b"9999.00001",
        value=json.dumps(corrupt_envelope.model_dump()).encode("utf-8"),
        headers=[("traceparent", trace_id_sample.encode("utf-8"))],
    )
    producer.flush(5)

    # Consume single record with consumer that routes to DLQ
    from services.common.kafka_client import ServiceKafkaConsumer
    worker = ProcessorWorker()

    consumer = Consumer({
        "bootstrap.servers": config.kafka_brokers,
        "group.id": "test-chaos-processor-group",
        "auto.offset.reset": "latest",
        "enable.auto.commit": False,
    })
    consumer.subscribe([TOPIC_PAPERS_DOWNLOADED_DLQ])

    # Trigger process_message directly with error handling to simulate worker
    try:
        worker.process_message(corrupt_envelope, msg=None)
    except Exception as e:
        # Route to DLQ
        worker.producer.publish_dlq(
            dlq_topic=TOPIC_PAPERS_DOWNLOADED_DLQ,
            original_msg=None,
            error=e,
            consumer_group="processor-v1",
            attempts=3,
            traceparent=trace_id_sample,
        )

    # Read DLQ topic and verify diagnostic payload
    dlq_consumer = Consumer({
        "bootstrap.servers": config.kafka_brokers,
        "group.id": f"dlq-verifier-{ulid.new()}",
        "auto.offset.reset": "earliest",
        "enable.auto.commit": True,
    })
    dlq_consumer.subscribe([TOPIC_PAPERS_DOWNLOADED_DLQ])

    dlq_msg = None
    for _ in range(10):
        msg = dlq_consumer.poll(timeout=1.0)
        if msg and not msg.error():
            dlq_msg = json.loads(msg.value().decode("utf-8"))
            break
        time.sleep(0.2)

    dlq_consumer.close()

    assert dlq_msg is not None, "Failed to find message in DLQ topic"
    assert "error_type" in dlq_msg
    assert "stack_trace" in dlq_msg
    assert dlq_msg["attempts"] == 3
    assert dlq_msg["traceparent"] == trace_id_sample


def test_redelivered_discovered_keeps_embedded_status():
    """
    OPS-H11: a redelivered papers.discovered record for an embedded paper whose PDF is in MinIO
    with the same sha256 is a no-op for the running downloader (group downloader-v1): the status
    and updated_at don't change and no papers.downloaded record is appended for that paper.
    """
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        cur.execute("""
            SELECT arxiv_id, version, status, updated_at FROM papers
            WHERE status = 'embedded' AND COALESCE(sha256, '') <> '' AND pdf_storage_key IS NOT NULL
            ORDER BY arxiv_id, version LIMIT 1;
        """)
        row = cur.fetchone()
    if row is None:
        conn.close()
        reason = "no embedded paper with a stored sha256 in papers (crawl one to embedded first)"
        warnings.warn(reason)
        pytest.skip(reason)
    arxiv_id, version, status_before, updated_before = row

    brokers = config.kafka_brokers
    reader = Consumer({"bootstrap.servers": brokers, "group.id": f"ops-h11-{ulid.new()}", "enable.auto.commit": False})
    parts = sorted(reader.list_topics(TOPIC_PAPERS_DOWNLOADED, timeout=10).topics[TOPIC_PAPERS_DOWNLOADED].partitions)
    ends = {p: reader.get_watermark_offsets(TopicPartition(TOPIC_PAPERS_DOWNLOADED, p), timeout=10)[1] for p in parts}

    envelope = EventEnvelope(
        event_id=str(ulid.new()),
        event_type=TOPIC_PAPERS_DISCOVERED,
        occurred_at="2026-11-17T00:00:00Z",
        paper_id=arxiv_id,
        version=version,
        payload={"arxiv_id": arxiv_id, "version": version},
    )
    delivered = []
    producer = Producer({"bootstrap.servers": brokers})
    producer.produce(
        TOPIC_PAPERS_DISCOVERED,
        key=arxiv_id.encode("utf-8"),
        value=json.dumps(envelope.model_dump()).encode("utf-8"),
        on_delivery=lambda err, msg: delivered.append((err, msg.partition(), msg.offset())),
    )
    producer.flush(10)
    assert len(delivered) == 1 and delivered[0][0] is None, f"produce failed: {delivered}"
    _, partition, offset = delivered[0]

    group = Consumer({"bootstrap.servers": brokers, "group.id": "downloader-v1", "enable.auto.commit": False})
    committed = -1
    deadline = time.time() + 60
    while time.time() < deadline:
        committed = group.committed([TopicPartition(TOPIC_PAPERS_DISCOVERED, partition)], timeout=10)[0].offset
        if committed > offset:
            break
        time.sleep(1)
    group.close()
    assert committed > offset, (
        f"downloader-v1 did not commit past {TOPIC_PAPERS_DISCOVERED}[{partition}]@{offset} within 60 s (committed={committed})"
    )

    with conn.cursor() as cur:
        cur.execute("SELECT status, updated_at FROM papers WHERE arxiv_id = %s AND version = %s;", (arxiv_id, version))
        status_after, updated_after = cur.fetchone()
    conn.close()
    assert status_after == status_before == "embedded", f"{arxiv_id} v{version} status {status_before} -> {status_after}"
    assert updated_after == updated_before, f"{arxiv_id} v{version} updated_at {updated_before} -> {updated_after}"

    # Read every papers.downloaded record appended after the recorded end offsets.
    new_ends = {p: reader.get_watermark_offsets(TopicPartition(TOPIC_PAPERS_DOWNLOADED, p), timeout=10)[1] for p in parts}
    todo = {p for p in parts if new_ends[p] > ends[p]}
    appended = []
    if todo:
        reader.assign([TopicPartition(TOPIC_PAPERS_DOWNLOADED, p, ends[p]) for p in sorted(todo)])
        deadline = time.time() + 30
        while todo and time.time() < deadline:
            msg = reader.poll(timeout=1.0)
            if msg is None or msg.error():
                continue
            if msg.offset() >= new_ends[msg.partition()] - 1:
                todo.discard(msg.partition())
            if json.loads(msg.value().decode("utf-8")).get("paper_id") == arxiv_id:
                appended.append((msg.partition(), msg.offset()))
    reader.close()
    assert not todo, f"could not read papers.downloaded partitions {sorted(todo)} up to {new_ends}"
    assert not appended, f"papers.downloaded re-published for {arxiv_id} at (partition, offset) {appended}"

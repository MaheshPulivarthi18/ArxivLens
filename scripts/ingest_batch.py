#!/usr/bin/env python3
"""
ArxivLens — Ingest All Downloaded Papers Batch Runner
Iterates over all papers with status 'downloaded' or 'chunked' in PostgreSQL,
runs PyMuPDF typography parsing + sliding token chunking, and embeds with BGE on GPU.
"""

import logging
import psycopg
import ulid
from services.common.config import config
from services.processor.worker import ProcessorWorker
from services.embedder.worker import EmbedderWorker
from shared.events.events import EventEnvelope, TOPIC_PAPERS_DOWNLOADED, TOPIC_PAPERS_CHUNKED

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("batch-ingest")


def run_batch_ingest():
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        cur.execute("""
            SELECT id, arxiv_id, version, title, pdf_storage_key, status
            FROM papers
            WHERE status IN ('downloaded', 'chunked') AND pdf_storage_key IS NOT NULL
            ORDER BY id;
        """)
        papers_to_process = cur.fetchall()
    conn.close()

    logger.info(f"Found {len(papers_to_process)} downloaded papers ready for chunking & embedding.")

    processor = ProcessorWorker()
    embedder = EmbedderWorker()

    for idx, (db_id, arxiv_id, version, title, storage_key, status) in enumerate(papers_to_process, start=1):
        logger.info(f"[{idx}/{len(papers_to_process)}] Processing paper {arxiv_id} ('{title[:40]}...')...")

        # Step 1: Chunk paper if needed
        download_envelope = EventEnvelope(
            event_id=str(ulid.new()),
            event_type=TOPIC_PAPERS_DOWNLOADED,
            occurred_at="2026-08-23T10:00:00Z",
            paper_id=arxiv_id,
            version=version,
            attempt=0,
            payload={
                "arxiv_id": arxiv_id,
                "version": version,
                "pdf_storage_key": storage_key,
            },
        )
        try:
            processor.process_message(download_envelope, msg=None)
        except Exception as e:
            logger.error(f"Failed to process/chunk {arxiv_id}: {e}")
            continue

        # Step 2: Embed chunks
        conn = psycopg.connect(config.postgres_url)
        with conn.cursor() as cur:
            cur.execute("SELECT count(*) FROM paper_chunks WHERE paper_id = %s;", (db_id,))
            chunk_count = cur.fetchone()[0]
        conn.close()

        chunk_envelope = EventEnvelope(
            event_id=str(ulid.new()),
            event_type=TOPIC_PAPERS_CHUNKED,
            occurred_at="2026-08-23T10:00:00Z",
            paper_id=arxiv_id,
            version=version,
            attempt=0,
            payload={
                "paper_db_id": db_id,
                "arxiv_id": arxiv_id,
                "version": version,
                "chunk_count": chunk_count,
            },
        )
        try:
            embedder.process_message(chunk_envelope, msg=None)
        except Exception as e:
            logger.error(f"Failed to embed {arxiv_id}: {e}")
            continue

    logger.info("Batch ingestion completed successfully!")


if __name__ == "__main__":
    run_batch_ingest()

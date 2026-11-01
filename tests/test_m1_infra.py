import psycopg
import pytest
from confluent_kafka.admin import AdminClient
import requests


def test_postgres_pgvector_and_schema():
    conn = psycopg.connect("postgresql://postgres:postgres@localhost:5432/arxivlens")
    with conn.cursor() as cur:
        # Check pgvector extension
        cur.execute("SELECT extname FROM pg_extension WHERE extname = 'vector';")
        ext = cur.fetchone()
        assert ext is not None, "pgvector extension is not installed"

        # Check tables
        cur.execute("""
            SELECT table_name FROM information_schema.tables
            WHERE table_schema = 'public';
        """)
        tables = {row[0] for row in cur.fetchall()}
        required_tables = {"papers", "paper_sections", "paper_chunks", "chunk_embeddings", "processed_events"}
        assert required_tables.issubset(tables), f"Missing tables: {required_tables - tables}"

        # Check papers row count from our crawl
        cur.execute("SELECT count(*) FROM papers;")
        total_papers = cur.fetchone()[0]
        assert total_papers >= 2
    conn.close()


def test_kafka_topics_and_partitions():
    admin = AdminClient({"bootstrap.servers": "localhost:29092"})
    cluster_meta = admin.list_topics(timeout=10)

    expected_topics = [
        "papers.discovered",
        "papers.discovered.dlq",
        "papers.downloaded",
        "papers.downloaded.dlq",
        "papers.chunked",
        "papers.chunked.dlq",
    ]

    for topic_name in expected_topics:
        assert topic_name in cluster_meta.topics, f"Topic {topic_name} not found in Kafka"
        partitions = cluster_meta.topics[topic_name].partitions
        assert len(partitions) == 12, f"Topic {topic_name} has {len(partitions)} partitions (expected 12)"


def test_minio_bucket_and_downloaded_files():
    # MinIO health endpoint
    resp = requests.get("http://localhost:9000/minio/health/live", timeout=5)
    assert resp.status_code == 200

    # Query Postgres for downloaded papers and verify storage key
    conn = psycopg.connect("postgresql://postgres:postgres@localhost:5432/arxivlens")
    with conn.cursor() as cur:
        cur.execute("SELECT arxiv_id, pdf_storage_key, sha256 FROM papers WHERE pdf_storage_key IS NOT NULL;")
        rows = cur.fetchall()
        assert len(rows) >= 2
        for arxiv_id, storage_key, sha256 in rows:
            assert storage_key.startswith("raw/")
            assert storage_key.endswith(".pdf")
            assert len(sha256) == 64
    conn.close()

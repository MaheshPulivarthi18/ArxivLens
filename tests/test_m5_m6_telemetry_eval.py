import time
import psycopg
import pytest
from opentelemetry import trace
from services.common.config import config
from services.common.telemetry import (
    start_trace_span,
    extract_traceparent,
    inject_traceparent,
)
from scripts.evaluate_retrieval import compute_recall_at_k, compute_mrr, compute_ndcg_at_k


def test_w3c_traceparent_injection_and_extraction():
    """Verify W3C TraceContext propagation across span boundaries."""
    trace_id_sample = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
    span_ctx = extract_traceparent(trace_id_sample)
    assert span_ctx.is_valid
    assert f"{span_ctx.trace_id:032x}" == "4bf92f3577b34da6a3ce929d0e0e4736"

    with start_trace_span("test_worker_task", parent_traceparent=trace_id_sample) as span:
        injected = inject_traceparent(span)
        assert injected is not None
        assert injected.startswith("00-4bf92f3577b34da6a3ce929d0e0e4736-")


def test_retrieval_ranking_metric_calculations():
    """Verify mathematical correctness of Recall@K, MRR, and NDCG@K."""
    relevant = [10, 20, 30]
    ranked_perfect = [10, 20, 30, 40, 50]
    ranked_partial = [40, 10, 50, 20, 60]
    ranked_none = [100, 200, 300]

    # Recall
    assert compute_recall_at_k(ranked_perfect, relevant, k=3) == 1.0
    assert compute_recall_at_k(ranked_none, relevant, k=3) == 0.0

    # MRR
    assert compute_mrr(ranked_perfect, relevant) == 1.0
    assert compute_mrr(ranked_partial, relevant) == 0.5
    assert compute_mrr(ranked_none, relevant) == 0.0

    # NDCG
    assert compute_ndcg_at_k(ranked_perfect, relevant, k=3) == 1.0
    assert compute_ndcg_at_k(ranked_none, relevant, k=3) == 0.0


def test_pgvector_hnsw_index_presence_and_querying():
    """Verify pgvector HNSW index is active on chunk_embeddings."""
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        # Verify index exists
        cur.execute("""
            SELECT indexname, indexdef 
            FROM pg_indexes 
            WHERE tablename = 'chunk_embeddings' AND indexname = 'chunk_embeddings_hnsw_idx';
        """)
        idx = cur.fetchone()
        assert idx is not None
        assert "hnsw" in idx[1].lower()

        # Query with ef_search
        cur.execute("SET hnsw.ef_search = 40;")
        dummy_vec = "[" + ",".join(["0.01"] * 768) + "]"
        cur.execute("""
            SELECT chunk_id, embedding <=> %s::halfvec AS dist 
            FROM chunk_embeddings 
            ORDER BY dist ASC 
            LIMIT 5;
        """, (dummy_vec,))
        results = cur.fetchall()
        assert len(results) > 0
    conn.close()

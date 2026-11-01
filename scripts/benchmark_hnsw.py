#!/usr/bin/env python3
"""
ArxivLens — HNSW Parameter Sweep & Index Benchmark (§11)
Sweeps ef_search values (20, 40, 80, 160) and measures:
  - Latency distributions (p50, p95, p99 in ms)
  - Throughput (QPS)
  - Index build statistics
"""

import time
from typing import List
import numpy as np
import psycopg

from scripts.m0_skeleton import BGEEmbedder
from services.common.config import config


def benchmark_hnsw_sweep():
    print("=" * 75)
    print("ArxivLens — pgvector HNSW ef_search Parameter Sweep Benchmark (§11)")
    print("=" * 75)

    conn = psycopg.connect(config.postgres_url)
    embedder = BGEEmbedder()

    # Create HNSW index if not already present
    with conn.cursor() as cur:
        print("Ensuring HNSW index exists on chunk_embeddings(embedding)...")
        t0 = time.perf_counter()
        cur.execute("""
            CREATE INDEX IF NOT EXISTS chunk_embeddings_hnsw_idx 
            ON chunk_embeddings 
            USING hnsw (embedding halfvec_cosine_ops) 
            WITH (m = 16, ef_construction = 64);
        """)
        conn.commit()
        t_index = (time.perf_counter() - t0) * 1000
        print(f"HNSW Index verified in {t_index:.2f} ms")

        # Get total chunk count
        cur.execute("SELECT count(*) FROM chunk_embeddings;")
        total_vectors = cur.fetchone()[0]
        print(f"Total indexed vectors in database: {total_vectors}")

    test_queries = [
        "patient-oriented medical report interpretation",
        "checklist-aligned reward learning factuality",
        "multimodal dialogue history generation",
        "active data collection travel behavior",
        "weather-sensitive demand prediction modeling",
    ]

    # Precompute query vectors
    query_vectors = [
        "[" + ",".join(f"{x:.6f}" for x in embedder.embed_texts([q], is_query=True)[0]) + "]"
        for q in test_queries
    ]

    ef_search_values = [20, 40, 80, 160]
    results = {}

    with conn.cursor() as cur:
        for ef in ef_search_values:
            cur.execute(f"SET hnsw.ef_search = {ef};")
            latencies = []

            # Run 50 timed iterations per parameter
            iterations = 50
            t_start = time.perf_counter()

            for i in range(iterations):
                vec_str = query_vectors[i % len(query_vectors)]
                t0 = time.perf_counter()
                cur.execute(
                    """
                    SELECT chunk_id, embedding <=> %s::halfvec AS distance
                    FROM chunk_embeddings
                    ORDER BY distance ASC
                    LIMIT 10;
                    """,
                    (vec_str,),
                )
                _ = cur.fetchall()
                t1 = time.perf_counter()
                latencies.append((t1 - t0) * 1000)

            total_time = time.perf_counter() - t_start
            qps = iterations / total_time

            p50 = np.percentile(latencies, 50)
            p95 = np.percentile(latencies, 95)
            p99 = np.percentile(latencies, 99)

            results[ef] = {
                "p50_ms": p50,
                "p95_ms": p95,
                "p99_ms": p99,
                "qps": qps,
            }

    conn.close()

    print("\nHNSW Parameter Sweep Results Table:")
    print(f"{'ef_search':<12} | {'p50 Latency (ms)':<18} | {'p95 (ms)':<10} | {'p99 (ms)':<10} | {'Throughput (QPS)':<16}")
    print("-" * 75)
    for ef, metrics in results.items():
        print(f"{ef:<12} | {metrics['p50_ms']:<18.2f} | {metrics['p95_ms']:<10.2f} | {metrics['p99_ms']:<10.2f} | {metrics['qps']:<16.1f}")
    print("=" * 75)


if __name__ == "__main__":
    benchmark_hnsw_sweep()

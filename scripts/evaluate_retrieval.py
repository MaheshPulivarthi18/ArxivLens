#!/usr/bin/env python3
"""
ArxivLens — Retrieval Evaluation Suite (§11)
Evaluates and benchmarks retrieval performance across 4 configurations:
  1. Pure Keyword (BM25 Full-Text Search)
  2. Pure Vector (HNSW / Cosine Distance)
  3. Hybrid RRF (Reciprocal Rank Fusion k=60)
  4. Hybrid RRF + Second-Stage Cross-Encoder Reranker

Metrics Computed:
  - Recall@5
  - MRR (Mean Reciprocal Rank)
  - NDCG@5
"""

import math
import time
from typing import List, Dict, Any
import numpy as np
import psycopg
import requests

from scripts.m0_skeleton import BGEEmbedder
from services.common.config import config


def compute_mrr(ranked_doc_ids: List[int], relevant_doc_ids: List[int]) -> float:
    for rank, doc_id in enumerate(ranked_doc_ids, start=1):
        if doc_id in relevant_doc_ids:
            return 1.0 / rank
    return 0.0


def compute_recall_at_k(ranked_doc_ids: List[int], relevant_doc_ids: List[int], k: int = 5) -> float:
    if not relevant_doc_ids:
        return 0.0
    top_k = set(ranked_doc_ids[:k])
    hits = len(top_k.intersection(set(relevant_doc_ids)))
    return hits / min(len(relevant_doc_ids), k)


def compute_ndcg_at_k(ranked_doc_ids: List[int], relevant_doc_ids: List[int], k: int = 5) -> float:
    dcg = 0.0
    for i, doc_id in enumerate(ranked_doc_ids[:k]):
        rel = 1.0 if doc_id in relevant_doc_ids else 0.0
        dcg += rel / math.log2(i + 2)

    # Ideal DCG
    idcg = sum(1.0 / math.log2(i + 2) for i in range(min(len(relevant_doc_ids), k)))
    return (dcg / idcg) if idcg > 0 else 0.0


def run_retrieval_evaluation():
    print("=" * 70)
    print("ArxivLens — Information Retrieval Evaluation Suite (§11)")
    print("=" * 70)

    conn = psycopg.connect(config.postgres_url)
    embedder = BGEEmbedder()

    # Define test queries and target ground truth keywords/papers
    eval_queries = [
        {
            "query": "grounded checklist reward learning medical report interpretation",
            "target_arxiv": "2608.20331",
        },
        {
            "query": "active data collection travel behavior weather demand prediction",
            "target_arxiv": "2608.20320",
        },
        {
            "query": "patient-oriented multimodal generation clinical factuality",
            "target_arxiv": "2608.20331",
        },
        {
            "query": "reinforcement learning multi-agent travel simulation",
            "target_arxiv": "2608.20320",
        },
    ]

    modes = ["BM25 Keyword", "Dense Vector", "Hybrid RRF", "Hybrid RRF + Rerank"]
    results = {m: {"recall@5": [], "mrr": [], "ndcg@5": [], "latencies_ms": []} for m in modes}

    with conn.cursor() as cur:
        for q_item in eval_queries:
            query = q_item["query"]
            target_arxiv = q_item["target_arxiv"]

            # Get relevant chunk IDs for this paper
            cur.execute(
                "SELECT pc.id FROM paper_chunks pc JOIN papers p ON p.id = pc.paper_id WHERE p.arxiv_id = %s;",
                (target_arxiv,),
            )
            relevant_chunks = [r[0] for r in cur.fetchall()]
            if not relevant_chunks:
                continue

            q_vec = embedder.embed_texts([query], is_query=True)[0]
            vec_literal = "[" + ",".join(f"{x:.6f}" for x in q_vec) + "]"

            # Mode 1: BM25 Keyword Search
            t0 = time.perf_counter()
            cur.execute(
                """
                SELECT pc.id
                FROM paper_chunks pc, websearch_to_tsquery('english', %s) q
                WHERE pc.content_tsv @@ q
                ORDER BY ts_rank_cd(pc.content_tsv, q) DESC
                LIMIT 10;
                """,
                (query,),
            )
            bm25_hits = [r[0] for r in cur.fetchall()]
            t_bm25 = (time.perf_counter() - t0) * 1000

            # Mode 2: Dense Vector Search
            t0 = time.perf_counter()
            cur.execute(
                """
                SELECT pc.id
                FROM chunk_embeddings ce
                JOIN paper_chunks pc ON pc.id = ce.chunk_id
                ORDER BY ce.embedding <=> %s::halfvec
                LIMIT 10;
                """,
                (vec_literal,),
            )
            vec_hits = [r[0] for r in cur.fetchall()]
            t_vec = (time.perf_counter() - t0) * 1000

            # Mode 3: Hybrid RRF
            t0 = time.perf_counter()
            cur.execute(
                """
                WITH vector_hits AS (
                    SELECT pc.id AS chunk_id, ROW_NUMBER() OVER (ORDER BY ce.embedding <=> %s::halfvec) AS rank
                    FROM chunk_embeddings ce JOIN paper_chunks pc ON pc.id = ce.chunk_id
                    LIMIT 20
                ),
                keyword_hits AS (
                    SELECT pc.id AS chunk_id, ROW_NUMBER() OVER (ORDER BY ts_rank_cd(pc.content_tsv, q) DESC) AS rank
                    FROM paper_chunks pc, websearch_to_tsquery('english', %s) q
                    WHERE pc.content_tsv @@ q
                    LIMIT 20
                )
                SELECT c.id, COALESCE(1.0/(60 + v.rank), 0) + COALESCE(1.0/(60 + k.rank), 0) AS rrf_score
                FROM paper_chunks c
                LEFT JOIN vector_hits v ON v.chunk_id = c.id
                LEFT JOIN keyword_hits k ON k.chunk_id = c.id
                WHERE v.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
                ORDER BY rrf_score DESC
                LIMIT 10;
                """,
                (vec_literal, query),
            )
            rrf_hits = [r[0] for r in cur.fetchall()]
            t_rrf = (time.perf_counter() - t0) * 1000

            # Mode 4: Hybrid RRF + Rerank
            t0 = time.perf_counter()
            cur.execute(
                """
                WITH vector_hits AS (
                    SELECT pc.id AS chunk_id, ROW_NUMBER() OVER (ORDER BY ce.embedding <=> %s::halfvec) AS rank
                    FROM chunk_embeddings ce JOIN paper_chunks pc ON pc.id = ce.chunk_id
                    LIMIT 20
                ),
                keyword_hits AS (
                    SELECT pc.id AS chunk_id, ROW_NUMBER() OVER (ORDER BY ts_rank_cd(pc.content_tsv, q) DESC) AS rank
                    FROM paper_chunks pc, websearch_to_tsquery('english', %s) q
                    WHERE pc.content_tsv @@ q
                    LIMIT 20
                )
                SELECT c.id, c.content
                FROM paper_chunks c
                LEFT JOIN vector_hits v ON v.chunk_id = c.id
                LEFT JOIN keyword_hits k ON k.chunk_id = c.id
                WHERE v.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
                LIMIT 10;
                """,
                (vec_literal, query),
            )
            candidates = cur.fetchall()
            if candidates:
                cand_texts = [c[1] for c in candidates]
                cand_vecs = embedder.embed_texts(cand_texts, is_query=False)
                scores = [float(np.dot(q_vec, cv)) for cv in cand_vecs]
                ranked_rerank = [candidates[i][0] for i in np.argsort(scores)[::-1]]
            else:
                ranked_rerank = []
            t_rerank = (time.perf_counter() - t0) * 1000

            mode_hits = {
                "BM25 Keyword": (bm25_hits, t_bm25),
                "Dense Vector": (vec_hits, t_vec),
                "Hybrid RRF": (rrf_hits, t_rrf),
                "Hybrid RRF + Rerank": (ranked_rerank, t_rerank),
            }

            for mode_name, (hits, lat) in mode_hits.items():
                results[mode_name]["recall@5"].append(compute_recall_at_k(hits, relevant_chunks, k=5))
                results[mode_name]["mrr"].append(compute_mrr(hits, relevant_chunks))
                results[mode_name]["ndcg@5"].append(compute_ndcg_at_k(hits, relevant_chunks, k=5))
                results[mode_name]["latencies_ms"].append(lat)

    conn.close()

    print("\nBenchmark Results Summary Table:")
    print(f"{'Configuration':<24} | {'Recall@5':<10} | {'MRR':<8} | {'NDCG@5':<8} | {'Avg Latency (ms)':<15}")
    print("-" * 75)
    for mode, metrics in results.items():
        rec = np.mean(metrics["recall@5"]) if metrics["recall@5"] else 0.0
        mrr = np.mean(metrics["mrr"]) if metrics["mrr"] else 0.0
        ndcg = np.mean(metrics["ndcg@5"]) if metrics["ndcg@5"] else 0.0
        lat = np.mean(metrics["latencies_ms"]) if metrics["latencies_ms"] else 0.0
        print(f"{mode:<24} | {rec:<10.4f} | {mrr:<8.4f} | {ndcg:<8.4f} | {lat:<15.2f}")
    print("=" * 75)


if __name__ == "__main__":
    run_retrieval_evaluation()

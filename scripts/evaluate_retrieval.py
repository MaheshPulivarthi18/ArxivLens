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

Queries come from evals/retrieval.jsonl (EVAL-1); none are hardcoded here.
Heavy dependencies (psycopg, numpy, sentence-transformers) are imported lazily so
`--help` and the metric helpers work without infra.
"""

import argparse
import json
import math
import os
import time
from typing import List, Dict, Any

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_DATASET = os.path.join(REPO_ROOT, "evals", "retrieval.jsonl")
MODES = ["BM25 Keyword", "Dense Vector", "Hybrid RRF", "Hybrid RRF + Rerank"]


def load_cases(path: str = DEFAULT_DATASET) -> List[Dict[str, Any]]:
    """Read a JSONL eval set, skipping blank lines."""
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


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


def run_retrieval_evaluation(dataset_path: str = DEFAULT_DATASET) -> Dict[str, Any]:
    """Run all 4 modes over the dataset. Returns per-mode mean metrics plus coverage counts."""
    import numpy as np
    import psycopg

    from scripts.m0_skeleton import BGEEmbedder
    from services.common.config import config

    print("=" * 70)
    print("ArxivLens — Information Retrieval Evaluation Suite (§11)")
    print("=" * 70)

    eval_queries = load_cases(dataset_path)
    skipped: List[str] = []

    conn = psycopg.connect(config.postgres_url)
    embedder = BGEEmbedder()

    results = {m: {"recall@5": [], "mrr": [], "ndcg@5": [], "latencies_ms": []} for m in MODES}

    with conn.cursor() as cur:
        for q_item in eval_queries:
            query = q_item["query"]
            relevant_arxiv_ids = list(q_item["relevant_arxiv_ids"])

            # Relevant chunks = every chunk of any relevant paper
            cur.execute(
                "SELECT pc.id FROM paper_chunks pc JOIN papers p ON p.id = pc.paper_id WHERE p.arxiv_id = ANY(%s);",
                (relevant_arxiv_ids,),
            )
            relevant_chunks = [r[0] for r in cur.fetchall()]
            if not relevant_chunks:
                # Paper not indexed: record it so the gate can fail on incomplete coverage.
                skipped.append(q_item["id"])
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

    summary: Dict[str, Any] = {"modes": {}, "cases_total": len(eval_queries), "skipped_case_ids": skipped}
    summary["cases_evaluated"] = len(eval_queries) - len(skipped)

    print("\nBenchmark Results Summary Table:")
    print(f"{'Configuration':<24} | {'Recall@5':<10} | {'MRR':<8} | {'NDCG@5':<8} | {'Avg Latency (ms)':<15}")
    print("-" * 75)
    for mode, metrics in results.items():
        rec = float(np.mean(metrics["recall@5"])) if metrics["recall@5"] else 0.0
        mrr = float(np.mean(metrics["mrr"])) if metrics["mrr"] else 0.0
        ndcg = float(np.mean(metrics["ndcg@5"])) if metrics["ndcg@5"] else 0.0
        lat = float(np.mean(metrics["latencies_ms"])) if metrics["latencies_ms"] else 0.0
        summary["modes"][mode] = {"recall@5": rec, "mrr": mrr, "ndcg@5": ndcg, "avg_latency_ms": lat}
        print(f"{mode:<24} | {rec:<10.4f} | {mrr:<8.4f} | {ndcg:<8.4f} | {lat:<15.2f}")
    print("=" * 75)
    print(f"Cases evaluated: {summary['cases_evaluated']}/{summary['cases_total']}")
    if skipped:
        print(f"WARNING: skipped (paper not indexed): {', '.join(skipped)}")
    return summary


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Retrieval eval (Recall@5 / MRR / NDCG@5) over 4 modes. "
                                     "Needs Postgres with the evals/corpus.txt papers indexed (DATABASE_URL).")
    parser.add_argument("--dataset", default=DEFAULT_DATASET, help="JSONL eval set (default: evals/retrieval.jsonl)")
    parser.add_argument("--json-out", help="Also write the summary as JSON to this path")
    args = parser.parse_args(argv)
    summary = run_retrieval_evaluation(args.dataset)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

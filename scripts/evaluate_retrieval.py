#!/usr/bin/env python3
"""
ArxivLens — Retrieval Evaluation Suite (§11)
Evaluates and benchmarks retrieval performance across 4 configurations:
  1. Pure Keyword (BM25 Full-Text Search)
  2. Pure Vector (HNSW / Cosine Distance)
  3. Hybrid RRF (Reciprocal Rank Fusion k=60)
  4. Hybrid RRF + Rerank: the Hybrid RRF top-10 reordered by the PRODUCTION reranker, i.e. a POST to
     the embedder API's /internal/rerank (BAAI/bge-reranker-base cross-encoder), the same endpoint
     /v1/ask reaches through internal/query/client.go EmbedClient.Rerank. The eval checks /healthz
     first and refuses to run if the API would fall back to bi-encoder scoring.

Metrics Computed:
  - Recall@5
  - MRR (Mean Reciprocal Rank)
  - NDCG@5

Queries come from evals/retrieval.jsonl (EVAL-1); none are hardcoded here.
Coverage is per arXiv ID: a case is skipped (and the gate fails) unless EVERY relevant_arxiv_id has
indexed chunks; the missing IDs are reported.
Heavy dependencies (psycopg, numpy, sentence-transformers) are imported lazily so
`--help` and the metric helpers work without infra.
"""

import argparse
import json
import math
import os
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, Iterable, List, Sequence, Tuple

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_DATASET = os.path.join(REPO_ROOT, "evals", "retrieval.jsonl")
RERANK_MODE = "Hybrid RRF + Rerank"  # the mode scripts/eval_gate.py gates
MODES = ["BM25 Keyword", "Dense Vector", "Hybrid RRF", RERANK_MODE]
DEFAULT_EMBEDDER_URL = os.getenv("ARXIVLENS_EMBEDDER_URL", "http://localhost:8081")
RERANK_PATH = "/internal/rerank"
# Recorded in the summary; eval_gate.py only accepts the rerank mode if this value is present.
PRODUCTION_RERANK_PATH = "embedder-api:/internal/rerank (cross-encoder)"

RerankFn = Callable[[str, List[str]], List[float]]


class RerankError(RuntimeError):
    """The production rerank endpoint could not be used. The eval must not silently fall back."""


def load_cases(path: str = DEFAULT_DATASET) -> List[Dict[str, Any]]:
    """Read a JSONL eval set, skipping blank lines."""
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def make_http_rerank(embedder_url: str = DEFAULT_EMBEDDER_URL, timeout_s: float = 30.0) -> RerankFn:
    """Client for the production rerank endpoint: POST {query, passages} -> {scores} (services/embedder/api.py)."""
    url = embedder_url.rstrip("/") + RERANK_PATH

    def rerank(query: str, passages: List[str]) -> List[float]:
        body = json.dumps(dict(query=query, passages=list(passages))).encode("utf-8")
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as r:
                data = json.loads(r.read().decode("utf-8"))
        except (urllib.error.URLError, TimeoutError, ValueError, OSError) as e:
            raise RerankError(f"POST {url} failed: {e}") from e
        scores = data.get("scores") if isinstance(data, dict) else None
        if not isinstance(scores, list) or len(scores) != len(passages):
            raise RerankError(f"POST {url}: expected {len(passages)} scores, got {scores!r}")
        return [float(x) for x in scores]

    return rerank


def require_cross_encoder(embedder_url: str = DEFAULT_EMBEDDER_URL, timeout_s: float = 10.0) -> None:
    """Fail unless /healthz reports the cross-encoder loaded (otherwise /internal/rerank uses bi-encoder dots)."""
    url = embedder_url.rstrip("/") + "/healthz"
    try:
        with urllib.request.urlopen(url, timeout=timeout_s) as r:
            data = json.loads(r.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, ValueError, OSError) as e:
        raise RerankError(f"GET {url} failed: {e}") from e
    if not (isinstance(data, dict) and data.get("reranker") is True):
        raise RerankError(f"{url} reports no cross-encoder loaded ({data!r}); refusing to evaluate the "
                          "bi-encoder fallback as the production reranker")


def rerank_candidates(query: str, candidates: Sequence[Tuple[int, str]], rerank: RerankFn) -> List[int]:
    """Order (chunk_id, content) candidates by reranker score, highest first (ties keep RRF order)."""
    if not candidates:
        return []
    scores = rerank(query, [c[1] for c in candidates])
    order = sorted(range(len(candidates)), key=lambda i: -scores[i])
    return [candidates[i][0] for i in order]


def missing_arxiv_ids(relevant_arxiv_ids: Iterable[str], indexed_rows: Iterable[Tuple[str, int]]) -> List[str]:
    """Relevant arXiv IDs with no indexed chunk, given (arxiv_id, chunk_id) rows. Order follows the case."""
    indexed = {arxiv_id for arxiv_id, _ in indexed_rows}
    return [a for a in relevant_arxiv_ids if a not in indexed]


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


COVERAGE_SQL = ("SELECT p.arxiv_id, pc.id FROM paper_chunks pc JOIN papers p ON p.id = pc.paper_id "
                "WHERE p.arxiv_id = ANY(%s);")

BM25_SQL = """
    SELECT pc.id
    FROM paper_chunks pc, websearch_to_tsquery('english', %s) q
    WHERE pc.content_tsv @@ q
    ORDER BY ts_rank_cd(pc.content_tsv, q) DESC
    LIMIT 10;
"""

DENSE_SQL = """
    SELECT pc.id
    FROM chunk_embeddings ce
    JOIN paper_chunks pc ON pc.id = ce.chunk_id
    ORDER BY ce.embedding <=> %s::halfvec
    LIMIT 10;
"""

RRF_SQL = """
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
    SELECT c.id, c.content, COALESCE(1.0/(60 + v.rank), 0) + COALESCE(1.0/(60 + k.rank), 0) AS rrf_score
    FROM paper_chunks c
    LEFT JOIN vector_hits v ON v.chunk_id = c.id
    LEFT JOIN keyword_hits k ON k.chunk_id = c.id
    WHERE v.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
    ORDER BY rrf_score DESC, c.id
    LIMIT 10;
"""


def relevant_chunks_for_case(cur, relevant_arxiv_ids: List[str]) -> Tuple[List[int], List[str]]:
    """Relevant chunks = every chunk of every relevant paper. Returns (chunk ids, missing arXiv IDs).

    Fail-closed: if any relevant paper has no indexed chunks, the chunk list is empty and the caller skips the case.
    """
    cur.execute(COVERAGE_SQL, (list(relevant_arxiv_ids),))
    rows = cur.fetchall()
    missing = missing_arxiv_ids(relevant_arxiv_ids, rows)
    return ([] if missing else [r[1] for r in rows]), missing


def run_modes_for_case(cur, query: str, vec_literal: str, rerank: RerankFn) -> Dict[str, Tuple[List[int], float]]:
    """Run all 4 modes for one query. Returns {mode: (ranked chunk ids, latency ms)}."""
    t0 = time.perf_counter()
    cur.execute(BM25_SQL, (query,))
    bm25_hits = [r[0] for r in cur.fetchall()]
    t_bm25 = (time.perf_counter() - t0) * 1000

    t0 = time.perf_counter()
    cur.execute(DENSE_SQL, (vec_literal,))
    vec_hits = [r[0] for r in cur.fetchall()]
    t_vec = (time.perf_counter() - t0) * 1000

    t0 = time.perf_counter()
    cur.execute(RRF_SQL, (vec_literal, query))
    rrf_rows = cur.fetchall()
    rrf_hits = [r[0] for r in rrf_rows]
    t_rrf = (time.perf_counter() - t0) * 1000

    # Mode 4: the RRF top-10 reordered by the production cross-encoder (/internal/rerank).
    # Latency = RRF retrieval + the rerank round-trip.
    t0 = time.perf_counter()
    reranked = rerank_candidates(query, [(r[0], r[1]) for r in rrf_rows], rerank)
    t_rerank = t_rrf + (time.perf_counter() - t0) * 1000

    return {
        "BM25 Keyword": (bm25_hits, t_bm25),
        "Dense Vector": (vec_hits, t_vec),
        "Hybrid RRF": (rrf_hits, t_rrf),
        RERANK_MODE: (reranked, t_rerank),
    }


def run_retrieval_evaluation(dataset_path: str = DEFAULT_DATASET, embedder_url: str = DEFAULT_EMBEDDER_URL,
                             timeout_s: float = 30.0) -> Dict[str, Any]:
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
    missing_by_case: Dict[str, List[str]] = {}

    require_cross_encoder(embedder_url, timeout_s)
    rerank = make_http_rerank(embedder_url, timeout_s)
    conn = psycopg.connect(config.postgres_url)
    embedder = BGEEmbedder()

    results = {m: {"recall@5": [], "mrr": [], "ndcg@5": [], "latencies_ms": []} for m in MODES}

    with conn.cursor() as cur:
        for q_item in eval_queries:
            query = q_item["query"]
            relevant_arxiv_ids = list(q_item["relevant_arxiv_ids"])

            relevant_chunks, missing = relevant_chunks_for_case(cur, relevant_arxiv_ids)
            if missing:
                # Skipped cases make coverage < 1.0, which fails the gate.
                skipped.append(q_item["id"])
                missing_by_case[q_item["id"]] = missing
                continue

            q_vec = embedder.embed_texts([query], is_query=True)[0]
            vec_literal = "[" + ",".join(f"{x:.6f}" for x in q_vec) + "]"

            mode_hits = run_modes_for_case(cur, query, vec_literal, rerank)

            for mode_name, (hits, lat) in mode_hits.items():
                results[mode_name]["recall@5"].append(compute_recall_at_k(hits, relevant_chunks, k=5))
                results[mode_name]["mrr"].append(compute_mrr(hits, relevant_chunks))
                results[mode_name]["ndcg@5"].append(compute_ndcg_at_k(hits, relevant_chunks, k=5))
                results[mode_name]["latencies_ms"].append(lat)

    conn.close()

    summary: Dict[str, Any] = {"modes": {}, "cases_total": len(eval_queries), "skipped_case_ids": skipped,
                               "missing_arxiv_ids": missing_by_case,
                               "rerank_path": PRODUCTION_RERANK_PATH, "embedder_url": embedder_url}
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
        print("WARNING: skipped cases (relevant paper(s) not indexed):")
        for cid in skipped:
            print(f"  {cid}: missing {', '.join(missing_by_case[cid])}")
    return summary


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Retrieval eval (Recall@5 / MRR / NDCG@5) over 4 modes. "
                                     "Needs Postgres with the evals/corpus.txt papers indexed (DATABASE_URL) "
                                     "and the embedder API with the cross-encoder loaded.")
    parser.add_argument("--dataset", default=DEFAULT_DATASET, help="JSONL eval set (default: evals/retrieval.jsonl)")
    parser.add_argument("--json-out", help="Also write the summary as JSON to this path")
    parser.add_argument("--embedder-url", default=DEFAULT_EMBEDDER_URL,
                        help="Embedder API base URL for the production /internal/rerank "
                        "(env ARXIVLENS_EMBEDDER_URL, default http://localhost:8081)")
    parser.add_argument("--timeout", type=float, default=30.0, help="Per embedder API request timeout (s)")
    args = parser.parse_args(argv)
    summary = run_retrieval_evaluation(args.dataset, args.embedder_url, args.timeout)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

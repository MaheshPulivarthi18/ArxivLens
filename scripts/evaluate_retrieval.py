#!/usr/bin/env python3
"""
ArxivLens — Retrieval Evaluation Suite (§11)
Evaluates and benchmarks retrieval performance across 4 configurations. Every mode is scored on the
RESULT_LIMIT (5) chunks it returns, i.e. what a /v1/ask request with the default maxCitations gets:
  1. Pure Keyword (BM25 Full-Text Search)
  2. Pure Vector (HNSW / Cosine Distance, current embedding model only)
  3. Hybrid RRF: top 5 of the RRF candidate pool (same SQL and HNSW settings as internal/query/search.go:
     50 per arm, ce.model filter, k=60, ef_search=100 + strict_order iterative scan)
  4. Hybrid RRF + Rerank (gated, ADR-0010): the RRF pool of 20 candidates, reranked in ONE call to the
     PRODUCTION reranker (POST <embedder>/internal/rerank, the BAAI/bge-reranker-base cross-encoder that
     internal/query/client.go EmbedClient.Rerank calls), stable-sorted by score (ties keep RRF order),
     truncated to the top 5. /healthz must report the cross-encoder loaded. Production /v1/ask orders
     results this way only once RET-1 merges; until then this mode measures the ADR-0010 target.

Metrics (ADR-0006, paper-level; relevance labels are arXiv IDs):
  - Recall@5 (gated): dedupe the ranked chunks to arXiv IDs in rank order,
    |top-5 papers ∩ relevant_arxiv_ids| / |relevant_arxiv_ids|
  - MRR, NDCG@5: on the same deduped paper ranking
  - chunk_precision@5 (diagnostic, NOT gated): |top-5 chunks from a relevant paper| / 5

Queries come from evals/retrieval.jsonl; none are hardcoded here.
Coverage is per arXiv ID: a paper counts as indexed only if it has chunks with embeddings for the current
model. A case is skipped (and the gate fails) unless EVERY relevant_arxiv_id is indexed.
Heavy dependencies (psycopg, numpy, sentence-transformers) are imported lazily so
`--help` and the metric helpers work without infra.
"""

import argparse
import json
import math
import os
import re
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, Hashable, Iterable, List, Optional, Sequence, Tuple

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_DATASET = os.path.join(REPO_ROOT, "evals", "retrieval.jsonl")
RERANK_MODE = "Hybrid RRF + Rerank"  # the mode scripts/eval_gate.py gates
MODES = ["BM25 Keyword", "Dense Vector", "Hybrid RRF", RERANK_MODE]
DEFAULT_EMBEDDER_URL = os.getenv("ARXIVLENS_EMBEDDER_URL", "http://localhost:8081")
RERANK_PATH = "/internal/rerank"
# Recorded in the summary; eval_gate.py only accepts the rerank mode if this value is present.
PRODUCTION_RERANK_PATH = "embedder-api:/internal/rerank (cross-encoder)"

K = 5               # metric cutoff (Recall@5, NDCG@5, chunk_precision@5)
RESULT_LIMIT = 5    # chunks each mode returns: /v1/ask maxCitations default (internal/query/rag.go)
RRF_POOL_SIZE = 20  # ADR-0010 rerank candidate pool
RRF_ARM_LIMIT = 50  # per-arm LIMIT in internal/query/search.go
RRF_K = 60          # RRF constant in internal/query/search.go
# The eval embeds with EMBEDDING_MODEL (services/common/config.py); the Go query service uses its --model flag.
# Both must name the same model or the eval measures other vectors. The --model default is read from the Go source
# (single source of truth); pass --query-model / ARXIVLENS_QUERY_MODEL when the service runs with an override.
GO_MAIN_PATH = os.path.join(REPO_ROOT, "cmd", "arxivlens", "main.go")
QUERY_MODEL_ENV = "ARXIVLENS_QUERY_MODEL"
_GO_MODEL_FLAG_RE = re.compile(r'\.StringVar\(\s*&\w+\s*,\s*"model"\s*,\s*"([^"]+)"')

RerankFn = Callable[[str, List[str]], List[float]]
Hit = Tuple[int, str]  # (chunk_id, arxiv_id)


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


def rerank_pool(query: str, pool: Sequence[Tuple[int, str, str]], rerank: RerankFn,
                limit: int = RESULT_LIMIT) -> List[Hit]:
    """ADR-0010 ordering: one rerank call over the whole (chunk_id, arxiv_id, content) RRF pool, stable sort by
    score descending (ties keep RRF order), truncate to `limit`. A rerank failure raises (production falls back
    to RRF order; the eval must fail closed instead of measuring that fallback as the reranker)."""
    if not pool:
        return []
    scores = rerank(query, [c[2] for c in pool])
    if len(scores) != len(pool):
        raise RerankError(f"rerank returned {len(scores)} scores for {len(pool)} passages")
    order = sorted(range(len(pool)), key=lambda i: -scores[i])  # sorted() is stable
    return [(pool[i][0], pool[i][1]) for i in order[:limit]]


def missing_arxiv_ids(relevant_arxiv_ids: Iterable[str], indexed_rows: Iterable[Sequence[Any]]) -> List[str]:
    """Relevant arXiv IDs absent from the indexed rows (first column = arxiv_id). Order follows the case."""
    indexed = {normalize_arxiv_id(row[0]) for row in indexed_rows}
    return [a for a in relevant_arxiv_ids if normalize_arxiv_id(a) not in indexed]


# --- metrics (ADR-0006) ---

def normalize_arxiv_id(arxiv_id: str) -> str:
    """'1706.03762v7' -> '1706.03762' (eval labels carry no version)."""
    return re.sub(r"v\d+$", "", arxiv_id.strip())


def dedupe_to_papers(ranked_arxiv_ids: Iterable[str]) -> List[str]:
    """Collapse a chunk ranking to a paper ranking: each arXiv ID once, at its best (first) rank."""
    seen, papers = set(), []
    for a in ranked_arxiv_ids:
        a = normalize_arxiv_id(a)
        if a not in seen:
            seen.add(a)
            papers.append(a)
    return papers


def compute_mrr(ranked_doc_ids: Sequence[Hashable], relevant_doc_ids: Iterable[Hashable]) -> float:
    relevant = set(relevant_doc_ids)
    for rank, doc_id in enumerate(ranked_doc_ids, start=1):
        if doc_id in relevant:
            return 1.0 / rank
    return 0.0


def compute_recall_at_k(ranked_doc_ids: Sequence[Hashable], relevant_doc_ids: Iterable[Hashable],
                        k: int = K) -> float:
    """Standard recall: |top-k ∩ relevant| / |relevant|. Pass a deduped paper ranking (ADR-0006)."""
    relevant = set(relevant_doc_ids)
    if not relevant:
        return 0.0
    return len(set(ranked_doc_ids[:k]) & relevant) / len(relevant)


def compute_ndcg_at_k(ranked_doc_ids: Sequence[Hashable], relevant_doc_ids: Iterable[Hashable],
                      k: int = K) -> float:
    relevant = set(relevant_doc_ids)
    dcg = sum(1.0 / math.log2(i + 2) for i, doc_id in enumerate(ranked_doc_ids[:k]) if doc_id in relevant)
    idcg = sum(1.0 / math.log2(i + 2) for i in range(min(len(relevant), k)))
    return (dcg / idcg) if idcg > 0 else 0.0


def compute_chunk_precision_at_k(ranked_chunk_arxiv_ids: Sequence[str], relevant_arxiv_ids: Iterable[str],
                                 k: int = K) -> float:
    """Diagnostic only (ADR-0006, not gated): share of the top-k chunks that come from any relevant paper."""
    relevant = {normalize_arxiv_id(a) for a in relevant_arxiv_ids}
    return sum(1 for a in ranked_chunk_arxiv_ids[:k] if normalize_arxiv_id(a) in relevant) / k


def score_case(hits: Sequence[Hit], relevant_arxiv_ids: Sequence[str]) -> Dict[str, float]:
    """All metrics for one mode on one case. `hits` = ranked (chunk_id, arxiv_id)."""
    chunk_papers = [a for _, a in hits]
    papers = dedupe_to_papers(chunk_papers)
    relevant = dedupe_to_papers(relevant_arxiv_ids)
    return {
        "recall@5": compute_recall_at_k(papers, relevant, K),
        "mrr": compute_mrr(papers, relevant),
        "ndcg@5": compute_ndcg_at_k(papers, relevant, K),
        "chunk_precision@5": compute_chunk_precision_at_k(chunk_papers, relevant, K),
    }


# --- SQL ---

# A paper is covered only if some chunk has an embedding for the current model (dense/RRF can't return it otherwise).
COVERAGE_SQL = """
    SELECT DISTINCT p.arxiv_id
    FROM papers p
    JOIN paper_chunks pc ON pc.paper_id = p.id
    JOIN chunk_embeddings ce ON ce.chunk_id = pc.id
    WHERE ce.model = %(model)s AND p.arxiv_id = ANY(%(ids)s);
"""

BM25_SQL = """
    SELECT pc.id, p.arxiv_id
    FROM paper_chunks pc JOIN papers p ON p.id = pc.paper_id, websearch_to_tsquery('english', %(query)s) q
    WHERE pc.content_tsv @@ q
    ORDER BY ts_rank_cd(pc.content_tsv, q) DESC
    LIMIT %(limit)s;
"""

DENSE_SQL = """
    SELECT pc.id, p.arxiv_id
    FROM chunk_embeddings ce
    JOIN paper_chunks pc ON pc.id = ce.chunk_id
    JOIN papers p ON p.id = pc.paper_id
    WHERE ce.model = %(model)s
    ORDER BY ce.embedding <=> %(vec)s::halfvec
    LIMIT %(limit)s;
"""

# Mirrors the HybridSearch SQL in internal/query/search.go (no category filter). The extra `c.id` tiebreak makes
# equal RRF scores deterministic; production leaves their order unspecified.
RRF_POOL_SQL = f"""
    WITH vector_hits AS (
        SELECT pc.id AS chunk_id,
               ROW_NUMBER() OVER (ORDER BY ce.embedding <=> %(vec)s::halfvec) AS rank
        FROM chunk_embeddings ce
        JOIN paper_chunks pc ON pc.id = ce.chunk_id
        WHERE ce.model = %(model)s
        ORDER BY ce.embedding <=> %(vec)s::halfvec
        LIMIT {RRF_ARM_LIMIT}
    ),
    keyword_hits AS (
        SELECT pc.id AS chunk_id,
               ROW_NUMBER() OVER (ORDER BY ts_rank_cd(pc.content_tsv, q) DESC) AS rank
        FROM paper_chunks pc, websearch_to_tsquery('english', %(query)s) q
        WHERE pc.content_tsv @@ q
        ORDER BY ts_rank_cd(pc.content_tsv, q) DESC
        LIMIT {RRF_ARM_LIMIT}
    )
    SELECT c.id, p.arxiv_id, c.content,
           COALESCE(1.0/({RRF_K} + v.rank), 0) + COALESCE(1.0/({RRF_K} + k.rank), 0) AS rrf_score
    FROM paper_chunks c
    JOIN papers p ON p.id = c.paper_id
    LEFT JOIN vector_hits v ON v.chunk_id = c.id
    LEFT JOIN keyword_hits k ON k.chunk_id = c.id
    WHERE v.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
    ORDER BY rrf_score DESC, c.id
    LIMIT %(pool)s;
"""
# Mirrors hnswSearchSettings in internal/query/search.go (OPS-H5): production runs these with SET LOCAL right before
# the hybrid query, so the vector arm can fill its LIMIT after the model filter. The eval runs them right before
# RRF_POOL_SQL and resets them right after, so the BM25/dense baselines keep the server defaults (not gated).
RRF_HNSW_SETTINGS = "SET LOCAL hnsw.ef_search = 100; SET LOCAL hnsw.iterative_scan = strict_order"
RRF_HNSW_RESET = "RESET hnsw.ef_search; RESET hnsw.iterative_scan"


# Per relevant paper: all chunks vs chunks embedded for the current model (a partly embedded paper still counts as
# covered above, so this is reported as a warning; making it fail the gate needs an ADR).
CHUNK_COUNTS_SQL = """
    SELECT p.arxiv_id, COUNT(ce.chunk_id) AS embedded_chunks, COUNT(pc.id) AS total_chunks
    FROM papers p
    JOIN paper_chunks pc ON pc.paper_id = p.id
    LEFT JOIN chunk_embeddings ce ON ce.chunk_id = pc.id AND ce.model = %(model)s
    WHERE p.arxiv_id = ANY(%(ids)s)
    GROUP BY p.arxiv_id;
"""


def chunk_counts_for_case(cur, relevant_arxiv_ids: List[str], model: str) -> Dict[str, Dict[str, int]]:
    """{arxiv_id: {"embedded_chunks", "total_chunks"}} for every relevant ID (0/0 when it has no chunks)."""
    cur.execute(CHUNK_COUNTS_SQL, dict(model=model, ids=list(relevant_arxiv_ids)))
    rows = {normalize_arxiv_id(r[0]): (int(r[1]), int(r[2])) for r in cur.fetchall()}
    out = {}
    for a in map(normalize_arxiv_id, relevant_arxiv_ids):
        embedded, total = rows.get(a, (0, 0))
        out[a] = {"embedded_chunks": embedded, "total_chunks": total}
    return out


def partial_embedding_warnings(counts: Dict[str, Dict[str, int]], model: str) -> List[str]:
    """One warning per paper with some chunks but fewer embedded for `model` than it has (warning only, not gated)."""
    return [f"WARNING: {a} has only {c['embedded_chunks']}/{c['total_chunks']} chunks embedded for {model}"
            for a, c in sorted(counts.items()) if 0 < c["total_chunks"] and c["embedded_chunks"] < c["total_chunks"]]


def go_default_model(path: str = GO_MAIN_PATH) -> str:
    """The query service's `--model` flag default, parsed from cmd/arxivlens/main.go. Raises unless exactly one."""
    with open(path, encoding="utf-8") as f:
        found = _GO_MODEL_FLAG_RE.findall(f.read())
    if len(found) != 1:
        raise ValueError(f"expected exactly one StringVar(..., \"model\", <default>, ...) in {path}, found {found}")
    return found[0]


def resolve_query_model(query_model: Optional[str] = None) -> str:
    """Model the query service runs with: explicit value, else $ARXIVLENS_QUERY_MODEL, else the Go --model default."""
    return query_model or os.getenv(QUERY_MODEL_ENV) or go_default_model()


def model_mismatch_warning(model: str, query_model: str) -> Optional[str]:
    """Warning when the eval's model differs from the query service's model (warning only, not gated)."""
    if model == query_model:
        return None
    return (f"WARNING: eval embedding model {model!r} (EMBEDDING_MODEL) differs from the query service model "
            f"{query_model!r} (--query-model / {QUERY_MODEL_ENV}, default: Go --model default); both must match "
            f"or the eval does not measure production")


def missing_for_case(cur, relevant_arxiv_ids: List[str], model: str) -> List[str]:
    """Relevant arXiv IDs with no chunk embedded for `model`. Non-empty -> the caller skips the case (fail-closed)."""
    cur.execute(COVERAGE_SQL, dict(model=model, ids=list(relevant_arxiv_ids)))
    return missing_arxiv_ids(relevant_arxiv_ids, cur.fetchall())


def run_modes_for_case(cur, query: str, vec_literal: str, model: str,
                       rerank: RerankFn) -> Dict[str, Tuple[List[Hit], float]]:
    """Run all 4 modes for one query. Returns {mode: (ranked (chunk_id, arxiv_id) hits, latency ms)}."""
    t0 = time.perf_counter()
    cur.execute(BM25_SQL, dict(query=query, limit=RESULT_LIMIT))
    bm25_hits = [(r[0], r[1]) for r in cur.fetchall()]
    t_bm25 = (time.perf_counter() - t0) * 1000

    t0 = time.perf_counter()
    cur.execute(DENSE_SQL, dict(vec=vec_literal, model=model, limit=RESULT_LIMIT))
    vec_hits = [(r[0], r[1]) for r in cur.fetchall()]
    t_vec = (time.perf_counter() - t0) * 1000

    t0 = time.perf_counter()
    cur.execute(RRF_HNSW_SETTINGS)
    cur.execute(RRF_POOL_SQL, dict(vec=vec_literal, model=model, query=query, pool=RRF_POOL_SIZE))
    pool = [(r[0], r[1], r[2]) for r in cur.fetchall()]
    t_rrf = (time.perf_counter() - t0) * 1000
    cur.execute(RRF_HNSW_RESET)

    # Mode 4 (ADR-0010): the whole pool reranked by the production cross-encoder, then the top RESULT_LIMIT.
    # Latency = RRF retrieval + the rerank round-trip.
    t0 = time.perf_counter()
    reranked = rerank_pool(query, pool, rerank, RESULT_LIMIT)
    t_rerank = t_rrf + (time.perf_counter() - t0) * 1000

    return {
        "BM25 Keyword": (bm25_hits, t_bm25),
        "Dense Vector": (vec_hits, t_vec),
        "Hybrid RRF": ([(c[0], c[1]) for c in pool[:RESULT_LIMIT]], t_rrf),
        RERANK_MODE: (reranked, t_rerank),
    }


def run_retrieval_evaluation(dataset_path: str = DEFAULT_DATASET, embedder_url: str = DEFAULT_EMBEDDER_URL,
                             timeout_s: float = 30.0, model: Optional[str] = None,
                             query_model: Optional[str] = None) -> Dict[str, Any]:
    """Run all 4 modes over the dataset. Returns per-mode mean metrics plus coverage counts."""
    import numpy as np
    import psycopg

    from scripts.m0_skeleton import BGEEmbedder
    from services.common.config import config

    model = model or config.embedding_model
    query_model = resolve_query_model(query_model)
    print("=" * 70)
    print("ArxivLens — Information Retrieval Evaluation Suite (§11)")
    print("=" * 70)
    mismatch = model_mismatch_warning(model, query_model)
    if mismatch:
        print(mismatch)

    eval_queries = load_cases(dataset_path)
    skipped: List[str] = []
    missing_by_case: Dict[str, List[str]] = {}
    chunk_coverage: Dict[str, Dict[str, int]] = {}

    # Before any DB work or rerank call: refuse to measure the bi-encoder fallback as the reranker.
    require_cross_encoder(embedder_url, timeout_s)
    rerank = make_http_rerank(embedder_url, timeout_s)
    conn = psycopg.connect(config.postgres_url)
    embedder = BGEEmbedder(model_name=model)

    metric_names = ("recall@5", "mrr", "ndcg@5", "chunk_precision@5")
    results = {m: {**{n: [] for n in metric_names}, "latencies_ms": []} for m in MODES}

    with conn.cursor() as cur:
        for q_item in eval_queries:
            query = q_item["query"]
            relevant_arxiv_ids = list(q_item["relevant_arxiv_ids"])

            chunk_coverage.update(chunk_counts_for_case(cur, relevant_arxiv_ids, model))
            missing = missing_for_case(cur, relevant_arxiv_ids, model)
            if missing:
                # Skipped cases make coverage < 1.0, which fails the gate.
                skipped.append(q_item["id"])
                missing_by_case[q_item["id"]] = missing
                continue

            q_vec = embedder.embed_texts([query], is_query=True)[0]
            vec_literal = "[" + ",".join(f"{x:.6f}" for x in q_vec) + "]"

            for mode_name, (hits, lat) in run_modes_for_case(cur, query, vec_literal, model, rerank).items():
                for n, v in score_case(hits, relevant_arxiv_ids).items():
                    results[mode_name][n].append(v)
                results[mode_name]["latencies_ms"].append(lat)

    conn.close()

    summary: Dict[str, Any] = {"modes": {}, "cases_total": len(eval_queries), "skipped_case_ids": skipped,
                               "missing_arxiv_ids": missing_by_case, "chunk_coverage": chunk_coverage,
                               "rerank_path": PRODUCTION_RERANK_PATH, "embedder_url": embedder_url,
                               "embedding_model": model, "query_model": query_model, "rerank_pool_size": RRF_POOL_SIZE,
                               "result_limit": RESULT_LIMIT,
                               "recall_definition": "paper-level (ADR-0006); chunk_precision@5 is not gated"}
    summary["cases_evaluated"] = len(eval_queries) - len(skipped)

    def mean(xs: List[float]) -> float:
        return float(np.mean(xs)) if xs else 0.0

    print("\nBenchmark Results Summary Table (paper-level Recall@5 / MRR / NDCG@5; ChunkP@5 is diagnostic):")
    print(f"{'Configuration':<24} | {'Recall@5':<8} | {'MRR':<8} | {'NDCG@5':<8} | {'ChunkP@5':<8} | Avg ms")
    print("-" * 80)
    for mode, metrics in results.items():
        row = {n: mean(metrics[n]) for n in metric_names}
        row["avg_latency_ms"] = mean(metrics["latencies_ms"])
        summary["modes"][mode] = row
        print(f"{mode:<24} | {row['recall@5']:<8.4f} | {row['mrr']:<8.4f} | {row['ndcg@5']:<8.4f} | "
              f"{row['chunk_precision@5']:<8.4f} | {row['avg_latency_ms']:.2f}")
    print("=" * 80)
    print(f"Cases evaluated: {summary['cases_evaluated']}/{summary['cases_total']}")
    if skipped:
        print(f"WARNING: skipped cases (relevant paper(s) without {model} embeddings):")
        for cid in skipped:
            print(f"  {cid}: missing {', '.join(missing_by_case[cid])}")
    for w in partial_embedding_warnings(chunk_coverage, model):
        print(w)
    return summary


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Retrieval eval (paper-level Recall@5 / MRR / NDCG@5, plus "
                                     "diagnostic chunk precision@5) over 4 modes. Needs Postgres with the "
                                     "evals/corpus.txt papers indexed (DATABASE_URL) and the embedder API with "
                                     "the cross-encoder loaded.")
    parser.add_argument("--dataset", default=DEFAULT_DATASET, help="JSONL eval set (default: evals/retrieval.jsonl)")
    parser.add_argument("--json-out", help="Also write the summary as JSON to this path")
    parser.add_argument("--embedder-url", default=DEFAULT_EMBEDDER_URL,
                        help="Embedder API base URL for the production /internal/rerank "
                        "(env ARXIVLENS_EMBEDDER_URL, default http://localhost:8081)")
    parser.add_argument("--timeout", type=float, default=30.0, help="Per embedder API request timeout (s)")
    parser.add_argument("--query-model", default=None,
                        help="Model the query service runs with, if started with --model "
                        f"(env {QUERY_MODEL_ENV}; default: the --model default in cmd/arxivlens/main.go)")
    args = parser.parse_args(argv)
    summary = run_retrieval_evaluation(args.dataset, args.embedder_url, args.timeout, query_model=args.query_model)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

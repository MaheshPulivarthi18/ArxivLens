"""EVAL-1: no-infra checks for the golden eval sets and the eval gate logic.

Metric definitions: ADR-0006 (paper-level Recall@5) and ADR-0010 (rerank ordering) in
https://github.com/MaheshPulivarthi18/arxivlens-agents/tree/main/docs/adr
"""

import json
import math
import os
import re
import sys
import types

import pytest

from scripts import eval_gate, evaluate_answers, evaluate_retrieval

EVALS = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "evals")
ARXIV_ID = re.compile(r"^\d{4}\.\d{4,5}$")


def _jsonl(name):
    return evaluate_answers.load_cases(os.path.join(EVALS, name))


def _corpus():
    ids = []
    with open(os.path.join(EVALS, "corpus.txt"), encoding="utf-8") as f:
        for line in f:
            line = line.split("#", 1)[0].strip()
            if line:
                ids.append(line)
    return ids


SETS = {
    "retrieval.jsonl": ("query", "relevant_arxiv_ids", 30),
    "answer.jsonl": ("question", "expected_arxiv_ids", 20),
    "refusal.jsonl": ("question", None, 20),
}


def test_corpus_ids_valid_and_unique():
    ids = _corpus()
    assert ids and all(ARXIV_ID.match(i) for i in ids), ids
    assert len(ids) == len(set(ids))


@pytest.mark.parametrize("name", sorted(SETS))
def test_dataset_schema_size_and_corpus_membership(name):
    text_key, ids_key, min_size = SETS[name]
    cases = _jsonl(name)
    corpus = set(_corpus())
    assert len(cases) >= min_size
    expected_keys = {"id", text_key} | ({ids_key} if ids_key else set())
    for c in cases:
        assert set(c) == expected_keys, c
        assert isinstance(c[text_key], str) and c[text_key].strip()
        if ids_key:
            assert c[ids_key], c
            assert set(c[ids_key]) <= corpus, f"{c['id']} references IDs missing from corpus.txt"
    assert len({c["id"] for c in cases}) == len(cases)
    assert len({c[text_key].lower() for c in cases}) == len(cases)


def test_case_ids_unique_across_sets():
    all_ids = [c["id"] for name in SETS for c in _jsonl(name)]
    assert len(all_ids) == len(set(all_ids))


def test_retrieval_script_has_no_hardcoded_queries():
    with open(evaluate_retrieval.__file__, encoding="utf-8") as f:
        src = f.read()
    assert "target_arxiv" not in src
    assert '"query":' not in src
    assert len(evaluate_retrieval.load_cases()) >= 30


def test_thresholds_match_prd_section_8():
    t = eval_gate.load_thresholds()
    assert t["retrieval_recall_at_5_min"] == 0.80
    assert t["supported_citation_rate_min"] == 0.90
    assert t["correct_refusal_rate_min"] == 0.95
    assert t["ask_p95_latency_ms_max"] == 3000
    assert t["hybrid_rerank_beats_baselines"] is True


def _mode(r, m, n):
    return {"recall@5": r, "mrr": m, "ndcg@5": n, "avg_latency_ms": 1.0}


def _results(recall=0.85, supported=0.95, refusal=1.0, p95=1200.0, evaluated=34):
    return {
        "retrieval": {"cases_total": 34, "cases_evaluated": evaluated,
                      "rerank_path": evaluate_retrieval.PRODUCTION_RERANK_PATH, "modes": {
            "BM25 Keyword": _mode(0.5, 0.5, 0.5),
            "Dense Vector": _mode(0.6, 0.6, 0.6),
            "Hybrid RRF": _mode(0.7, 0.7, 0.7),
            "Hybrid RRF + Rerank": _mode(recall, 0.8, 0.8),
        }},
        "answers": {"supported_citation_rate": supported, "correct_refusal_rate": refusal,
                    "ask_p95_ms": p95, "ask_p50_ms": 500.0, "expected_paper_hit_rate": 0.9,
                    "ask_error_count": 0},
    }


def test_gate_passes_when_all_targets_met():
    checks = eval_gate.check_thresholds(_results(), eval_gate.load_thresholds())
    assert all(c["passed"] for c in checks), checks


@pytest.mark.parametrize("override", [
    {"recall": 0.79}, {"supported": 0.89}, {"refusal": 0.94}, {"p95": 3000.0}, {"evaluated": 33},
])
def test_gate_fails_below_any_threshold(override):
    checks = eval_gate.check_thresholds(_results(**override), eval_gate.load_thresholds())
    assert not all(c["passed"] for c in checks)


def test_gate_fails_when_hybrid_does_not_beat_baselines():
    res = _results()
    res["retrieval"]["modes"]["BM25 Keyword"]["mrr"] = 0.9
    assert not all(c["passed"] for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds()))


def test_gate_fails_on_partial_run():
    res = _results()
    res["answers"] = None
    assert not all(c["passed"] for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds()))


def test_gate_fails_when_rerank_not_from_production_path():
    for bad in (None, "local bi-encoder dot product"):
        res = _results()
        if bad is None:
            del res["retrieval"]["rerank_path"]
        else:
            res["retrieval"]["rerank_path"] = bad
        checks = {c["metric"]: c for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds())}
        assert not checks["rerank via production /internal/rerank"]["passed"]
        assert checks[f"recall@5 ({eval_gate.GATED_MODE})"]["value"] is None  # not gated on untrusted numbers
        assert not all(c["passed"] for c in checks.values())


# --- BLOCKER 2: the gated mode goes through the production rerank endpoint (no infra; HTTP mocked) ---

class _FakeResp:
    def __init__(self, payload):
        self._body = json.dumps(payload).encode("utf-8")

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def read(self):
        return self._body


class _FakeCursor:
    """Answers the coverage and mode queries of the retrieval eval by matching the SQL text."""

    def __init__(self, pool, embedded=("1706.03762", "1810.04805"), chunks=None):
        self.pool = pool  # RRF pool rows: (chunk_id, arxiv_id, content, rrf_score)
        self.embedded = embedded  # arXiv IDs with embeddings for the current model
        # {arxiv_id: (embedded_chunks, total_chunks)}; default: every embedded paper fully embedded (3/3)
        self.chunks = chunks if chunks is not None else {a: (3, 3) for a in embedded}
        self.executed = []

    def execute(self, sql, params):
        self.executed.append((sql, params))

    def fetchall(self):
        sql, params = self.executed[-1]
        if sql == evaluate_retrieval.COVERAGE_SQL:
            return [(a,) for a in params["ids"] if a in self.embedded]
        if sql == evaluate_retrieval.BM25_SQL:
            return [(1, "1810.04805"), (2, "1706.03762")]
        if sql == evaluate_retrieval.DENSE_SQL:
            return [(2, "1706.03762"), (1, "1810.04805")]
        if sql == evaluate_retrieval.RRF_POOL_SQL:
            return self.pool[:params["pool"]]
        if sql == evaluate_retrieval.CHUNK_COUNTS_SQL:
            return [(a, *self.chunks[a]) for a in params["ids"] if a in self.chunks]
        raise AssertionError(f"unexpected SQL: {sql}")


def _pool(n=20):
    # Alternating papers so the reranked top 5 is distinguishable from the RRF top 5.
    return [(i, "1706.03762" if i % 2 else "1810.04805", f"chunk {i}", 1.0 / (60 + i)) for i in range(1, n + 1)]


def test_gated_mode_calls_production_rerank_endpoint(monkeypatch):
    calls = []

    def fake_urlopen(req, timeout=None):
        body = json.loads(req.data.decode("utf-8"))
        calls.append((req.full_url, req.get_method(), body, timeout))
        return _FakeResp({"scores": [float(i) for i in range(len(body["passages"]))]})  # reverses RRF order

    monkeypatch.setattr(evaluate_retrieval.urllib.request, "urlopen", fake_urlopen)
    rerank = evaluate_retrieval.make_http_rerank("http://embedder:8081/", timeout_s=7.0)
    out = evaluate_retrieval.run_modes_for_case(_FakeCursor(_pool()), "what is attention?", "[0.1]", "M", rerank)

    assert eval_gate.GATED_MODE == evaluate_retrieval.RERANK_MODE
    assert calls == [("http://embedder:8081/internal/rerank", "POST",
                      {"query": "what is attention?", "passages": [f"chunk {i}" for i in range(1, 21)]}, 7.0)]
    assert [c for c, _ in out["Hybrid RRF"][0]] == [1, 2, 3, 4, 5]
    assert [c for c, _ in out[eval_gate.GATED_MODE][0]] == [20, 19, 18, 17, 16]  # ordered by the endpoint's scores


# --- R3-4 (ADR-0010 parity): pool of 20, one rerank call, stable sort, top 5; SQL mirrors search.go ---

def test_adr0010_parity_pool_one_call_stable_ties_top5():
    cur = _FakeCursor(_pool(30))  # more rows available than the pool size
    calls = []

    def rerank(query, passages):
        calls.append(list(passages))
        # Ties everywhere except chunk 7 and chunk 12; ties must keep RRF order.
        return [0.9 if p in ("chunk 7", "chunk 12") else 0.5 for p in passages]

    out = evaluate_retrieval.run_modes_for_case(cur, "q", "[0.1]", "BAAI/bge-base-en-v1.5", rerank)
    pool_params = [p for sql, p in cur.executed if sql == evaluate_retrieval.RRF_POOL_SQL]
    assert pool_params == [{"vec": "[0.1]", "model": "BAAI/bge-base-en-v1.5", "query": "q", "pool": 20}]
    assert len(calls) == 1 and len(calls[0]) == 20
    assert [c for c, _ in out[evaluate_retrieval.RERANK_MODE][0]] == [7, 12, 1, 2, 3]
    assert evaluate_retrieval.RRF_POOL_SIZE == 20 and evaluate_retrieval.RESULT_LIMIT == 5


def test_rerank_pool_empty_makes_no_call():
    assert evaluate_retrieval.rerank_pool("q", [], lambda q, p: pytest.fail("rerank called")) == []


def _norm_sql(sql):
    return re.sub(r"\s+", " ", sql).strip()


# Every allowed difference between search.go's HybridSearch SQL (after whitespace + placeholder normalisation) and
# RRF_POOL_SQL, as (search.go fragment, eval fragment). Each search.go fragment must occur exactly once.
_GO_TO_EVAL_SQL = [
    # vector arm: no category filter in the eval, so no papers join
    ("JOIN paper_chunks pc ON pc.id = ce.chunk_id JOIN papers p ON p.id = pc.paper_id WHERE ce.model = $2 "
     "AND ($3::text[] IS NULL OR p.categories && $3) ORDER BY",
     "JOIN paper_chunks pc ON pc.id = ce.chunk_id WHERE ce.model = $2 ORDER BY"),
    # keyword arm: same, no category filter
    ("FROM paper_chunks pc JOIN papers p ON p.id = pc.paper_id, websearch_to_tsquery('english', $4) q "
     "WHERE pc.content_tsv @@ q AND ($3::text[] IS NULL OR p.categories && $3) ORDER BY",
     "FROM paper_chunks pc, websearch_to_tsquery('english', $4) q WHERE pc.content_tsv @@ q ORDER BY"),
    # outer SELECT: the eval needs only (id, arxiv_id, content); no titles, so no paper_sections join
    ("SELECT c.id, c.content, p.arxiv_id, p.title, COALESCE(ps.title, '') AS section_title,",
     "SELECT c.id, p.arxiv_id, c.content,"),
    ("JOIN papers p ON p.id = c.paper_id LEFT JOIN paper_sections ps ON ps.id = c.section_id LEFT JOIN",
     "JOIN papers p ON p.id = c.paper_id LEFT JOIN"),
    # deterministic tiebreak for equal RRF scores (production leaves their order unspecified)
    ("ORDER BY rrf_score DESC LIMIT $5;", "ORDER BY rrf_score DESC, c.id LIMIT $5;"),
]
_EVAL_PLACEHOLDERS = {"%(vec)s": "$1", "%(model)s": "$2", "%(query)s": "$4", "%(pool)s": "$5"}


def test_rrf_pool_sql_mirrors_search_go():
    sql = evaluate_retrieval.RRF_POOL_SQL
    assert sql.count("LIMIT 50") == 2 and evaluate_retrieval.RRF_ARM_LIMIT == 50
    assert "WHERE ce.model = %(model)s" in sql
    assert "1.0/(60 + v.rank)" in sql and "1.0/(60 + k.rank)" in sql
    assert "ORDER BY ce.embedding <=> %(vec)s::halfvec" in sql
    assert "ORDER BY ts_rank_cd(pc.content_tsv, q) DESC" in sql
    assert "ce.model = %(model)s" in evaluate_retrieval.DENSE_SQL
    go = os.path.join(os.path.dirname(EVALS), "internal", "query", "search.go")
    # drift guard: production still uses the values mirrored above. A missing search.go fails (no silent skip).
    assert os.path.exists(go), f"{go} missing: the ADR-0010 parity guard cannot run"
    with open(go, encoding="utf-8") as f:
        src = f.read()
    assert src.count("LIMIT 50") == 2
    assert "WHERE ce.model = $2" in src
    assert "1.0/(60 + v.rank)" in src and "1.0/(60 + k.rank)" in src

    # (b) pool size: const RerankPool in search.go == RRF_POOL_SIZE
    pool = re.findall(r"^const RerankPool = (\d+)\s*$", src, re.M)
    assert pool == [str(evaluate_retrieval.RRF_POOL_SIZE)], pool

    # (c) whole-query parity: normalised search.go SQL with only the listed differences == RRF_POOL_SQL
    go_sql = re.findall(r"querySQL := `(.*?)`", src, re.S)
    assert len(go_sql) == 1, "HybridSearch querySQL not found exactly once in search.go"
    go_norm = _norm_sql(go_sql[0])
    for go_frag, eval_frag in _GO_TO_EVAL_SQL:
        assert go_norm.count(go_frag) == 1, f"allowed difference not found once in search.go: {go_frag!r}"
        go_norm = go_norm.replace(go_frag, eval_frag)
    eval_norm = _norm_sql(sql)
    for name, n in _EVAL_PLACEHOLDERS.items():
        eval_norm = eval_norm.replace(name, n)
    assert "%(" not in eval_norm, eval_norm
    assert eval_norm == go_norm


# --- R3-1/R3-2/R3-3 (ADR-0006): paper-level recall vs chunk precision ---

def _hits(*arxiv_ids):
    return [(i, a) for i, a in enumerate(arxiv_ids, start=1)]


def test_one_relevant_paper_filling_top5_is_full_recall():
    m = evaluate_retrieval.score_case(_hits(*["1706.03762"] * 5), ["1706.03762"])
    assert m["recall@5"] == 1.0 and m["chunk_precision@5"] == 1.0
    assert m["mrr"] == 1.0 and m["ndcg@5"] == 1.0


def test_best_chunk_first_then_other_papers_is_full_recall_low_precision():
    # The ADR-0006 example: precision@5-like scoring gave 0.2 here; paper-level recall is 1.0.
    m = evaluate_retrieval.score_case(_hits("1706.03762", "1810.04805", "1512.03385", "2005.14165", "1910.10683"),
                                      ["1706.03762"])
    assert m["recall@5"] == 1.0
    assert m["chunk_precision@5"] == pytest.approx(0.2)


def test_two_relevant_papers_one_retrieved_is_half_recall():
    m = evaluate_retrieval.score_case(_hits(*["1706.03762"] * 5), ["1706.03762", "1810.04805"])
    assert m["recall@5"] == 0.5
    assert m["chunk_precision@5"] == 1.0  # precision can't tell that a relevant paper is missing
    assert m["mrr"] == 1.0
    assert m["ndcg@5"] == pytest.approx(1.0 / (1.0 + 1.0 / math.log2(3)))


def test_duplicate_chunks_do_not_raise_recall():
    one = evaluate_retrieval.score_case(_hits("1706.03762", "1512.03385"), ["1706.03762", "1810.04805"])
    dup = evaluate_retrieval.score_case(_hits("1706.03762", "1706.03762", "1706.03762", "1512.03385"),
                                        ["1706.03762", "1810.04805"])
    assert one["recall@5"] == dup["recall@5"] == 0.5
    assert dup["ndcg@5"] == one["ndcg@5"]
    assert dup["chunk_precision@5"] > one["chunk_precision@5"]


def test_paper_level_mrr_and_ndcg_use_deduped_ranking():
    # Chunks: A A A B C -> papers A B C. Relevant C is paper rank 3 (chunk rank 5).
    m = evaluate_retrieval.score_case(_hits("1512.03385", "1512.03385", "1512.03385", "2005.14165", "1706.03762"),
                                      ["1706.03762"])
    assert m["mrr"] == pytest.approx(1 / 3)
    assert m["ndcg@5"] == pytest.approx(1.0 / math.log2(4))
    assert m["recall@5"] == 1.0
    # Paper beyond the top 5 papers does not count; versioned IDs are normalised.
    m = evaluate_retrieval.score_case(_hits("1", "2", "3", "4", "5", "1706.03762v7"), ["1706.03762"])
    assert m["recall@5"] == 0.0 and m["mrr"] == pytest.approx(1 / 6)
    assert evaluate_retrieval.dedupe_to_papers(["a", "b", "a", "c", "b"]) == ["a", "b", "c"]


def test_recall_denominator_is_all_relevant_papers_not_capped_at_k():
    relevant = ["1", "2", "3", "4", "5", "6"]
    m = evaluate_retrieval.score_case(_hits("1", "2", "3", "4", "5"), relevant)
    assert m["recall@5"] == pytest.approx(5 / 6)  # ADR-0006: / |relevant_arxiv_ids|, never min(|relevant|, 5)


def test_chunk_precision_divides_by_k_even_with_fewer_hits():
    m = evaluate_retrieval.score_case(_hits("1706.03762", "1706.03762"), ["1706.03762"])
    assert m["chunk_precision@5"] == pytest.approx(0.4) and m["recall@5"] == 1.0


def test_gate_ignores_chunk_precision():
    res = _results()
    for m in res["retrieval"]["modes"].values():
        m["chunk_precision@5"] = 0.0
    assert all(c["passed"] for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds()))
    res = _results(recall=0.79)
    res["retrieval"]["modes"][eval_gate.GATED_MODE]["chunk_precision@5"] = 1.0
    checks = {c["metric"]: c for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds())}
    assert checks[f"recall@5 ({eval_gate.GATED_MODE})"]["value"] == 0.79
    assert not checks[f"recall@5 ({eval_gate.GATED_MODE})"]["passed"]
    with open(eval_gate.__file__, encoding="utf-8") as f:
        assert '["chunk_precision@5"]' not in f.read()  # the gate only reads it with .get() for the info row


def test_retrieval_script_has_no_local_bi_encoder_rerank():
    with open(evaluate_retrieval.__file__, encoding="utf-8") as f:
        src = f.read()
    assert "np.dot" not in src and "cand_vecs" not in src


@pytest.mark.parametrize("payload", [{"scores": [0.5]}, {"detail": "boom"}, ["not", "a", "dict"]])
def test_production_rerank_bad_response_raises(monkeypatch, payload):
    monkeypatch.setattr(evaluate_retrieval.urllib.request, "urlopen", lambda req, timeout=None: _FakeResp(payload))
    rerank = evaluate_retrieval.make_http_rerank("http://embedder:8081")
    with pytest.raises(evaluate_retrieval.RerankError):
        rerank("q", ["a", "b"])


def test_production_rerank_http_error_raises(monkeypatch):
    def boom(req, timeout=None):
        raise evaluate_retrieval.urllib.error.URLError("connection refused")
    monkeypatch.setattr(evaluate_retrieval.urllib.request, "urlopen", boom)
    with pytest.raises(evaluate_retrieval.RerankError):
        evaluate_retrieval.make_http_rerank("http://embedder:8081")("q", ["a"])


@pytest.mark.parametrize("health,ok", [({"status": "ok", "reranker": True}, True),
                                       ({"status": "ok", "reranker": False}, False),
                                       ({"status": "ok"}, False)])
def test_require_cross_encoder_rejects_bi_encoder_fallback(monkeypatch, health, ok):
    seen = []

    def fake_urlopen(url, timeout=None):
        seen.append(url)
        return _FakeResp(health)

    monkeypatch.setattr(evaluate_retrieval.urllib.request, "urlopen", fake_urlopen)
    if ok:
        evaluate_retrieval.require_cross_encoder("http://embedder:8081")
    else:
        with pytest.raises(evaluate_retrieval.RerankError):
            evaluate_retrieval.require_cross_encoder("http://embedder:8081")
    assert seen == ["http://embedder:8081/healthz"]


# --- MAJOR 3: coverage is per arXiv ID ---

def test_coverage_two_paper_case_with_one_indexed_reports_missing_id():
    rows = [("1706.03762", 1), ("1706.03762", 2)]
    assert evaluate_retrieval.missing_arxiv_ids(["1706.03762", "1810.04805"], rows) == ["1810.04805"]
    assert evaluate_retrieval.missing_arxiv_ids(["1706.03762"], rows) == []
    assert evaluate_retrieval.missing_arxiv_ids(["1810.04805", "1512.03385"], []) == ["1810.04805", "1512.03385"]


def test_coverage_requires_embeddings_for_current_model():
    # R3-6: a paper with chunks but no embeddings (or embeddings for another model) is not indexed.
    sql = evaluate_retrieval.COVERAGE_SQL
    assert "JOIN chunk_embeddings ce ON ce.chunk_id = pc.id" in sql and "ce.model = %(model)s" in sql
    # Fake DB: (arxiv_id, chunk_id) chunk rows and (chunk_id, model) embedding rows, evaluated like the SQL.
    chunks = [("1706.03762", 1), ("1706.03762", 2), ("1810.04805", 3), ("1512.03385", 4)]
    embeddings = [(1, "M"), (3, "OTHER")]  # 1810.04805 embedded with another model, 1512.03385 not at all

    class Cur:
        def execute(self, q, params):
            assert q == sql
            self.p = params

        def fetchall(self):
            emb = {c for c, m in embeddings if m == self.p["model"]}
            return sorted({(a,) for a, c in chunks if c in emb and a in self.p["ids"]})

    ids = ["1706.03762", "1810.04805", "1512.03385"]
    assert evaluate_retrieval.missing_for_case(Cur(), ids, "M") == ["1810.04805", "1512.03385"]
    assert evaluate_retrieval.missing_for_case(Cur(), ids, "OTHER") == ["1706.03762", "1512.03385"]
    assert evaluate_retrieval.missing_for_case(Cur(), ["1706.03762"], "M") == []


def test_gate_fails_when_a_two_paper_case_is_partially_indexed():
    # One skipped case (missing one of its two papers) means coverage 33/34, which must fail the gate.
    res = _results(evaluated=33)
    res["retrieval"]["missing_arxiv_ids"] = {"r010": ["1810.04805"]}
    checks = {c["metric"]: c for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds())}
    assert not checks["retrieval_coverage"]["passed"]


def test_answer_metrics_with_stub_ask():
    answers = [{"id": "a1", "question": "q1", "expected_arxiv_ids": ["1706.03762"]},
               {"id": "a2", "question": "q2", "expected_arxiv_ids": ["1810.04805"]}]
    refusals = [{"id": "x1", "question": "r1"}, {"id": "x2", "question": "r2"}]
    canned = {
        "q1": {"answer": "A [1].", "citations": [{"marker": 1, "arxiv_id": "1706.03762v7", "supported": True},
                                                 {"marker": 2, "arxiv_id": "1810.04805", "supported": False}]},
        "q2": None,  # request error
        "r1": {"answer": "No indexed papers are relevant to this question.", "citations": []},
        "r2": {"answer": "B [1].", "citations": [{"marker": 1, "arxiv_id": "1512.03385", "supported": True}]},
    }
    m = evaluate_answers.evaluate(answers, refusals, lambda q: (canned[q], 100.0))
    assert m["supported_citation_rate"] == 0.5
    assert m["expected_paper_hit_rate"] == 0.5
    assert m["correct_refusal_rate"] == 0.5
    assert m["error_case_ids"] == ["a2"]
    assert m["requests_ok"] == 3
    assert m["ask_error_count"] == 1
    assert m["ask_p95_ms"] == 100.0


def test_failed_requests_count_in_latency_and_not_as_refusals():
    m = evaluate_answers.evaluate([], [{"id": "x1", "question": "r1"}], lambda q: (None, 30000.0))
    assert m["correct_refusal_rate"] == 0.0
    assert m["ask_p95_ms"] == 30000.0  # timeouts are recorded at their elapsed time
    assert m["ask_error_count"] == 1
    res = _results()
    res["answers"]["ask_p95_ms"] = None
    assert not all(c["passed"] for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds()))


def test_gate_fails_when_some_answer_requests_fail_even_if_refusals_perfect():
    answers = [{"id": f"a{i}", "question": f"q{i}", "expected_arxiv_ids": ["1706.03762"]} for i in range(22)]
    refusals = [{"id": f"x{i}", "question": f"r{i}"} for i in range(22)]
    good = {"answer": "A [1].", "citations": [{"marker": 1, "arxiv_id": "1706.03762", "supported": True}]}

    def ask(q):
        if q.startswith("r"):
            return {"answer": "No indexed papers are relevant to this question.", "citations": []}, 50.0
        if int(q[1:]) < 2:  # 2 of 22 in-corpus calls fail fast; the rest are perfect
            return None, 10.0
        return good, 100.0

    m = evaluate_answers.evaluate(answers, refusals, ask)
    assert m["correct_refusal_rate"] == 1.0 and m["supported_citation_rate"] == 1.0
    res = _results()
    res["answers"] = m
    checks = {c["metric"]: c for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds())}
    assert not checks["ask_errors (answer + refusal sets)"]["passed"]
    assert not all(c["passed"] for c in checks.values())


def test_http_ask_records_timeout_at_least_timeout(monkeypatch):
    def boom(*a, **k):
        raise evaluate_answers.urllib.error.URLError(TimeoutError("timed out"))
    monkeypatch.setattr(evaluate_answers.urllib.request, "urlopen", boom)
    resp, ms = evaluate_answers.make_http_ask("http://127.0.0.1:9", timeout_s=2.0)("q")
    assert resp is None and ms >= 2000.0


def test_percentile_nearest_rank():
    assert evaluate_answers.percentile(list(range(1, 101)), 95) == 95
    assert evaluate_answers.percentile([], 95) is None
    assert json.dumps(evaluate_answers.percentile([3.0], 50)) == "3.0"


# --- R3-5: run_retrieval_evaluation checks /healthz before any rerank and fails closed (all infra stubbed) ---

def _stub_infra(monkeypatch, events, health, cursor):
    class Conn:
        def cursor(self):
            events.append("db.cursor")
            return self

        def __enter__(self):
            return cursor

        def __exit__(self, *a):
            return False

        def close(self):
            pass

    class Embedder:
        def __init__(self, model_name=None):
            events.append(f"embedder:{model_name}")

        def embed_texts(self, texts, is_query=False):
            return [[0.1, 0.2]]

    def connect(url):
        events.append("db.connect")
        return Conn()

    def fake_urlopen(req, timeout=None):
        url = req if isinstance(req, str) else req.full_url
        events.append(url.rsplit("/", 1)[-1] if url.endswith("healthz") else "rerank")
        if url.endswith("/healthz"):
            return _FakeResp(health)
        return _FakeResp({"scores": [0.0] * len(json.loads(req.data)["passages"])})

    monkeypatch.setitem(sys.modules, "psycopg", types.SimpleNamespace(connect=connect))
    monkeypatch.setitem(sys.modules, "scripts.m0_skeleton", types.SimpleNamespace(BGEEmbedder=Embedder))
    monkeypatch.setitem(sys.modules, "services.common.config", types.SimpleNamespace(
        config=types.SimpleNamespace(postgres_url="postgresql://stub", embedding_model="M")))
    monkeypatch.setattr(evaluate_retrieval.urllib.request, "urlopen", fake_urlopen)


def _two_cases(tmp_path):
    path = tmp_path / "r.jsonl"
    path.write_text(json.dumps({"id": "r1", "query": "q1", "relevant_arxiv_ids": ["1706.03762"]}) + "\n" +
                    json.dumps({"id": "r2", "query": "q2", "relevant_arxiv_ids": ["1706.03762", "1512.03385"]}) + "\n")
    return str(path)


def test_run_checks_healthz_before_any_rerank_and_reports_metrics(monkeypatch, tmp_path):
    events = []
    _stub_infra(monkeypatch, events, {"status": "ok", "reranker": True}, _FakeCursor(_pool()))
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)
    assert events[0] == "healthz" and events.count("healthz") == 1
    assert events.index("healthz") < events.index("rerank") and events.count("rerank") == 1
    assert "embedder:M" in events
    assert s["cases_evaluated"] == 1 and s["skipped_case_ids"] == ["r2"]
    assert s["missing_arxiv_ids"] == {"r2": ["1512.03385"]}
    assert s["embedding_model"] == "M" and s["rerank_pool_size"] == 20
    gated = s["modes"][evaluate_retrieval.RERANK_MODE]
    # Ties (all 0.0) keep RRF order: chunks 1..5 -> papers 1706, 1810 -> relevant 1706 at rank 1.
    assert gated["recall@5"] == 1.0 and gated["mrr"] == 1.0
    assert gated["chunk_precision@5"] == pytest.approx(0.6)


@pytest.mark.parametrize("health", [{"status": "ok", "reranker": False}, {"status": "ok"}])
def test_run_with_failed_healthz_yields_no_summary(monkeypatch, tmp_path, health):
    events = []
    _stub_infra(monkeypatch, events, health, _FakeCursor(_pool()))
    with pytest.raises(evaluate_retrieval.RerankError):
        evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)
    assert events == ["healthz"]  # no DB, no embedder, no rerank call


# --- EVAL-5 AC1: a rerank failure stops the run; no fallback to RRF order, no summary ---

def _bad_rerank_raises(query, passages):
    raise evaluate_retrieval.RerankError("POST /internal/rerank failed: 503")


def _bad_rerank_short(query, passages):
    return [1.0] * (len(passages) - 1)  # score-count mismatch


def _bad_rerank_long(query, passages):
    return [1.0] * (len(passages) + 1)


@pytest.mark.parametrize("bad", [_bad_rerank_raises, _bad_rerank_short, _bad_rerank_long])
def test_rerank_failure_in_run_modes_for_case_raises(bad):
    with pytest.raises(evaluate_retrieval.RerankError):
        evaluate_retrieval.run_modes_for_case(_FakeCursor(_pool()), "q", "[0.1]", "M", bad)


@pytest.mark.parametrize("bad", [_bad_rerank_raises, _bad_rerank_short])
def test_rerank_failure_stops_run_with_no_summary(monkeypatch, tmp_path, bad):
    events = []
    _stub_infra(monkeypatch, events, {"status": "ok", "reranker": True}, _FakeCursor(_pool()))
    monkeypatch.setattr(evaluate_retrieval, "make_http_rerank", lambda url, timeout: bad)
    out = tmp_path / "summary.json"
    with pytest.raises(evaluate_retrieval.RerankError):
        evaluate_retrieval.main(["--dataset", _two_cases(tmp_path), "--json-out", str(out),
                                 "--embedder-url", "http://embedder:8081"])
    assert not out.exists()  # no summary written
    with pytest.raises(evaluate_retrieval.RerankError):
        evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)


# --- EVAL-5 AC2: partly embedded relevant papers are recorded and warned about (not gated) ---

def test_partial_embedding_recorded_and_warned_without_failing(monkeypatch, tmp_path, capsys):
    cur = _FakeCursor(_pool(), chunks={"1706.03762": (2, 5), "1512.03385": (0, 4)})
    _stub_infra(monkeypatch, [], {"status": "ok", "reranker": True}, cur)
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)
    count_params = [p for sql, p in cur.executed if sql == evaluate_retrieval.CHUNK_COUNTS_SQL]
    assert count_params == [{"model": "M", "ids": ["1706.03762"]},
                            {"model": "M", "ids": ["1706.03762", "1512.03385"]}]
    assert s["chunk_coverage"] == {"1706.03762": {"embedded_chunks": 2, "total_chunks": 5},
                                   "1512.03385": {"embedded_chunks": 0, "total_chunks": 4}}
    out = capsys.readouterr().out
    assert "WARNING: 1706.03762 has only 2/5 chunks embedded for M" in out
    assert "WARNING: 1512.03385 has only 0/4 chunks embedded for M" in out
    # warning only: the partly embedded r1 is still evaluated (coverage rules unchanged)
    assert s["cases_evaluated"] == 1 and s["skipped_case_ids"] == ["r2"]
    assert json.loads(json.dumps(s))["chunk_coverage"] == s["chunk_coverage"]  # survives --json-out


def test_chunk_counts_fully_embedded_and_unknown_papers():
    cur = _FakeCursor(_pool(), chunks={"1706.03762": (3, 3)})
    counts = evaluate_retrieval.chunk_counts_for_case(cur, ["1706.03762", "1810.04805"], "M")
    assert counts == {"1706.03762": {"embedded_chunks": 3, "total_chunks": 3},
                      "1810.04805": {"embedded_chunks": 0, "total_chunks": 0}}
    assert evaluate_retrieval.partial_embedding_warnings(counts, "M") == []
    sql = evaluate_retrieval.CHUNK_COUNTS_SQL
    assert "LEFT JOIN chunk_embeddings ce ON ce.chunk_id = pc.id AND ce.model = %(model)s" in sql
    assert "COUNT(ce.chunk_id) AS embedded_chunks" in sql and "COUNT(pc.id) AS total_chunks" in sql



@pytest.mark.skipif(not os.getenv("DATABASE_URL"), reason="opt-in: needs DATABASE_URL with migrations/ applied")
def test_chunk_counts_sql_against_real_postgres():
    # H3: CHUNK_COUNTS_SQL on the real schema (pgvector halfvec): one paper with 0/2 chunks embedded, one with 1/2.
    psycopg = pytest.importorskip("psycopg")
    vec = "[" + ",".join(["0.1"] * 768) + "]"
    conn = psycopg.connect(os.environ["DATABASE_URL"])
    try:
        cur = conn.cursor()
        chunk_ids = {}
        for arxiv_id in ("eval5.zero", "eval5.part"):
            cur.execute("INSERT INTO papers (arxiv_id) VALUES (%s) RETURNING id", (arxiv_id,))
            paper_id = cur.fetchone()[0]
            for i in range(2):
                cur.execute("INSERT INTO paper_chunks (paper_id, chunk_index, content, content_hash) "
                            "VALUES (%s, %s, %s, %s) RETURNING id", (paper_id, i, f"text {i}", f"h{i}"))
                chunk_ids.setdefault(arxiv_id, []).append(cur.fetchone()[0])
        cur.execute("INSERT INTO chunk_embeddings (chunk_id, model, embedding) VALUES (%s, %s, %s::halfvec)",
                    (chunk_ids["eval5.part"][0], "M", vec))
        cur.execute("INSERT INTO chunk_embeddings (chunk_id, model, embedding) VALUES (%s, %s, %s::halfvec)",
                    (chunk_ids["eval5.zero"][0], "other-model", vec))  # another model's vector doesn't count
        counts = evaluate_retrieval.chunk_counts_for_case(cur, ["eval5.zero", "eval5.part", "eval5.none"], "M")
        print(counts)
        assert counts == {"eval5.zero": {"embedded_chunks": 0, "total_chunks": 2},
                          "eval5.part": {"embedded_chunks": 1, "total_chunks": 2},
                          "eval5.none": {"embedded_chunks": 0, "total_chunks": 0}}
    finally:
        conn.rollback()
        conn.close()

# --- EVAL-5 AC3 / H1-H2: the model the eval uses vs the model the Go query service runs with ---

def _python_default_model():
    src = open(os.path.join(evaluate_retrieval.REPO_ROOT, "services", "common", "config.py"), encoding="utf-8").read()
    found = re.findall(r'os\.getenv\(\s*"EMBEDDING_MODEL"\s*,\s*"([^"]+)"\s*\)', src)
    assert len(found) == 1, found
    return found[0]


def test_go_model_default_parsed_from_main_go_matches_python_default():
    # H1: the Go --model default is read from cmd/arxivlens/main.go, not duplicated; it must equal the Python default.
    assert not hasattr(evaluate_retrieval, "GO_DEFAULT_MODEL")
    go_default = evaluate_retrieval.go_default_model()
    assert go_default == _python_default_model(), (
        f"cmd/arxivlens/main.go --model default {go_default!r} != services/common/config.py EMBEDDING_MODEL default "
        f"{_python_default_model()!r}")


def test_go_model_default_parser(tmp_path):
    go = tmp_path / "main.go"
    go.write_text('cmd.Flags().StringVar(&port, "port-name", "x", "")\n'
                  'cmd.Flags().StringVar( &modelName ,\n\t\t"model", "org/m-1.5", "Embedding model name")\n')
    assert evaluate_retrieval.go_default_model(str(go)) == "org/m-1.5"
    for body in ['cmd.Flags().StringVar(&m, "models", "a", "")\n',  # no --model flag
                 'f.StringVar(&a, "model", "a", "")\nf.StringVar(&b, "model", "b", "")\n']:  # ambiguous
        go.write_text(body)
        with pytest.raises(ValueError):
            evaluate_retrieval.go_default_model(str(go))


def test_model_mismatch_warning(monkeypatch, tmp_path, capsys):
    monkeypatch.delenv(evaluate_retrieval.QUERY_MODEL_ENV, raising=False)
    go_default = evaluate_retrieval.go_default_model()
    assert evaluate_retrieval.resolve_query_model() == go_default
    assert evaluate_retrieval.model_mismatch_warning(go_default, go_default) is None
    assert "EMBEDDING_MODEL" in evaluate_retrieval.model_mismatch_warning("other/model", go_default)
    _stub_infra(monkeypatch, [], {"status": "ok", "reranker": True}, _FakeCursor(_pool()))  # EMBEDDING_MODEL = "M"
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)
    assert s["embedding_model"] == "M" and s["query_model"] == go_default
    assert f"WARNING: eval embedding model 'M' (EMBEDDING_MODEL) differs from the query service model {go_default!r}" \
        in capsys.readouterr().out
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0,
                                                    model=go_default)
    assert s["embedding_model"] == go_default
    assert "differs" not in capsys.readouterr().out


def test_model_mismatch_uses_explicit_query_model_override(monkeypatch, tmp_path, capsys):
    # H2: the query service started with --model X -> compare against X, not the Go default.
    monkeypatch.delenv(evaluate_retrieval.QUERY_MODEL_ENV, raising=False)
    _stub_infra(monkeypatch, [], {"status": "ok", "reranker": True}, _FakeCursor(_pool()))  # EMBEDDING_MODEL = "M"
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0,
                                                    query_model="M")
    assert s["query_model"] == "M" and "differs" not in capsys.readouterr().out  # override matches -> no warning
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0,
                                                    query_model="org/large")
    assert s["query_model"] == "org/large"
    assert "differs from the query service model 'org/large'" in capsys.readouterr().out  # override differs
    monkeypatch.setenv(evaluate_retrieval.QUERY_MODEL_ENV, "M")  # env form of the override
    assert evaluate_retrieval.resolve_query_model() == "M"
    s = evaluate_retrieval.run_retrieval_evaluation(_two_cases(tmp_path), "http://embedder:8081", 5.0)
    assert s["query_model"] == "M"
    assert "differs" not in capsys.readouterr().out
    seen = {}
    monkeypatch.setattr(evaluate_retrieval, "run_retrieval_evaluation", lambda *a, **kw: seen.update(kw) or {})
    assert evaluate_retrieval.main(["--query-model", "org/large"]) == 0 and seen["query_model"] == "org/large"

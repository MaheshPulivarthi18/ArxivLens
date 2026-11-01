"""EVAL-1: no-infra checks for the golden eval sets and the eval gate logic."""

import json
import os
import re

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
        "retrieval": {"cases_total": 34, "cases_evaluated": evaluated, "modes": {
            "BM25 Keyword": _mode(0.5, 0.5, 0.5),
            "Dense Vector": _mode(0.6, 0.6, 0.6),
            "Hybrid RRF": _mode(0.7, 0.7, 0.7),
            "Hybrid RRF + Rerank": _mode(recall, 0.8, 0.8),
        }},
        "answers": {"supported_citation_rate": supported, "correct_refusal_rate": refusal,
                    "ask_p95_ms": p95, "ask_p50_ms": 500.0, "expected_paper_hit_rate": 0.9},
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
    assert m["ask_p95_ms"] == 100.0


def test_failed_requests_do_not_count_as_fast_or_refused():
    m = evaluate_answers.evaluate([], [{"id": "x1", "question": "r1"}], lambda q: (None, 1.0))
    assert m["correct_refusal_rate"] == 0.0
    assert m["ask_p95_ms"] is None
    res = _results()
    res["answers"]["ask_p95_ms"] = None
    assert not all(c["passed"] for c in eval_gate.check_thresholds(res, eval_gate.load_thresholds()))


def test_percentile_nearest_rank():
    assert evaluate_answers.percentile(list(range(1, 101)), 95) == 95
    assert evaluate_answers.percentile([], 95) is None
    assert json.dumps(evaluate_answers.percentile([3.0], 50)) == "3.0"

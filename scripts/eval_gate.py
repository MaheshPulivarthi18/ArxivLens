#!/usr/bin/env python3
"""
ArxivLens — eval quality gate (EVAL-1). Used by `make eval`.

Runs the retrieval eval (scripts/evaluate_retrieval.py, needs Postgres + indexed corpus) and the
answer/refusal eval (scripts/evaluate_answers.py, needs the query service), compares the results
with evals/thresholds.json (Product PRD §8), writes evals/results/<timestamp>.json, prints a table,
and exits 0 only if every check passes.

Exit codes: 0 = all checks pass, 1 = a check failed or was not measured, 2 = an eval could not run.
A partial run (--skip-*) never passes the gate.
"""

import argparse
import datetime as _dt
import json
import os
import sys
from typing import Any, Dict, List, Optional

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EVALS_DIR = os.path.join(REPO_ROOT, "evals")
GATED_MODE = "Hybrid RRF + Rerank"  # the configuration /v1/ask actually uses
BASELINE_MODES = ("BM25 Keyword", "Dense Vector")


def load_thresholds(path: str = os.path.join(EVALS_DIR, "thresholds.json")) -> Dict[str, Any]:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def _check(name: str, value: Optional[float], op: str, target: Any) -> Dict[str, Any]:
    if value is None:
        passed = False
    elif op == ">=":
        passed = value >= target
    elif op == "<":
        passed = value < target
    elif op == "==":
        passed = value == target
    else:
        raise ValueError(f"unknown op {op}")
    return {"metric": name, "value": value, "op": op, "target": target, "passed": passed}


def check_thresholds(results: Dict[str, Any], thresholds: Dict[str, Any]) -> List[Dict[str, Any]]:
    """Compare results {"retrieval": ..., "answers": ...} (either may be None) to thresholds."""
    checks: List[Dict[str, Any]] = []
    retrieval = results.get("retrieval")
    answers = results.get("answers")

    modes = (retrieval or {}).get("modes", {})
    gated = modes.get(GATED_MODE)
    checks.append(_check("retrieval_coverage", None if retrieval is None else
                         retrieval["cases_evaluated"] / max(1, retrieval["cases_total"]), "==", 1.0))
    checks.append(_check(f"recall@5 ({GATED_MODE})", None if gated is None else gated["recall@5"],
                         ">=", thresholds["retrieval_recall_at_5_min"]))
    if thresholds.get("hybrid_rerank_beats_baselines"):
        beats: Optional[float] = None
        if gated is not None and all(m in modes for m in BASELINE_MODES):
            beats = float(all(gated[k] > modes[b][k] for b in BASELINE_MODES for k in ("mrr", "ndcg@5")))
        checks.append(_check("hybrid+rerank beats BM25 & dense (MRR, NDCG@5)", beats, "==", 1.0))

    a = answers or {}
    checks.append(_check("supported_citation_rate", a.get("supported_citation_rate"),
                         ">=", thresholds["supported_citation_rate_min"]))
    checks.append(_check("correct_refusal_rate", a.get("correct_refusal_rate"),
                         ">=", thresholds["correct_refusal_rate_min"]))
    checks.append(_check("ask_p95_ms", a.get("ask_p95_ms"), "<", thresholds["ask_p95_latency_ms_max"]))
    # Failed/timed-out calls would otherwise only dilute the rates; any failure fails the gate.
    checks.append(_check("ask_errors (answer + refusal sets)", a.get("ask_error_count"), "==", 0))
    return checks


def print_table(checks: List[Dict[str, Any]], answers: Optional[Dict[str, Any]]) -> None:
    print("\n" + "=" * 78)
    print(f"{'Metric':<48} | {'Value':>9} | {'Target':>10} | Result")
    print("-" * 78)
    for c in checks:
        val = "n/a" if c["value"] is None else f"{c['value']:.4f}"
        result = "PASS" if c["passed"] else ("NOT RUN" if c["value"] is None else "FAIL")
        print(f"{c['metric']:<48} | {val:>9} | {c['op'] + ' ' + str(c['target']):>10} | {result}")
    if answers:
        print(f"{'(info) expected_paper_hit_rate':<48} | {answers['expected_paper_hit_rate']:>9.4f} |")
        p50 = answers["ask_p50_ms"]
        print(f"{'(info) ask_p50_ms':<48} | {'n/a' if p50 is None else f'{p50:.1f}':>9} |")
    print("=" * 78)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Run retrieval + answer evals and gate on evals/thresholds.json "
                                     "(Product PRD §8). Needs Postgres with evals/corpus.txt indexed and the "
                                     "query service running.")
    parser.add_argument("--api-url", default=os.getenv("ARXIVLENS_API_URL", "http://localhost:8080"))
    parser.add_argument("--thresholds", default=os.path.join(EVALS_DIR, "thresholds.json"))
    parser.add_argument("--results-dir", default=os.path.join(EVALS_DIR, "results"))
    parser.add_argument("--timeout", type=float, default=30.0, help="Per /v1/ask request timeout (s)")
    parser.add_argument("--skip-retrieval", action="store_true", help="Measure answers only (gate will fail)")
    parser.add_argument("--skip-answers", action="store_true", help="Measure retrieval only (gate will fail)")
    args = parser.parse_args(argv)

    if REPO_ROOT not in sys.path:
        sys.path.insert(0, REPO_ROOT)
    thresholds = load_thresholds(args.thresholds)
    results: Dict[str, Any] = {"retrieval": None, "answers": None}

    try:
        if not args.skip_retrieval:
            from scripts.evaluate_retrieval import run_retrieval_evaluation
            results["retrieval"] = run_retrieval_evaluation()
        if not args.skip_answers:
            from scripts import evaluate_answers as ea
            results["answers"] = ea.evaluate(ea.load_cases(os.path.join(EVALS_DIR, "answer.jsonl")),
                                             ea.load_cases(os.path.join(EVALS_DIR, "refusal.jsonl")),
                                             ea.make_http_ask(args.api_url, args.timeout))
            ea.print_report(results["answers"])
            if results["answers"]["requests_ok"] == 0:
                raise ConnectionError(f"every /v1/ask request to {args.api_url} failed")
    except Exception as e:  # infra down, missing deps, etc.
        print(f"eval could not run: {type(e).__name__}: {e}", file=sys.stderr)
        return 2

    checks = check_thresholds(results, thresholds)
    print_table(checks, results["answers"])

    os.makedirs(args.results_dir, exist_ok=True)
    stamp = _dt.datetime.now(_dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    out = os.path.join(args.results_dir, f"{stamp}.json")
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"timestamp": stamp, "thresholds": thresholds, "checks": checks, **results}, f, indent=2)
    print(f"results written to {out}")

    passed = all(c["passed"] for c in checks)
    print("GATE: PASS" if passed else "GATE: FAIL")
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())

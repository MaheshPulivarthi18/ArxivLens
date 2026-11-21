#!/usr/bin/env python3
"""
ArxivLens — eval quality gate (EVAL-1). Used by `make eval`.

Runs the retrieval eval (scripts/evaluate_retrieval.py, needs Postgres + indexed corpus) and the
answer/refusal eval (scripts/evaluate_answers.py, needs the query service), compares the results
with evals/thresholds.json (Product PRD §8, in arxivlens-agents), writes evals/results/<timestamp>.json, prints a table,
and exits 0 only if every check passes.

Exit codes: 0 = all checks pass, 1 = a check failed or was not measured, 2 = an eval could not run.
A partial run (--skip-*) never passes the gate.
"""

import argparse
import datetime as _dt
import json
import os
import sys
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EVALS_DIR = os.path.join(REPO_ROOT, "evals")
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)
from scripts import evaluate_retrieval as er  # noqa: E402  (stdlib-only at import time)

# ADR-0010 ordering: RRF pool of 20 (search.go SQL), reranked in one call to the production cross-encoder
# (embedder API /internal/rerank, the endpoint /v1/ask calls via EmbedClient.Rerank), stable-sorted by score,
# top 5. Production /v1/ask orders this way only after RET-1 merges; until then this gates the target behaviour.
# Recall@5 here is paper-level (ADR-0006). chunk_precision@5 is reported for information and never gated.
# ADRs: https://github.com/MaheshPulivarthi18/arxivlens-agents/tree/main/docs/adr
GATED_MODE = er.RERANK_MODE
BASELINE_MODES = ("BM25 Keyword", "Dense Vector")
# Printed by print_table, never read by check_thresholds (EVAL-6). Definitions: evals/README.md.
REPORT_ONLY_KEYS = ("ask_full_p95_ms", "citation_coverage", "near_miss_correct_refusal_rate", "verification_unavailable_count")


# Technical PRD §9 "Reference hardware" (Accepted 2026-11-15): latency targets are gated on the reference profile, a
# GPU machine (Apple silicon with PyTorch MPS, or CUDA), which is what self-hosting scholars run. On a CPU-only machine
# (e.g. a GitHub runner) latency is measured and reported but cannot pass or fail the gate. The profile comes from the
# device the embedder reports; when it cannot be determined the latency gate applies (fail closed).
GPU_PROFILE = "gpu"
CPU_PROFILE = "cpu-only"
LATENCY_GATES = ("ask_p95_ms",)


def hardware_profile(device: Optional[str]) -> Optional[str]:
    d = (device or "").strip().lower()
    if d.startswith(("mps", "cuda")):
        return GPU_PROFILE
    if d == "cpu":
        return CPU_PROFILE
    return None


def embedder_device(embedder_url: str, timeout_s: float = 10.0) -> Optional[str]:
    """The device the embedder API serves on (GET /healthz), or None if it cannot be read."""
    try:
        with urllib.request.urlopen(embedder_url.rstrip("/") + "/healthz", timeout=timeout_s) as r:
            device = json.loads(r.read().decode("utf-8")).get("device")
    except (urllib.error.URLError, TimeoutError, ValueError, OSError, AttributeError):
        return None
    return device if isinstance(device, str) else None


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
    """Compare results {"retrieval": ..., "answers": ..., "hardware": ...} (any may be None) to thresholds.

    A latency check measured on the CPU-only profile is marked informational: it keeps its value and target, is never
    failed, and is labelled with the profile. Every other check, and latency on a GPU or unknown machine, is gated.
    """
    checks: List[Dict[str, Any]] = []
    retrieval = results.get("retrieval")
    answers = results.get("answers")

    modes = (retrieval or {}).get("modes", {})
    # Only trust the gated mode's numbers if they came from the production rerank path.
    production_rerank = None if retrieval is None else \
        float(retrieval.get("rerank_path") == er.PRODUCTION_RERANK_PATH)
    checks.append(_check("rerank via production /internal/rerank", production_rerank, "==", 1.0))
    gated = modes.get(GATED_MODE) if production_rerank else None
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
    profile = (results.get("hardware") or {}).get("profile")
    for c in checks:
        if c["metric"] in LATENCY_GATES and profile == CPU_PROFILE and c["value"] is not None:
            c["informational"] = profile
            c["passed"] = True
    return checks


def print_table(checks: List[Dict[str, Any]], answers: Optional[Dict[str, Any]],
                retrieval: Optional[Dict[str, Any]] = None) -> None:
    print("\n" + "=" * 78)
    print(f"{'Metric':<48} | {'Value':>9} | {'Target':>10} | Result")
    print("-" * 78)
    for c in checks:
        val = "n/a" if c["value"] is None else f"{c['value']:.4f}"
        if c.get("informational"):
            result = f"INFO ({c['informational']}; not gated)"
        else:
            result = "PASS" if c["passed"] else ("NOT RUN" if c["value"] is None else "FAIL")
        print(f"{c['metric']:<48} | {val:>9} | {c['op'] + ' ' + str(c['target']):>10} | {result}")
    cp = ((retrieval or {}).get("modes", {}).get(GATED_MODE) or {}).get("chunk_precision@5")
    if cp is not None:
        print(f"{'(info, not gated) chunk_precision@5 (' + GATED_MODE + ')':<48} | {cp:>9.4f} |")
    if answers:
        print(f"{'(info) expected_paper_hit_rate':<48} | {answers['expected_paper_hit_rate']:>9.4f} |")
        p50 = answers["ask_p50_ms"]
        print(f"{'(info) ask_p50_ms':<48} | {'n/a' if p50 is None else f'{p50:.1f}':>9} |")
        print("-" * 78)
        print("report-only (EVAL-6; not gated, promoting one to a gate needs an ADR)")
        for key in REPORT_ONLY_KEYS:
            v = answers.get(key)
            val = "n/a" if v is None else (f"{v:.4f}" if isinstance(v, float) else str(v))
            print(f"{'(report-only) ' + key:<48} | {val:>9} |")
        print(f"{'(report-only) refusal_reason_counts':<48} | {json.dumps(answers.get('refusal_reason_counts', {}))}")
    print("=" * 78)


def could_not_run_hint(e: BaseException) -> Optional[str]:
    """A next step for the two setup mistakes that most often stop `make eval`, or None."""
    if isinstance(e, ModuleNotFoundError):
        return (f"missing Python module {e.name!r}: run with the project venv (make uses .venv/bin/python when it "
                "exists; otherwise activate it or pass PY=...), after `make setup-py`")
    if type(e).__name__ == "OperationalError":  # psycopg: server down, wrong port, wrong password
        from services.common.config import config
        url = config.postgres_url
        where = url.rsplit("@", 1)[-1]  # host:port/db only, never the password
        source = "DATABASE_URL" if os.getenv("DATABASE_URL") else "the default (DATABASE_URL unset)"
        return (f"could not use Postgres at {where} from {source}. Start it with `make infra-up`, or point "
                "DATABASE_URL at the ArxivLens database, e.g. "
                "DATABASE_URL=postgresql://postgres:postgres@localhost:5432/arxivlens make eval")
    return None


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Run retrieval + answer evals and gate on evals/thresholds.json "
                                     "(Product PRD §8). Needs Postgres with evals/corpus.txt indexed and the "
                                     "query service running.")
    parser.add_argument("--api-url", default=os.getenv("ARXIVLENS_API_URL", "http://localhost:8080"))
    parser.add_argument("--embedder-url", default=er.DEFAULT_EMBEDDER_URL,
                        help="Embedder API base URL for the production /internal/rerank "
                        "(env ARXIVLENS_EMBEDDER_URL, default http://localhost:8081)")
    parser.add_argument("--thresholds", default=os.path.join(EVALS_DIR, "thresholds.json"))
    parser.add_argument("--results-dir", default=os.path.join(EVALS_DIR, "results"))
    parser.add_argument("--timeout", type=float, default=30.0,
                        help="Per request timeout for /v1/ask and the embedder API (s)")
    parser.add_argument("--skip-retrieval", action="store_true", help="Measure answers only (gate will fail)")
    parser.add_argument("--skip-answers", action="store_true", help="Measure retrieval only (gate will fail)")
    args = parser.parse_args(argv)

    thresholds = load_thresholds(args.thresholds)
    device = embedder_device(args.embedder_url)
    results: Dict[str, Any] = {"retrieval": None, "answers": None,
                               "hardware": {"device": device, "profile": hardware_profile(device)}}
    print(f"hardware: embedder device={device or 'unknown'} profile={hardware_profile(device) or 'unknown (latency gated)'}")

    try:
        if not args.skip_retrieval:
            results["retrieval"] = er.run_retrieval_evaluation(embedder_url=args.embedder_url,
                                                               timeout_s=args.timeout)
        if not args.skip_answers:
            from scripts import evaluate_answers as ea
            results["answers"] = ea.evaluate(ea.load_cases(os.path.join(EVALS_DIR, "answer.jsonl")),
                                             ea.load_cases(os.path.join(EVALS_DIR, "refusal.jsonl")),
                                             ea.make_http_ask(args.api_url, args.timeout),
                                             ea.load_cases(os.path.join(EVALS_DIR, "refusal_near_miss.jsonl")),
                                             # ADR-0019: the 3 s latency gate times the quick first stage
                                             quick_ask=ea.make_http_ask(args.api_url, args.timeout, quick=True))
            ea.print_report(results["answers"])
            if results["answers"]["requests_ok"] == 0:
                raise ConnectionError(f"every /v1/ask request to {args.api_url} failed")
    except Exception as e:  # infra down, missing deps, etc.
        print(f"eval could not run: {type(e).__name__}: {e}", file=sys.stderr)
        hint = could_not_run_hint(e)
        if hint:
            print(f"hint: {hint}", file=sys.stderr)
        return 2

    checks = check_thresholds(results, thresholds)
    print_table(checks, results["answers"], results["retrieval"])

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

#!/usr/bin/env python3
"""
ArxivLens — Answer & refusal evaluation against POST /v1/ask (EVAL-1).

Reads evals/answer.jsonl (in-corpus) and evals/refusal.jsonl (out-of-corpus), calls the
query service, and reports:
  - supported_citation_rate  : supported citations / all citations across answer-set responses
  - expected_paper_hit_rate  : answers citing at least one expected arXiv ID
  - answer_refusal_rate      : in-corpus questions that were refused (informational)
  - correct_refusal_rate     : out-of-corpus questions refused (no citations returned)
  - ask_p50_ms / ask_p95_ms  : latency over successful /v1/ask calls (None if none succeeded)

Response shape (internal/query/rag.go AskResponse):
  {"answer": str, "citations": [{"marker", "arxiv_id", "supported", ...}], "trace_id": str}
A refusal is a response with zero citations (rag.go returns an empty list when it refuses).
A failed request never counts as a correct refusal or a supported citation.

Stdlib only; no infra needed for --help.
"""

import argparse
import json
import math
import os
import re
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, List, Optional, Tuple

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EVALS_DIR = os.path.join(REPO_ROOT, "evals")
DEFAULT_API = os.getenv("ARXIVLENS_API_URL", "http://localhost:8080")

_VERSION_SUFFIX = re.compile(r"v\d+$")

# ask_fn(question) -> (response dict or None on error, latency_ms)
AskFn = Callable[[str], Tuple[Optional[Dict[str, Any]], float]]


def load_cases(path: str) -> List[Dict[str, Any]]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def normalize_arxiv_id(arxiv_id: str) -> str:
    return _VERSION_SUFFIX.sub("", (arxiv_id or "").strip())


def percentile(values: List[float], pct: float) -> Optional[float]:
    """Nearest-rank percentile; None for an empty list (nothing measured)."""
    if not values:
        return None
    ordered = sorted(values)
    rank = max(1, math.ceil(pct / 100.0 * len(ordered)))
    return ordered[rank - 1]


def is_refusal(resp: Optional[Dict[str, Any]]) -> bool:
    return resp is not None and not (resp.get("citations") or [])


def make_http_ask(api_url: str, timeout_s: float, max_citations: int = 5) -> AskFn:
    url = api_url.rstrip("/") + "/v1/ask"

    def ask(question: str) -> Tuple[Optional[Dict[str, Any]], float]:
        body = json.dumps({"question": question, "max_citations": max_citations}).encode("utf-8")
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as r:
                data = json.loads(r.read().decode("utf-8"))
        except (urllib.error.URLError, TimeoutError, ValueError, OSError):
            return None, (time.perf_counter() - t0) * 1000
        return data, (time.perf_counter() - t0) * 1000

    return ask


def evaluate(answer_cases: List[Dict[str, Any]], refusal_cases: List[Dict[str, Any]], ask: AskFn) -> Dict[str, Any]:
    latencies: List[float] = []
    errors: List[str] = []
    total_citations = supported_citations = hits = refused_answers = 0
    misses: List[str] = []

    for case in answer_cases:
        resp, ms = ask(case["question"])
        if resp is None:
            errors.append(case["id"])
            misses.append(case["id"])
            continue
        latencies.append(ms)
        citations = resp.get("citations") or []
        if not citations:
            refused_answers += 1
        total_citations += len(citations)
        supported_citations += sum(1 for c in citations if c.get("supported") is True)
        expected = {normalize_arxiv_id(a) for a in case["expected_arxiv_ids"]}
        if any(normalize_arxiv_id(c.get("arxiv_id", "")) in expected for c in citations):
            hits += 1
        else:
            misses.append(case["id"])

    correct_refusals = 0
    false_answers: List[str] = []
    for case in refusal_cases:
        resp, ms = ask(case["question"])
        if resp is None:
            errors.append(case["id"])
            false_answers.append(case["id"])
            continue
        latencies.append(ms)
        if is_refusal(resp):
            correct_refusals += 1
        else:
            false_answers.append(case["id"])

    n_ans, n_ref = len(answer_cases), len(refusal_cases)
    return {
        "supported_citation_rate": supported_citations / total_citations if total_citations else 0.0,
        "expected_paper_hit_rate": hits / n_ans if n_ans else 0.0,
        "answer_refusal_rate": refused_answers / n_ans if n_ans else 0.0,
        "correct_refusal_rate": correct_refusals / n_ref if n_ref else 0.0,
        "ask_p50_ms": percentile(latencies, 50),
        "ask_p95_ms": percentile(latencies, 95),
        "citations_total": total_citations,
        "answer_cases": n_ans,
        "refusal_cases": n_ref,
        "requests_ok": len(latencies),
        "error_case_ids": errors,
        "expected_paper_miss_ids": misses,
        "refusal_failure_ids": false_answers,
    }


def print_report(m: Dict[str, Any]) -> None:
    print("=" * 60)
    print("ArxivLens — /v1/ask answer + refusal eval")
    print("=" * 60)
    print(f"Supported citations      : {m['supported_citation_rate']:.2%} ({m['citations_total']} citations)")
    print(f"Answers citing expected  : {m['expected_paper_hit_rate']:.2%} of {m['answer_cases']}")
    print(f"In-corpus refused        : {m['answer_refusal_rate']:.2%}")
    print(f"Correct refusals         : {m['correct_refusal_rate']:.2%} of {m['refusal_cases']}")
    if m["ask_p95_ms"] is not None:
        print(f"Latency p50 / p95 (ms)   : {m['ask_p50_ms']:.0f} / {m['ask_p95_ms']:.0f}")
    if m["error_case_ids"]:
        print(f"WARNING: request errors  : {', '.join(m['error_case_ids'])}")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Evaluate /v1/ask on evals/answer.jsonl and evals/refusal.jsonl. "
                                     "Needs the query service running.")
    parser.add_argument("--api-url", default=DEFAULT_API, help="Query service base URL (env ARXIVLENS_API_URL)")
    parser.add_argument("--answers", default=os.path.join(EVALS_DIR, "answer.jsonl"))
    parser.add_argument("--refusals", default=os.path.join(EVALS_DIR, "refusal.jsonl"))
    parser.add_argument("--timeout", type=float, default=30.0, help="Per-request timeout in seconds")
    parser.add_argument("--json-out", help="Also write metrics as JSON to this path")
    args = parser.parse_args(argv)

    metrics = evaluate(load_cases(args.answers), load_cases(args.refusals), make_http_ask(args.api_url, args.timeout))
    print_report(metrics)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(metrics, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

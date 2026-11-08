#!/usr/bin/env python3
"""
ArxivLens — Answer & refusal evaluation against POST /v1/ask (EVAL-1).

Reads evals/answer.jsonl (in-corpus), evals/refusal.jsonl (out-of-corpus) and, report-only,
evals/refusal_near_miss.jsonl (in-domain but unanswered by the corpus), calls the query service, and reports:
  - supported_citation_rate  : supported / all citations of *answered* answer-set responses (EVAL-6: a
                               refusal's evidence passages are not answer citations)
  - expected_paper_hit_rate  : answer-set questions answered with a citation of an expected arXiv ID
  - answer_refusal_rate      : in-corpus questions that were refused (informational)
  - correct_refusal_rate     : out-of-corpus questions refused (see classify())
  - ask_p50_ms / ask_p95_ms  : latency over every answer + refusal call, failed ones included at their elapsed
                               time (timeouts count as >= the timeout); None only if nothing was called
  - ask_error_count          : failed calls (HTTP error, timeout, bad JSON) plus ADR-0003 case B
                               (verification_unavailable) and unknown refusal_reason values; the gate requires 0
  Report-only (EVAL-6, never gated): citation_coverage, refusal_reason_counts,
  verification_unavailable_count, near_miss_correct_refusal_rate. Definitions: evals/README.md.
  - cases                    : per-case records (IDs and numbers only, never passage or answer text)

Response shape (internal/query/rag.go AskResponse):
  {"answer": str, "citations": [{"marker", "arxiv_id", "chunk_id", "supported", "score", ...}],
   "refusal_reason"?: str (ADR-0003), "claims"?: [{"text", "status", "markers": [n]}] (ADR-0014), "trace_id": str}
A failed request never counts as a correct refusal, a hit, or a supported citation.

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

# ask_fn(question) -> (response dict or None on error, latency_ms incl. failed calls)
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


# ADR-0003 refusal_reason values. Case A is a refusal that still carries its evidence passages; case B
# (verification could not run) is an error, never a correct refusal. Any other value fails closed as an error.
REFUSAL_REASONS = {"all_citations_unsupported": "refusal", "verification_unavailable": "error"}
VERIFICATION_UNAVAILABLE = "verification_unavailable"


def classify(resp: Optional[Dict[str, Any]]) -> str:
    """'error' | 'refusal' | 'answered' for one /v1/ask response (None = failed request)."""
    if resp is None:
        return "error"
    reason = resp.get("refusal_reason")
    if reason:
        return REFUSAL_REASONS.get(reason, "error")
    return "answered" if resp.get("citations") else "refusal"  # no citations = early refusal (QA-5)


def is_refusal(resp: Optional[Dict[str, Any]]) -> bool:
    return classify(resp) == "refusal"


# Mirrors splitIntoSentences/sentenceAbbreviations in internal/query/rag.go (a test guards the list).
SENTENCE_ABBREVIATIONS = ("e.g.", "i.e.", "et al.", "cf.", "vs.", "fig.", "eq.")
MARKER = re.compile(r"\[(\d+)\]")


def _ends_with_abbreviation(s: str) -> bool:
    lower = s.lower()
    for abbr in SENTENCE_ABBREVIATIONS:
        if lower.endswith(abbr):
            rest = lower[:-len(abbr)]
            if rest == "" or rest.endswith(" ") or rest.endswith("("):
                return True
    return False


def split_sentences(text: str) -> List[str]:
    """Split like rag.go: end at '.', '!' or '?' followed by a space or the end, unless an abbreviation."""
    sentences: List[str] = []
    cur = ""
    i = 0
    while i < len(text):
        cur += text[i]
        if text[i] in ".!?" and (i + 1 == len(text) or text[i + 1] == " ") and not _ends_with_abbreviation(cur):
            if cur.strip():
                sentences.append(cur.strip())
            cur = ""
            if i + 1 < len(text):
                i += 1  # skip the space
        i += 1
    if cur.strip():
        sentences.append(cur.strip())
    return sentences


def coverage_units(resp: Dict[str, Any]) -> Tuple[int, int]:
    """(cited, total) units of an answered response: claims[] items when present, else answer sentences.
    A unit is cited if it has >= 1 in-range marker, i.e. a marker that matches a returned citation (the
    server drops markers outside 1..K, so only in-range markers get a citation)."""
    in_range = {c.get("marker") for c in resp.get("citations") or []}
    claims = resp.get("claims")
    if isinstance(claims, list) and claims:
        marks = [set(c.get("markers") or []) if isinstance(c, dict) else set() for c in claims]
    else:
        marks = [{int(m) for m in MARKER.findall(s)} for s in split_sentences(resp.get("answer") or "")]
    return sum(1 for m in marks if m & in_range), len(marks)


def case_record(case_id: str, case_set: str, resp: Optional[Dict[str, Any]], ms: float) -> Dict[str, Any]:
    """Per-case record for EVAL-7's review export: IDs and numbers only, no passage or answer text."""
    outcome = classify(resp)
    r = resp or {}
    rec: Dict[str, Any] = {
        "id": case_id, "set": case_set, "outcome": outcome, "refused": outcome == "refusal",
        "refusal_reason": r.get("refusal_reason") or None, "latency_ms": round(ms, 1),
        "citations": [{k: c.get(k) for k in ("arxiv_id", "chunk_id", "marker", "supported", "score")}
                      for c in r.get("citations") or []],
    }
    if outcome == "answered":
        rec["cited_units"], rec["total_units"] = coverage_units(r)
    return rec


def make_http_ask(api_url: str, timeout_s: float, max_citations: int = 5) -> AskFn:
    url = api_url.rstrip("/") + "/v1/ask"

    def ask(question: str) -> Tuple[Optional[Dict[str, Any]], float]:
        body = json.dumps({"question": question, "max_citations": max_citations}).encode("utf-8")
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as r:
                data = json.loads(r.read().decode("utf-8"))
        except (urllib.error.URLError, TimeoutError, ValueError, OSError) as e:
            ms = (time.perf_counter() - t0) * 1000
            reason = getattr(e, "reason", e)
            if isinstance(e, TimeoutError) or isinstance(reason, TimeoutError):
                ms = max(ms, timeout_s * 1000)  # a timeout costs at least the full timeout
            return None, ms
        return data, (time.perf_counter() - t0) * 1000

    return ask


def evaluate(answer_cases: List[Dict[str, Any]], refusal_cases: List[Dict[str, Any]], ask: AskFn,
             near_miss_cases: Optional[List[Dict[str, Any]]] = None) -> Dict[str, Any]:
    latencies: List[float] = []  # every answer + refusal call, failed ones included
    errors: List[str] = []
    ok = verification_unavailable = 0
    total_citations = supported_citations = hits = refused_answers = 0
    cited_units = total_units = 0
    misses: List[str] = []
    reason_counts: Dict[str, int] = {}
    records: List[Dict[str, Any]] = []

    def call(case: Dict[str, Any], case_set: str) -> Tuple[Optional[Dict[str, Any]], str, float]:
        resp, ms = ask(case["question"])
        reason = (resp or {}).get("refusal_reason")
        if reason:
            reason_counts[reason] = reason_counts.get(reason, 0) + 1
        records.append(case_record(case["id"], case_set, resp, ms))
        return resp, classify(resp), ms

    def gated_call(case: Dict[str, Any], case_set: str) -> Tuple[Optional[Dict[str, Any]], str]:
        nonlocal ok, verification_unavailable
        resp, outcome, ms = call(case, case_set)
        latencies.append(ms)
        if resp is not None:
            ok += 1
            if resp.get("refusal_reason") == VERIFICATION_UNAVAILABLE:
                verification_unavailable += 1
        if outcome == "error":
            errors.append(case["id"])
        return resp, outcome

    for case in answer_cases:
        resp, outcome = gated_call(case, "answer")
        if outcome != "answered":
            refused_answers += outcome == "refusal"
            misses.append(case["id"])
            continue
        citations = resp.get("citations") or []
        total_citations += len(citations)
        supported_citations += sum(1 for c in citations if c.get("supported") is True)
        cited, units = coverage_units(resp)
        cited_units += cited
        total_units += units
        expected = {normalize_arxiv_id(a) for a in case["expected_arxiv_ids"]}
        if any(normalize_arxiv_id(c.get("arxiv_id", "")) in expected for c in citations):
            hits += 1
        else:
            misses.append(case["id"])

    correct_refusals = 0
    false_answers: List[str] = []
    for case in refusal_cases:
        _, outcome = gated_call(case, "refusal")
        if outcome == "refusal":
            correct_refusals += 1
        else:
            false_answers.append(case["id"])

    # Report-only: near-miss calls stay out of the gated latency and error counts, so unreviewed rows
    # can never change a gate. Errors still count as not refused.
    near_miss_cases = near_miss_cases or []
    near_miss_refused = 0
    near_miss_errors: List[str] = []
    near_miss_failures: List[str] = []
    for case in near_miss_cases:
        _, outcome, _ = call(case, "near_miss")
        if outcome == "refusal":
            near_miss_refused += 1
        else:
            near_miss_failures.append(case["id"])
            if outcome == "error":
                near_miss_errors.append(case["id"])

    n_ans, n_ref, n_nm = len(answer_cases), len(refusal_cases), len(near_miss_cases)
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
        "requests_ok": ok,
        "ask_error_count": len(errors),
        "verification_unavailable_count": verification_unavailable,
        "error_case_ids": errors,
        "expected_paper_miss_ids": misses,
        "refusal_failure_ids": false_answers,
        # report-only (EVAL-6)
        "citation_coverage": cited_units / total_units if total_units else None,
        "refusal_reason_counts": reason_counts,
        "near_miss_cases": n_nm,
        "near_miss_correct_refusal_rate": near_miss_refused / n_nm if n_nm else None,
        "near_miss_error_ids": near_miss_errors,
        "near_miss_failure_ids": near_miss_failures,
        "cases": records,
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
        print(f"WARNING: errors          : {', '.join(m['error_case_ids'])} "
              f"({m['verification_unavailable_count']} verification_unavailable)")
    print("-- report-only (not gated) --")
    cov, nm = m["citation_coverage"], m["near_miss_correct_refusal_rate"]
    print(f"Citation coverage        : {'n/a' if cov is None else f'{cov:.2%}'}")
    print(f"Near-miss refusals       : {'n/a' if nm is None else f'{nm:.2%}'} of {m['near_miss_cases']}")
    print(f"refusal_reason counts    : {m['refusal_reason_counts'] or '{}'}")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Evaluate /v1/ask on evals/answer.jsonl and evals/refusal.jsonl. "
                                     "Needs the query service running.")
    parser.add_argument("--api-url", default=DEFAULT_API, help="Query service base URL (env ARXIVLENS_API_URL)")
    parser.add_argument("--answers", default=os.path.join(EVALS_DIR, "answer.jsonl"))
    parser.add_argument("--refusals", default=os.path.join(EVALS_DIR, "refusal.jsonl"))
    parser.add_argument("--near-miss", default=os.path.join(EVALS_DIR, "refusal_near_miss.jsonl"),
                        help="Report-only near-miss refusal set ('' to skip)")
    parser.add_argument("--timeout", type=float, default=30.0, help="Per-request timeout in seconds")
    parser.add_argument("--json-out", help="Also write metrics as JSON to this path")
    args = parser.parse_args(argv)

    metrics = evaluate(load_cases(args.answers), load_cases(args.refusals), make_http_ask(args.api_url, args.timeout),
                       load_cases(args.near_miss) if args.near_miss else None)
    print_report(metrics)
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(metrics, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

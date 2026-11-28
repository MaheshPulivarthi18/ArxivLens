#!/usr/bin/env python3
"""
ArxivLens — /v1/search latency (B-QUAL, EVAL-2). Run by scripts/eval_gate.py; report-only.

Times POST /v1/search with rerank (the production path, ADR-0010) once per evals/retrieval.jsonl query, after one
warm-up call that is not counted. Product PRD §8 targets p95 < 1500 ms, but ADR-0012 is Proposed, so nothing here is
gated: there is no threshold key and no check. Percentiles cover successful calls only; failures are counted.
Stdlib only.
"""

import http.client
import json
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, List, Optional, Tuple

from scripts.evaluate_answers import percentile

SEARCH_TARGET_MS = 1500  # Product PRD §8; printed only (ADR-0012 Proposed)
SEARCH_KEYS = ("search_p50_ms", "search_p95_ms", "search_count", "search_error_count")
SearchFn = Callable[[str], Tuple[bool, float]]


def make_http_search(api_url: str, timeout_s: float, limit: int = 5) -> SearchFn:
    """search(query) -> (ok, ms). A transport error, non-2xx, broken response or bad JSON is not ok, at its elapsed time."""
    url = api_url.rstrip("/") + "/v1/search"

    def search(query: str) -> Tuple[bool, float]:
        body = json.dumps({"query": query, "limit": limit, "rerank": True}).encode("utf-8")
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as r:
                json.loads(r.read().decode("utf-8"))
        except (urllib.error.URLError, http.client.HTTPException, TimeoutError, ValueError, OSError):
            return False, (time.perf_counter() - t0) * 1000
        return True, (time.perf_counter() - t0) * 1000

    return search


def measure(queries: List[str], search: SearchFn) -> Dict[str, Any]:
    """One uncounted warm-up on queries[0], then one timed call per query."""
    if queries:
        search(queries[0])
    ok_ms: List[float] = []
    errors = 0
    for q in queries:
        ok, ms = search(q)
        if ok:
            ok_ms.append(ms)
        else:
            errors += 1
    return {"search_p50_ms": percentile(ok_ms, 50), "search_p95_ms": percentile(ok_ms, 95),
            "search_count": len(queries), "search_error_count": errors}


def failed(count: int) -> Dict[str, Any]:
    """The block when measuring itself raised: every call counts as an error."""
    return {"search_p50_ms": None, "search_p95_ms": None, "search_count": count, "search_error_count": count}


def summary_line(m: Dict[str, Any], profile: Optional[str]) -> str:
    def fmt(v: Optional[float]) -> str:
        return "n/a" if v is None else f"{v:.0f}"
    return (f"/v1/search p50/p95 (ms): {fmt(m['search_p50_ms'])} / {fmt(m['search_p95_ms'])} "
            f"(report-only, ADR-0012 Proposed; target {SEARCH_TARGET_MS}) "
            f"errors={m['search_error_count']}/{m['search_count']} profile={profile or 'unknown'}")

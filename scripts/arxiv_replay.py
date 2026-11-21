#!/usr/bin/env python3
"""
Record/replay proxy for the arXiv API and PDFs, for CI.

GitHub-hosted runners share IP addresses, and arXiv rate-limits them (HTTP 429, timeouts, empty bodies), so
jobs that called live arXiv failed for reasons unrelated to the code. CI points the Go services at this proxy
(ARXIV_API_URL=http://127.0.0.1:<port>/api/query, ARXIV_PDF_BASE=http://127.0.0.1:<port>/pdf) and keeps the
--dir directory in the Actions cache:

  - hit:  the recorded response is served; arXiv is not contacted.
  - miss with --record: fetched once from arXiv (serialized, >= 3 s apart, identifying User-Agent, retried on
    429/5xx/timeouts honouring Retry-After), saved only if valid (an Atom feed, or a body starting %PDF),
    then served. A failed upstream fetch is answered with arXiv's status, so the client's own retry applies.
  - miss without --record: 404 "not recorded".

After one successful recording run, CI makes zero arXiv requests and sees the same papers every run.

The proxy downloads a miss completely before answering, and arXiv can take minutes per PDF when it is
throttling, longer than a client's 60 s stall timeout. So CI warms the store first with `prefetch`, which uses
the same query strings as the Go code (crawler: search_query/start/max_results/sortBy/sortOrder; ingester:
id_list), and the services then only see hits.

Prefetch time budget (--budget-seconds N > 0; the clock starts at the first upstream request): when a pass over the
queries ends with failures, prefetch waits a cool-down (PASS_WAIT, or longer if arXiv sent a longer Retry-After) and
runs another pass over only the queries that had a failure (what they already saved is a hit), until nothing fails
(exit 0) or the budget is spent (exit 1, naming what is still missing). No upstream request starts after the
deadline and no retry, politeness or cool-down sleep runs past it: a wait that would reach it is not taken, and the
fetch it was for is reported as "(time budget)". So the worst case is the budget plus one in-flight request
(TIMEOUT), plus the seconds spent reading cached hits before the first request: 1020 + 120 = 1140 s < 1200 s (the
20-min step timeout in stack.yml) and 2520 + 120 = 2640 s < 2700 s (45 min, eval.yml). With no budget (0), prefetch
makes exactly one pass.

Usage:
  python3 scripts/arxiv_replay.py serve    --dir .arxiv-replay [--port 8099] [--record]
  python3 scripts/arxiv_replay.py prefetch --dir .arxiv-replay --ids evals/corpus.txt
  python3 scripts/arxiv_replay.py prefetch --dir .arxiv-replay --search "cat:cs.LG" --limit 2 --id 2608.20331 \
      --budget-seconds 1020
GET /stats returns {"hits", "misses", "upstream_fetches", "upstream_failures", "saved"}.
"""

import argparse
import hashlib
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

API_UPSTREAM = "https://export.arxiv.org/api/query"
PDF_UPSTREAM = "https://arxiv.org/pdf/"
USER_AGENT = "ArxivLens/1.0 (CI record proxy; mailto:contact@arxivlens.local)"
INTERVAL = 3.0             # arXiv politeness: one request per 3 s
RETRY_WAITS = (5, 15, 45)  # seconds before retries 1..3; at least Retry-After, at most MAX_WAIT
MAX_WAIT = 60
TIMEOUT = 120
PASS_WAIT = 60             # prefetch with a budget: cool-down before re-running the queries that failed
BUDGET = 0                 # fetch status: not attempted, the prefetch time budget is spent


def api_key(query: str) -> str:
    """Stable file key for an API query string (parameter order does not matter)."""
    canon = urllib.parse.urlencode(sorted(urllib.parse.parse_qsl(query, keep_blank_values=True)))
    return hashlib.sha256(canon.encode()).hexdigest()[:32]


def valid_pdf_name(name: str) -> bool:
    return bool(name) and "/" not in name and ".." not in name and len(name) < 64


def is_feed(body: bytes) -> bool:
    return b"<feed" in body[:2048]


def is_pdf(body: bytes) -> bool:
    return body[:5] == b"%PDF-"


class Recorder:
    """Serialized, rate-limited upstream fetches with retry, plus the on-disk store."""

    def __init__(self, root: str, record: bool, budget: float = 0):
        self.root, self.record = root, record
        self.budget = budget   # seconds (0: no limit); the deadline is set at the first upstream fetch
        self.deadline = None
        self.retry_after = 0   # largest Retry-After seen since the caller last reset it
        os.makedirs(os.path.join(root, "api"), exist_ok=True)
        os.makedirs(os.path.join(root, "pdf"), exist_ok=True)
        self.lock = threading.Lock()  # one upstream request at a time
        self.last = 0.0
        self.stats = {"hits": 0, "misses": 0, "upstream_fetches": 0, "upstream_failures": 0, "saved": 0}

    def path(self, kind: str, key: str) -> str:
        return os.path.join(self.root, kind, key)

    def load(self, kind: str, key: str):
        p = self.path(kind, key)
        if os.path.exists(p):
            with open(p, "rb") as f:
                return f.read()
        return None

    def save(self, kind: str, key: str, body: bytes, meta: dict) -> None:
        p = self.path(kind, key)
        tmp = p + ".tmp"
        with open(tmp, "wb") as f:
            f.write(body)
        os.replace(tmp, p)  # never leave a partial file under the real name
        with open(p + ".json", "w") as f:
            json.dump(meta, f)
        self.stats["saved"] += 1

    def budget_allows(self, wait: float) -> bool:
        """False if waiting `wait` seconds from now would reach the deadline (never, without a budget)."""
        return self.deadline is None or time.monotonic() + wait < self.deadline

    def fetch(self, url: str, valid) -> tuple:
        """(status, body). Retries 429/5xx/network errors; returns the last failure status (or 502), or BUDGET
        if the time budget ran out before the next attempt could start."""
        status, body = 502, b""
        for attempt in range(len(RETRY_WAITS) + 1):
            with self.lock:
                now = time.monotonic()
                if self.budget > 0 and self.deadline is None:
                    self.deadline = now + self.budget
                gap = INTERVAL - (now - self.last)
                if gap > 0:
                    if not self.budget_allows(gap):
                        return BUDGET, b""
                    time.sleep(gap)
                    now = time.monotonic()
                if self.deadline is not None and now >= self.deadline:
                    return BUDGET, b""
                self.last = now
                self.stats["upstream_fetches"] += 1
                retry_after = 0
                try:
                    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
                    with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
                        status, body = resp.status, resp.read()
                except urllib.error.HTTPError as e:
                    status, body = e.code, b""
                    try:
                        retry_after = int(e.headers.get("Retry-After", "0"))
                    except ValueError:
                        retry_after = 0
                    self.retry_after = max(self.retry_after, retry_after)
                except Exception as e:  # timeouts, resets, DNS
                    status, body = 504, b""
                    print(f"[replay] upstream error {type(e).__name__} for {url}", file=sys.stderr, flush=True)
            if status == 200 and valid(body):
                return status, body
            if status == 200:
                status = 502  # a 200 that is not a feed/PDF (rate-limit page, empty body) is not recorded
            self.stats["upstream_failures"] += 1
            print(f"[replay] upstream {status} for {url} (attempt {attempt + 1})", file=sys.stderr, flush=True)
            if (status != 429 and status < 500) or attempt == len(RETRY_WAITS):
                break
            wait = min(max(RETRY_WAITS[attempt], retry_after), MAX_WAIT)
            if not self.budget_allows(wait):
                return BUDGET, b""
            time.sleep(wait)
        return status, b""


class Handler(BaseHTTPRequestHandler):
    rec: Recorder = None  # set in main

    def log_message(self, fmt, *args):  # one compact line per request
        print(f"[replay] {self.command} {self.path} -> {args[1] if len(args) > 1 else ''}", flush=True)

    def send(self, status: int, body: bytes, ctype: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def serve(self, kind, key, upstream_url, valid, ctype, meta):
        body = self.rec.load(kind, key)
        if body is not None:
            self.rec.stats["hits"] += 1
            return self.send(200, body, ctype)
        self.rec.stats["misses"] += 1
        if not self.rec.record:
            return self.send(404, b"not recorded (start the proxy with --record)\n", "text/plain")
        status, body = self.rec.fetch(upstream_url, valid)
        if status != 200:
            return self.send(status, b"", "text/plain")
        self.rec.save(kind, key, body, meta)
        self.send(200, body, ctype)

    def do_GET(self):
        u = urllib.parse.urlsplit(self.path)
        if u.path == "/healthz":
            return self.send(200, b"ok\n", "text/plain")
        if u.path == "/stats":
            return self.send(200, json.dumps(self.rec.stats).encode(), "application/json")
        if u.path == "/api/query":
            return self.serve("api", api_key(u.query), f"{API_UPSTREAM}?{u.query}", is_feed,
                              "application/atom+xml", {"query": u.query})
        if u.path.startswith("/pdf/"):
            name = u.path[len("/pdf/"):]
            if not valid_pdf_name(name):
                return self.send(400, b"bad pdf name\n", "text/plain")
            return self.serve("pdf", name, PDF_UPSTREAM + name, is_pdf, "application/pdf", {"name": name})
        self.send(404, b"unknown path\n", "text/plain")


ATOM = "{http://www.w3.org/2005/Atom}"


def entry_pdf_names(feed: bytes) -> list:
    """PDF file names the Go code fetches for a feed's entries: "<id>v<n>.pdf", or "<id>.pdf" when unversioned."""
    names = []
    for e in ET.fromstring(feed).iter(ATOM + "entry"):
        eid = (e.findtext(ATOM + "id") or "").strip()
        eid = eid.split("/abs/")[-1]
        if eid:
            names.append(eid + ".pdf")
    return names


def get_or_fetch(rec: Recorder, kind: str, key: str, url: str, valid, meta: dict) -> tuple:
    """(body, 200) from the store or upstream (saved), or (None, failure status)."""
    body = rec.load(kind, key)
    if body is not None:
        rec.stats["hits"] += 1
        return body, 200
    rec.stats["misses"] += 1
    status, body = rec.fetch(url, valid)
    if status != 200:
        return None, status
    rec.save(kind, key, body, meta)
    return body, 200


def missing(name: str, status: int) -> str:
    return name + (" (time budget)" if status == BUDGET else "")


def prefetch_query(rec: Recorder, query: str) -> list:
    """Fetch an API query and every PDF its entries reference. Returns the names that could not be fetched."""
    feed, status = get_or_fetch(rec, "api", api_key(query), f"{API_UPSTREAM}?{query}", is_feed, {"query": query})
    if feed is None:
        return [missing(f"api?{query}", status)]
    failed = []
    for name in entry_pdf_names(feed):
        body, status = get_or_fetch(rec, "pdf", name, PDF_UPSTREAM + name, is_pdf, {"name": name})
        if body is None:
            failed.append(missing(name, status))
    return failed


def corpus_ids(path: str) -> list:
    ids = []
    for line in open(path):
        tok = line.split("#")[0].strip()
        if tok:
            ids.append(tok)
    return ids


def search_query_string(query: str, limit: int) -> str:
    """The crawler's first-page query string (internal/crawler: url.Values.Encode, keys sorted)."""
    return urllib.parse.urlencode(sorted({"search_query": query, "start": "0", "max_results": str(limit),
                                          "sortBy": "submittedDate", "sortOrder": "descending"}.items()))


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    sv = sub.add_parser("serve")
    sv.add_argument("--dir", required=True)
    sv.add_argument("--port", type=int, default=8099)
    sv.add_argument("--record", action="store_true", help="fetch and save misses from arXiv")
    pf = sub.add_parser("prefetch")
    pf.add_argument("--dir", required=True)
    pf.add_argument("--ids", help="file of arXiv IDs (one per line, # comments), fetched as id_list=<id>")
    pf.add_argument("--id", action="append", default=[], help="one arXiv ID (repeatable), fetched as id_list=<id>")
    pf.add_argument("--search", help="crawler search query; fetched as the crawler's first page")
    pf.add_argument("--limit", type=int, default=2)
    pf.add_argument("--budget-seconds", type=float, default=0,
                    help="time budget from the first upstream request: failed queries are retried in further "
                         "passes until it is spent (0: no limit, one pass); progress so far is kept")
    args = ap.parse_args(argv)

    if args.cmd == "serve":
        Handler.rec = Recorder(args.dir, args.record)
        srv = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
        print(f"[replay] serving {args.dir} on 127.0.0.1:{args.port} record={args.record}", flush=True)
        srv.serve_forever()
        return 0

    rec = Recorder(args.dir, True, max(args.budget_seconds, 0))
    ids = (corpus_ids(args.ids) if args.ids else []) + args.id
    queries = [f"id_list={i}" for i in dict.fromkeys(ids)]
    if args.search:
        queries.append(search_query_string(args.search, args.limit))
    pending, passes = queries, 0
    while True:  # one pass without a budget; with one, re-run the failed queries until none fail or time is up
        passes += 1
        failed, retry = [], []
        rec.retry_after = 0
        for i, q in enumerate(pending):
            if not rec.budget_allows(0):
                failed += [f"api?{x} (time budget)" for x in pending[i:]]
                break
            f = prefetch_query(rec, q)
            if f:
                failed += f
                retry.append(q)
        if not failed or rec.deadline is None:
            break
        wait = max(PASS_WAIT, rec.retry_after)
        if not rec.budget_allows(wait):  # the next pass could not start before the deadline
            break
        print(f"[replay] prefetch pass {passes}: {len(failed)} failed; retrying {len(retry)} queries in {wait:.0f}s",
              file=sys.stderr, flush=True)
        time.sleep(wait)
        pending = retry
    print(f"[replay] prefetch: {len(queries)} queries, stats={json.dumps(rec.stats)}", flush=True)
    # CI saves the store whenever this run added to it, even if incomplete, so recording converges across runs.
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as f:
            f.write(f"saved={rec.stats['saved']}\n")
    if failed:
        print(f"[replay] prefetch FAILED for {len(failed)}: {' '.join(failed)} "
              "(arXiv unreachable or rate-limiting, and not in the cache)", file=sys.stderr, flush=True)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

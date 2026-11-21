"""scripts/arxiv_replay.py against a fake upstream (no network)."""
import json
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from scripts import arxiv_replay as rp

FEED = b'<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><id>x</id></entry></feed>'
PDF = b"%PDF-1.5 fake"


class Upstream:
    """Fake arXiv: scripted (status, body) per path prefix; records requests."""

    def __init__(self):
        self.script = {}  # exact path, else "api" | "pdf" -> list of (status, body, headers); last one repeats
        self.requests = []
        self.on_request = None  # optional hook, called with the path before answering
        up = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                kind = "api" if self.path.startswith("/api") else "pdf"
                up.requests.append((self.path, self.headers.get("User-Agent")))
                if up.on_request:
                    up.on_request(self.path)
                seq = up.script.get(self.path) or up.script[kind]
                status, body, headers = seq.pop(0) if len(seq) > 1 else seq[0]
                self.send_response(status)
                for k, v in headers.items():
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.srv.server_address[1]}"


@pytest.fixture
def env(tmp_path, monkeypatch):
    up = Upstream()
    monkeypatch.setattr(rp, "API_UPSTREAM", up.base + "/api/query")
    monkeypatch.setattr(rp, "PDF_UPSTREAM", up.base + "/pdf/")
    monkeypatch.setattr(rp, "INTERVAL", 0.0)
    monkeypatch.setattr(rp, "RETRY_WAITS", (0.01, 0.01, 0.01))
    # Guard: a prefetch that loops (e.g. extra passes without a budget) fails the test instead of hanging it.
    real_sleep, sleeps = rp.time.sleep, []

    def bounded_sleep(s):
        sleeps.append(s)
        if len(sleeps) > 100:
            raise RuntimeError("runaway prefetch loop (test guard)")
        real_sleep(min(s, 0.05))

    monkeypatch.setattr(rp.time, "sleep", bounded_sleep)

    def start(record):
        rp.Handler.rec = rp.Recorder(str(tmp_path), record)
        srv = ThreadingHTTPServer(("127.0.0.1", 0), rp.Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        return f"http://127.0.0.1:{srv.server_address[1]}"

    yield up, start
    up.srv.shutdown()


def get(url):
    try:
        with urllib.request.urlopen(url, timeout=10) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def stats(base):
    return json.loads(get(base + "/stats")[1])


def test_record_then_replay_without_upstream(env):
    up, start = env
    up.script = {"api": [(200, FEED, {})], "pdf": [(200, PDF, {})]}
    base = start(record=True)
    assert get(base + "/api/query?id_list=2401.00001&max_results=1") == (200, FEED)
    assert get(base + "/pdf/2401.00001v2.pdf") == (200, PDF)
    assert len(up.requests) == 2 and all(ua == rp.USER_AGENT for _, ua in up.requests)
    # Hits are served from disk; parameter order does not matter; upstream is not contacted again.
    assert get(base + "/api/query?max_results=1&id_list=2401.00001") == (200, FEED)
    assert get(base + "/pdf/2401.00001v2.pdf") == (200, PDF)
    assert len(up.requests) == 2
    assert stats(base)["hits"] == 2 and stats(base)["saved"] == 2
    # A replay-only proxy over the same directory serves the recording with no upstream at all.
    up.script = {"api": [(500, b"", {})], "pdf": [(500, b"", {})]}
    replay = start(record=False)
    assert get(replay + "/pdf/2401.00001v2.pdf") == (200, PDF)
    assert get(replay + "/api/query?id_list=9999.99999")[0] == 404  # not recorded


def test_rate_limit_is_retried_and_bad_bodies_never_saved(env):
    up, start = env
    up.script = {"api": [(429, b"", {"Retry-After": "0"}), (200, FEED, {})],
                 "pdf": [(200, b"<html>rate limited</html>", {})]}  # a 200 that is not a PDF
    base = start(record=True)
    assert get(base + "/api/query?id_list=1") == (200, FEED)
    assert len([r for r in up.requests if r[0].startswith("/api")]) == 2
    status, _ = get(base + "/pdf/1v1.pdf")
    assert status == 502
    assert stats(base)["saved"] == 1  # only the feed
    assert get(base + "/pdf/1v1.pdf")[0] == 502  # still not recorded, tried again


def test_upstream_failure_passes_status_through(env):
    up, start = env
    up.script = {"api": [(404, b"", {})], "pdf": [(503, b"", {})]}
    base = start(record=True)
    assert get(base + "/api/query?id_list=1")[0] == 404  # not retried
    assert len(up.requests) == 1
    assert get(base + "/pdf/x.pdf")[0] == 503  # retried, then arXiv's status: the client's retry applies
    assert len(up.requests) == 1 + 4


def test_rejects_bad_pdf_names(env):
    _, start = env
    base = start(record=True)
    for name in ["../secret", "a/b.pdf", "x" * 80]:
        assert get(base + "/pdf/" + name)[0] in (400, 404)


FEED2 = (b'<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">'
         b'<entry><id>http://arxiv.org/abs/2401.00001v2</id></entry>'
         b'<entry><id>http://arxiv.org/abs/2401.00002</id></entry></feed>')


def test_entry_pdf_names_match_go_fetch_urls():
    # internal/arxiv.PDFURL -> "<id>v<n>.pdf"; crawler without a version -> "<id>.pdf"
    assert rp.entry_pdf_names(FEED2) == ["2401.00001v2.pdf", "2401.00002.pdf"]


def test_search_query_string_matches_go_crawler():
    # The URL the Go crawler sent in CI run 43677271839 (url.Values.Encode sorts keys).
    assert rp.search_query_string("cat:cs.LG", 2) == \
        "max_results=2&search_query=cat%3Acs.LG&sortBy=submittedDate&sortOrder=descending&start=0"


def test_prefetch_warms_store_then_serve_needs_no_upstream(env, tmp_path):
    up, start = env
    up.script = {"api": [(200, FEED2, {})], "pdf": [(200, PDF, {})]}
    ids = tmp_path / "corpus.txt"
    ids.write_text("# comment\n2401.00001  # trailing comment\n\n")
    assert rp.main(["prefetch", "--dir", str(tmp_path), "--ids", str(ids), "--search", "cat:cs.LG", "--limit", "2",
                    "--id", "2401.00001"]) == 0  # --id repeats an --ids entry: fetched once
    n = len(up.requests)
    assert n == 4  # 2 feeds (id_list, search) + 2 PDFs; the search reuses the PDFs already stored
    # Second prefetch: everything is a hit.
    assert rp.main(["prefetch", "--dir", str(tmp_path), "--ids", str(ids)]) == 0
    assert len(up.requests) == n
    # The crawler's exact URL is served by a replay-only proxy.
    base = start(record=False)
    assert get(base + "/api/query?" + rp.search_query_string("cat:cs.LG", 2)) == (200, FEED2)
    assert get(base + "/pdf/2401.00001v2.pdf") == (200, PDF)


def test_prefetch_reports_failures(env, tmp_path):
    up, _ = env
    up.script = {"api": [(429, b"", {})], "pdf": [(200, PDF, {})]}
    ids = tmp_path / "c.txt"
    ids.write_text("2401.00001\n")
    assert rp.main(["prefetch", "--dir", str(tmp_path / "store"), "--ids", str(ids)]) == 1


def test_prefetch_budget_stops_early_and_reports_saved(env, tmp_path, monkeypatch):
    up, _ = env
    up.script = {"api": [(200, FEED, {})], "pdf": [(200, PDF, {})]}
    ids = tmp_path / "c.txt"
    ids.write_text("2401.00001\n2401.00002\n2401.00003\n")
    out = tmp_path / "gh_output"
    monkeypatch.setenv("GITHUB_OUTPUT", str(out))
    # A budget already spent after the first query: the rest are reported, what was fetched is kept.
    clock = iter([0.0, 0.0, 100.0, 100.0, 100.0])
    monkeypatch.setattr(rp.time, "monotonic", lambda: next(clock, 100.0))
    assert rp.main(["prefetch", "--dir", str(tmp_path / "s"), "--ids", str(ids), "--budget-seconds", "10"]) == 1
    assert out.read_text() == "saved=2\n"  # the first query only: its feed and its one PDF (entry "x" -> x.pdf)


EMPTY_FEED = b'<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"></feed>'


class Clock:
    """Fake monotonic clock: sleeps advance it instantly and are recorded; each upstream request takes 2 s."""

    def __init__(self, monkeypatch, up):
        self.t, self.sleeps, self.request_times = 0.0, [], []
        monkeypatch.setattr(rp.time, "monotonic", lambda: self.t)
        monkeypatch.setattr(rp.time, "sleep", self.sleep)
        up.on_request = self.request

    def sleep(self, s):
        self.sleeps.append((self.t, s))
        self.t += s

    def request(self, path):
        self.request_times.append((self.t, path))
        self.t += 2.0


def api_paths(up):
    return [p for p, _ in up.requests if p.startswith("/api")]


def test_prefetch_retries_only_failed_queries_until_they_succeed(env, tmp_path, monkeypatch, capsys):
    up, _ = env
    a, b = "/api/query?id_list=2401.00001", "/api/query?id_list=2401.00002"
    # One pass makes 4 attempts per item; the fifth request for A succeeds. B succeeds (no entries) at once.
    up.script = {a: [(429, b"", {"Retry-After": "90"})] * 5 + [(200, FEED, {})],
                 b: [(200, EMPTY_FEED, {})], "pdf": [(200, PDF, {})]}
    monkeypatch.setattr(rp, "PASS_WAIT", 60)
    clock = Clock(monkeypatch, up)
    out = tmp_path / "gh_output"
    monkeypatch.setenv("GITHUB_OUTPUT", str(out))
    store = tmp_path / "s"
    assert rp.main(["prefetch", "--dir", str(store), "--id", "2401.00001", "--id", "2401.00002",
                    "--budget-seconds", "1000"]) == 0
    assert rp.Recorder(str(store), False).load("api", rp.api_key("id_list=2401.00001")) == FEED
    paths = [p for p, _ in up.requests]
    assert paths == [a] * 4 + [b] + [a] * 2 + ["/pdf/x.pdf"]  # pass 2: only A, then A's PDF
    assert all(ua == rp.USER_AGENT for _, ua in up.requests)
    assert [s for _, s in clock.sleeps if s > rp.MAX_WAIT] == [90]  # one cool-down, >= Retry-After > PASS_WAIT
    err = capsys.readouterr()
    assert "retrying 1 queries in 90s" in err.err
    assert '"hits": 0' in err.out  # B's stored feed was not even re-read in pass 2
    assert out.read_text() == "saved=3\n"  # pass 1: B's feed; pass 2: A's feed + x.pdf


def test_prefetch_without_budget_makes_one_pass(env, tmp_path, monkeypatch):
    up, _ = env
    up.script = {"api": [(429, b"", {})] * 4 + [(200, FEED, {})], "pdf": [(200, PDF, {})]}
    monkeypatch.setattr(rp, "PASS_WAIT", 0)  # a second pass (wrong here) would not sleep, and would succeed
    assert rp.main(["prefetch", "--dir", str(tmp_path / "s"), "--id", "2401.00001"]) == 1
    assert len(up.requests) == 4  # one pass: 4 attempts, no second pass


def test_prefetch_never_requests_or_sleeps_past_the_deadline(env, tmp_path, monkeypatch, capsys):
    up, _ = env
    up.script = {"/api/query?id_list=2401.00001": [(429, b"", {})], "api": [(200, FEED, {})],
                 "pdf": [(200, PDF, {})]}
    monkeypatch.setattr(rp, "INTERVAL", 3.0)
    monkeypatch.setattr(rp, "RETRY_WAITS", (5, 15, 45))
    monkeypatch.setattr(rp, "PASS_WAIT", 60)
    clock = Clock(monkeypatch, up)
    budget = 200.0  # the clock starts at 0 at the first request: deadline 200
    assert rp.main(["prefetch", "--dir", str(tmp_path / "s"), "--id", "2401.00001", "--id", "2401.00002",
                    "--budget-seconds", str(budget)]) == 1
    assert clock.sleeps and all(t + s <= budget for t, s in clock.sleeps), clock.sleeps
    assert clock.request_times and all(t < budget for t, _ in clock.request_times), clock.request_times
    assert len(api_paths(up)) > 4  # a second pass ran within the budget
    assert "prefetch FAILED for 1: api?id_list=2401.00001 (time budget)" in capsys.readouterr().err


def test_prefetch_loop_fetches_only_through_recorder():
    # Politeness (INTERVAL, User-Agent) lives in Recorder.fetch: the prefetch path must not call urllib itself.
    import inspect
    for fn in (rp.main, rp.prefetch_query, rp.get_or_fetch):
        assert "urllib.request" not in inspect.getsource(fn) and "urlopen" not in inspect.getsource(fn)

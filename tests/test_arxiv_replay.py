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
        self.script = {}  # "api" | "pdf" -> list of (status, body, headers); last one repeats
        self.requests = []
        up = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                kind = "api" if self.path.startswith("/api") else "pdf"
                up.requests.append((self.path, self.headers.get("User-Agent")))
                seq = up.script[kind]
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
    assert rp.main(["prefetch", "--dir", str(tmp_path), "--ids", str(ids), "--search", "cat:cs.LG", "--limit", "2"]) == 0
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

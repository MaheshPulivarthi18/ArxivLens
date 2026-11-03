"""ING-8: no-network, no-PDF tests for the section-extraction eval (scripts/evaluate_extraction.py)."""

import io
import json
import os
import re
import subprocess
import sys
import types

import pytest

from scripts import evaluate_extraction as ee

REPO_ROOT = ee.REPO_ROOT
EVALS = ee.EVALS_DIR


def H(*titles, level=1):
    return [{"title": t, "level": level} for t in titles]


# ------------------------------------------------------------------------------------------ AC2 scoring
def test_normalize_title():
    assert ee.normalize_title("3.1 Results.") == "results"
    assert ee.normalize_title("IV. Related   Work") == "related work"
    assert ee.normalize_title("A Appendix: Proofs:") == "appendix: proofs"
    assert ee.normalize_title("  2)  Method ") == "method"
    assert ee.normalize_title("Introduction") == "introduction"


def test_perfect_match():
    s = ee.score_paper(H("1 Introduction", "2 Method", "3 Results"), H("Introduction", "Method", "Results"))
    assert (s["tp"], s["precision"], s["recall"], s["f1"]) == (3, 1.0, 1.0, 1.0)
    assert s["false_positives"] == [] and s["missed"] == []


def test_spurious_heading_lowers_precision_only():
    s = ee.score_paper(H("Introduction", "Table 1: Accuracy", "Method"), H("Introduction", "Method"))
    assert s["precision"] == pytest.approx(2 / 3) and s["recall"] == 1.0
    assert s["false_positives"] == ["Table 1: Accuracy"]


def test_missed_heading_lowers_recall_only():
    s = ee.score_paper(H("Introduction", "Results"), H("Introduction", "Method", "Results"))
    assert s["precision"] == 1.0 and s["recall"] == pytest.approx(2 / 3)
    assert s["missed"] == ["Method"]


def test_numbering_case_punctuation_normalisation():
    s = ee.score_paper(H("3.1 Results."), H("results"))
    assert s["tp"] == 1 and s["precision"] == 1.0 and s["recall"] == 1.0


def test_order_preserved_out_of_order_duplicate_does_not_match():
    s = ee.score_paper(H("Introduction", "Method", "Results", "Method"), H("Introduction", "Method", "Results"))
    assert s["tp"] == 3 and s["precision"] == 0.75 and s["recall"] == 1.0
    assert s["false_positives"] == ["Method"]
    # swapped order: only one of the two can match
    s = ee.score_paper(H("Results", "Method"), H("Method", "Results"))
    assert s["tp"] == 1


def test_implicit_abstract_excluded_from_both_lists():
    pred = [{"title": "Abstract", "level": 1}] + H("1 Introduction")
    s = ee.score_paper(pred, H("Abstract", "Introduction"))
    assert (s["n_predicted"], s["n_expected"], s["tp"]) == (1, 1, 1)
    assert s["precision"] == 1.0 and s["false_positives"] == []


def test_empty_prediction_is_zero_not_crash():
    s = ee.score_paper([], H("Introduction", "Method"))
    assert (s["precision"], s["recall"], s["f1"]) == (0.0, 0.0, 0.0)
    assert s["level_accuracy"] is None
    assert s["missed"] == ["Introduction", "Method"]


def test_level_accuracy_over_tps():
    s = ee.score_paper([{"title": "Intro", "level": 1}, {"title": "Setup", "level": 1}],
                       [{"title": "Intro", "level": 1}, {"title": "Setup", "level": 2}])
    assert s["level_accuracy"] == 0.5


def test_micro_average_over_two_papers():
    a = ee.score_paper(H("Introduction", "Method"), H("Introduction", "Method"))           # 2/2, 2/2
    b = ee.score_paper(H("Intro", "Noise", "Noise 2"), H("Intro", "Method", "Results", "Conclusion"))  # 1/3, 1/4
    m = ee.micro_average([a, b])
    assert (m["tp"], m["n_predicted"], m["n_expected"]) == (3, 5, 6)
    assert m["precision"] == pytest.approx(3 / 5) and m["recall"] == pytest.approx(3 / 6)
    assert m["f1"] == pytest.approx(2 * 0.6 * 0.5 / 1.1)


# ------------------------------------------------------------------------------------------ AC1 dataset
GOOD_ROW = {"arxiv_id": "2301.00001", "version": "v2", "license": "CC BY 4.0", "layout": "two-column",
            "expected_headings": [{"title": "Introduction", "level": 1}]}


def _with(**kw):
    r = json.loads(json.dumps(GOOD_ROW))
    r.update(kw)
    return r


def test_validate_dataset_accepts_good_row():
    assert ee.validate_dataset([GOOD_ROW]) == []


@pytest.mark.parametrize("row,needle", [
    (_with(version="2"), "version"),
    (_with(version="latest"), "version"),
    (_with(license="CC BY-NC-SA 4.0"), "license"),
    (_with(license="arXiv non-exclusive"), "license"),
    (_with(expected_headings=[]), "expected_headings"),
    (_with(arxiv_id="2301.00001v2"), "arxiv_id"),
    (_with(expected_headings=[{"title": "Intro"}]), "bad heading"),
    ({k: v for k, v in GOOD_ROW.items() if k != "layout"}, "keys"),
    (_with(extra=1), "keys"),
])
def test_validate_dataset_rejects(row, needle):
    problems = ee.validate_dataset([row])
    assert problems and needle in problems[0], problems


def test_validate_dataset_rejects_duplicate_ids():
    assert any("duplicate" in p for p in ee.validate_dataset([GOOD_ROW, GOOD_ROW]))


DATASET_PENDING = (
    "evals/extraction.jsonl not present yet: it must be hand-annotated from the PDFs with licences checked on the "
    "arXiv abs pages, which needs arxiv.org access (see ING-8 Notes). AC1 is NOT met while this skips."
)


@pytest.mark.skipif(not os.path.exists(ee.DEFAULT_DATASET), reason=DATASET_PENDING)
def test_real_dataset_schema_size_and_layout_mix():
    rows = ee.load_dataset()
    assert ee.validate_dataset(rows) == []
    assert len(rows) >= 10
    assert len({r["layout"] for r in rows}) >= 3


def test_no_pdf_under_evals():
    pdfs = [os.path.join(d, f) for d, _, fs in os.walk(EVALS) for f in fs if f.lower().endswith(".pdf")]
    assert pdfs == []


# ------------------------------------------------------------------------------------------ AC4 fetching
class FakeClock:
    def __init__(self):
        self.t = 1000.0
        self.sleeps = []
        self.request_times = []

    def clock(self):
        return self.t

    def sleep(self, s):
        self.sleeps.append(s)
        self.t += s


class FakeResp(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


def _fetcher(tmp_path, fc, **kw):
    def urlopen(req, timeout):
        fc.request_times.append(fc.t)
        assert "ArxivLens" in req.get_header("User-agent")
        assert timeout > 0
        fc.t += 0.4  # download time
        return FakeResp(b"%PDF-1.5 fake")

    return ee.PoliteFetcher(cache_dir=str(tmp_path / ".cache"), clock=fc.clock, sleep=fc.sleep, urlopen=urlopen, **kw)


def test_requests_are_at_least_3s_apart_and_cache_hits_are_free(tmp_path):
    fc = FakeClock()
    f = _fetcher(tmp_path, fc)
    for aid in ("2301.00001", "2301.00002", "2301.00003"):
        f.fetch(aid, "v1")
    f.fetch("2301.00001", "v1")  # cached: no request
    assert f.requests == 3 and len(fc.request_times) == 3
    gaps = [b - a for a, b in zip(fc.request_times, fc.request_times[1:], strict=False)]
    assert all(g >= ee.MIN_REQUEST_INTERVAL_S for g in gaps), gaps
    assert ee.MIN_REQUEST_INTERVAL_S >= 3.0


def test_pdfs_cached_under_cache_dir_not_evals(tmp_path):
    fc = FakeClock()
    path = _fetcher(tmp_path, fc).fetch("2301.00001", "v2")
    assert path == str(tmp_path / ".cache" / "2301.00001v2.pdf") and os.path.exists(path)
    assert os.path.relpath(ee.DEFAULT_CACHE_DIR, REPO_ROOT).split(os.sep)[0] == ".cache"
    with pytest.raises(ValueError):
        ee.PoliteFetcher(cache_dir=os.path.join(EVALS, "pdfs"))
    with pytest.raises(ValueError):
        ee.PoliteFetcher(cache_dir=EVALS)


def test_cache_dir_is_git_ignored():
    out = subprocess.run(["git", "check-ignore", "-q", ".cache/extraction/2301.00001v2.pdf"], cwd=REPO_ROOT,
                         check=False)
    assert out.returncode == 0


def test_offline_never_requests(tmp_path):
    fc = FakeClock()
    f = _fetcher(tmp_path, fc, offline=True)
    with pytest.raises(FileNotFoundError):
        f.fetch("2301.00001", "v1")
    assert fc.request_times == []


def test_non_pdf_response_is_rejected_and_not_cached(tmp_path):
    f = ee.PoliteFetcher(cache_dir=str(tmp_path), urlopen=lambda req, timeout: FakeResp(b"<html>"),
                         sleep=lambda s: None)
    with pytest.raises(ValueError):
        f.fetch("2301.00001", "v1")
    assert os.listdir(tmp_path) == []


# ------------------------------------------------------------------------------------------ AC3 production detector
class StubFetcher:
    def __init__(self):
        self.requests = 0

    def fetch(self, arxiv_id, version):
        return f"/nonexistent/{arxiv_id}{version}.pdf"


def test_run_path_calls_production_parse_doc_sections(monkeypatch):
    calls = []

    class ProcessorWorker:
        def __init__(self):  # production __init__ connects to MinIO/Kafka: the scorer must never call it
            raise AssertionError("ProcessorWorker must not be constructed")

        def _parse_doc_sections(self, doc):
            calls.append((self, doc))
            return [{"title": "Abstract", "level": 1, "text": "x", "page": 1},
                    {"title": "Introduction", "level": 1, "text": "x", "page": 1},
                    {"title": "Spurious", "level": 2, "text": "x", "page": 2}]

    fake = types.ModuleType("services.processor.worker")
    fake.ProcessorWorker = ProcessorWorker
    monkeypatch.setitem(sys.modules, "services.processor.worker", fake)

    rows = [dict(GOOD_ROW, expected_headings=H("Abstract", "Introduction", "Method"))]
    res = ee.run(rows, StubFetcher(), pdf_opener=lambda path: ("doc", path))
    assert calls == [(None, ("doc", "/nonexistent/2301.00001v2.pdf"))]
    p = res["papers"][0]
    assert (p["tp"], p["n_predicted"], p["n_expected"]) == (1, 2, 2)
    assert p["false_positives"] == ["Spurious"] and p["missed"] == ["Method"]
    assert res["complete"] is True


def test_failed_paper_fails_closed(monkeypatch):
    def boom(path):
        raise OSError("cannot open")

    rows = [GOOD_ROW]
    res = ee.run(rows, StubFetcher(), detect=lambda doc: [], pdf_opener=boom)
    assert res["complete"] is False and res["errors"] == 1
    assert res["micro"]["recall"] == 0.0 and res["micro"]["n_expected"] == 1
    assert "cannot open" in res["papers"][0]["error"]


def test_no_copied_heuristic_in_scorer():
    with open(ee.__file__, encoding="utf-8") as f:
        src = f.read()
    assert not re.search(r"heading_threshold|1\.15", src)
    assert "ProcessorWorker._parse_doc_sections(None, doc)" in src


def test_scorer_import_does_not_pull_ml_stack_or_kafka():
    code = ("import sys; import scripts.evaluate_extraction; "
            "bad = [m for m in ('torch', 'transformers', 'confluent_kafka', 'services.processor.worker', 'pymupdf') "
            "if m in sys.modules]; assert not bad, bad")
    subprocess.run([sys.executable, "-c", code], cwd=REPO_ROOT, check=True, env={**os.environ, "PYTHONPATH": REPO_ROOT})


def test_help_runs_without_network():
    out = subprocess.run([sys.executable, "scripts/evaluate_extraction.py", "--help"], cwd=REPO_ROOT,
                         capture_output=True, text=True, check=False, env={**os.environ, "PYTHONPATH": REPO_ROOT})
    assert out.returncode == 0 and "--offline" in out.stdout


def test_main_writes_results_json(tmp_path, monkeypatch):
    ds = tmp_path / "ds.jsonl"
    ds.write_text(json.dumps(GOOD_ROW) + "\n")
    monkeypatch.setattr(ee, "load_production_detector", lambda: (lambda doc: [{"title": "1 Introduction", "level": 1}]))
    monkeypatch.setattr(ee, "open_pdf", lambda path: object())
    cache = tmp_path / "cache"
    cache.mkdir()
    (cache / "2301.00001v2.pdf").write_bytes(b"%PDF-1.5")
    rc = ee.main(["--dataset", str(ds), "--cache-dir", str(cache), "--results-dir", str(tmp_path / "res"), "--offline"])
    assert rc == 0
    (out,) = list((tmp_path / "res").glob("extraction-*.json"))
    data = json.loads(out.read_text())
    assert data["micro"]["f1"] == 1.0 and data["dataset_rows"] == 1 and data["network_requests"] == 0
    assert "commit" in data


def test_main_missing_dataset_exits_2(tmp_path):
    assert ee.main(["--dataset", str(tmp_path / "nope.jsonl"), "--results-dir", str(tmp_path / "res")]) == 2


def test_main_rejects_invalid_dataset(tmp_path):
    ds = tmp_path / "ds.jsonl"
    ds.write_text(json.dumps(_with(license="CC BY-NC 4.0")) + "\n")
    assert ee.main(["--dataset", str(ds), "--results-dir", str(tmp_path / "res")]) == 2

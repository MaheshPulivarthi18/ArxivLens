#!/usr/bin/env python3
"""
ArxivLens - Section-extraction quality eval (ING-8)

Measures how well the PRODUCTION section detector (services/processor/worker.py
ProcessorWorker._parse_doc_sections, Technical PRD §6.1) finds the headings of real arXiv papers.

Dataset: evals/extraction.jsonl, one paper per line:
  {"arxiv_id", "version" (pinned, "v2"), "license", "layout", "expected_headings": [{"title", "level"}, ...]}
Headings are annotated by hand from the PDF, in document order. Only CC BY / CC BY-SA / CC0 papers.

Scoring (documented in evals/README.md):
  - Titles are normalised: strip leading numbering, lower-case, collapse whitespace, strip trailing punctuation.
  - "Abstract" is excluded from both lists (the detector's implicit leading Abstract section, PRD §6.1 rule 4).
  - Predicted headings are matched greedily, in order, to the first unmatched expected heading with an equal
    normalised title after the last match (order-preserving). TP = matched.
  - Boundary precision = TP / predicted, boundary recall = TP / expected, F1; per paper and micro-averaged.
    Level accuracy over the TPs is informational.

PDFs are fetched from arxiv.org at run time (<= 1 request / 3 s, identifying User-Agent, Technical PRD §9) into a
git-ignored cache under .cache/. They are never written under evals/ and never committed.

The detector is the production code, called read-only. It is imported lazily (it pulls in the ML stack and Kafka),
so --help and the scoring helpers work without them. Never copy the heuristic into this file.
"""

import argparse
import datetime as _dt
import json
import os
import re
import string
import subprocess
import sys
import time
import urllib.request
from typing import Any, Callable, Dict, List, Optional, Sequence

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EVALS_DIR = os.path.join(REPO_ROOT, "evals")
DEFAULT_DATASET = os.path.join(EVALS_DIR, "extraction.jsonl")
DEFAULT_CACHE_DIR = os.path.join(REPO_ROOT, ".cache", "extraction")
DEFAULT_RESULTS_DIR = os.path.join(EVALS_DIR, "results")

ALLOWED_LICENSES = frozenset({"CC BY 4.0", "CC BY-SA 4.0", "CC0 1.0"})
REQUIRED_KEYS = frozenset({"arxiv_id", "version", "license", "layout", "expected_headings"})
ARXIV_ID_RE = re.compile(r"^\d{4}\.\d{4,5}$")
VERSION_RE = re.compile(r"^v\d+$")

MIN_REQUEST_INTERVAL_S = 3.0  # arXiv politeness (Technical PRD §9). Not configurable on purpose.
USER_AGENT = "ArxivLens/1.0 (Extraction Eval; mailto:contact@arxivlens.local)"
PDF_URL = "https://arxiv.org/pdf/{arxiv_id}{version}"
FETCH_TIMEOUT_S = 60

_NUMBERING_RE = re.compile(r"^\s*(\d+(\.\d+)*|[IVXLCDM]+|[A-Z])[.)]?\s+")
_WS_RE = re.compile(r"\s+")
EXCLUDED_TITLES = frozenset({"abstract"})

Heading = Dict[str, Any]  # {"title": str, "level": int}


# --------------------------------------------------------------------------------------------- scoring
def normalize_title(title: str) -> str:
    """Strip leading numbering (on the original case, so Roman numerals and letters are recognised),
    lower-case, collapse whitespace, strip trailing punctuation."""
    t = _NUMBERING_RE.sub("", title or "", count=1)
    t = _WS_RE.sub(" ", t.lower()).strip()
    return t.rstrip(string.punctuation + " ").strip()


def _drop_excluded(headings: Sequence[Heading]) -> List[Heading]:
    return [h for h in headings if normalize_title(h.get("title", "")) not in EXCLUDED_TITLES]


def match_headings(predicted: Sequence[Heading], expected: Sequence[Heading]) -> List[tuple]:
    """Order-preserving greedy match. Returns (predicted_index, expected_index) pairs."""
    exp_norm = [normalize_title(h["title"]) for h in expected]
    pairs = []
    last = -1
    for i, p in enumerate(predicted):
        p_norm = normalize_title(p["title"])
        for j in range(last + 1, len(expected)):
            if exp_norm[j] == p_norm:
                pairs.append((i, j))
                last = j
                break
    return pairs


def _prf(tp: int, n_pred: int, n_exp: int) -> Dict[str, float]:
    p = tp / n_pred if n_pred else 0.0
    r = tp / n_exp if n_exp else 0.0
    f1 = 2 * p * r / (p + r) if (p + r) else 0.0
    return {"precision": p, "recall": r, "f1": f1}


def score_paper(predicted: Sequence[Heading], expected: Sequence[Heading]) -> Dict[str, Any]:
    pred = _drop_excluded(predicted)
    exp = _drop_excluded(expected)
    pairs = match_headings(pred, exp)
    tp = len(pairs)
    level_hits = sum(1 for i, j in pairs if pred[i].get("level") == exp[j].get("level"))
    matched_p = {i for i, _ in pairs}
    matched_e = {j for _, j in pairs}
    out = {"tp": tp, "n_predicted": len(pred), "n_expected": len(exp)}
    out.update(_prf(tp, len(pred), len(exp)))
    out["level_accuracy"] = level_hits / tp if tp else None
    out["level_hits"] = level_hits
    out["false_positives"] = [pred[i]["title"] for i in range(len(pred)) if i not in matched_p]
    out["missed"] = [exp[j]["title"] for j in range(len(exp)) if j not in matched_e]
    return out


def micro_average(paper_scores: Sequence[Dict[str, Any]]) -> Dict[str, Any]:
    tp = sum(s["tp"] for s in paper_scores)
    n_pred = sum(s["n_predicted"] for s in paper_scores)
    n_exp = sum(s["n_expected"] for s in paper_scores)
    level_hits = sum(s["level_hits"] for s in paper_scores)
    out = {"tp": tp, "n_predicted": n_pred, "n_expected": n_exp, "papers": len(paper_scores)}
    out.update(_prf(tp, n_pred, n_exp))
    out["level_accuracy"] = level_hits / tp if tp else None
    return out


# --------------------------------------------------------------------------------------------- dataset
def load_dataset(path: str = DEFAULT_DATASET) -> List[Dict[str, Any]]:
    rows = []
    with open(path, encoding="utf-8") as f:
        for n, raw in enumerate(f, 1):
            line = raw.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as e:
                raise ValueError(f"{path}:{n}: invalid JSON: {e}") from e
    return rows


def validate_dataset(rows: Sequence[Dict[str, Any]]) -> List[str]:
    """Return a list of problems (empty = valid). Size/layout-mix minimums are checked by the tests."""
    problems = []
    seen = set()
    for n, r in enumerate(rows, 1):
        where = f"row {n} ({r.get('arxiv_id', '?')})"
        if set(r) != REQUIRED_KEYS:
            problems.append(f"{where}: keys {sorted(r)} != {sorted(REQUIRED_KEYS)}")
            continue
        if not isinstance(r["arxiv_id"], str) or not ARXIV_ID_RE.match(r["arxiv_id"]):
            problems.append(f"{where}: arxiv_id must look like 2301.01234 (no version)")
        if r["arxiv_id"] in seen:
            problems.append(f"{where}: duplicate arxiv_id")
        seen.add(r["arxiv_id"])
        if not isinstance(r["version"], str) or not VERSION_RE.match(r["version"]):
            problems.append(f"{where}: version must match ^v\\d+$")
        if r["license"] not in ALLOWED_LICENSES:
            problems.append(f"{where}: license {r['license']!r} not in {sorted(ALLOWED_LICENSES)}")
        if not isinstance(r["layout"], str) or not r["layout"].strip():
            problems.append(f"{where}: layout must be a non-empty string")
        hs = r["expected_headings"]
        if not isinstance(hs, list) or not hs:
            problems.append(f"{where}: expected_headings must be a non-empty list")
            continue
        for h in hs:
            if (
                not isinstance(h, dict)
                or set(h) != {"title", "level"}
                or not isinstance(h["title"], str)
                or not h["title"].strip()
                or not isinstance(h["level"], int)
                or h["level"] < 1
            ):
                problems.append(f"{where}: bad heading {h!r} (need {{'title': str, 'level': int >= 1}})")
    return problems


# --------------------------------------------------------------------------------------------- fetching
def _is_within(path: str, parent: str) -> bool:
    path, parent = os.path.realpath(path), os.path.realpath(parent)
    return path == parent or path.startswith(parent + os.sep)


class PoliteFetcher:
    """Fetches arXiv PDFs into a cache dir, at most one network request per MIN_REQUEST_INTERVAL_S.
    Cache hits make no request. clock/sleep/urlopen are injectable for tests."""

    def __init__(
        self,
        cache_dir: str = DEFAULT_CACHE_DIR,
        clock: Callable[[], float] = time.monotonic,
        sleep: Callable[[float], None] = time.sleep,
        urlopen: Callable[..., Any] = urllib.request.urlopen,
        offline: bool = False,
    ):
        if _is_within(cache_dir, EVALS_DIR):
            raise ValueError(f"cache dir {cache_dir} is under evals/; PDFs must never be stored there")
        self.cache_dir = cache_dir
        self.clock = clock
        self.sleep = sleep
        self.urlopen = urlopen
        self.offline = offline
        self._last_request: Optional[float] = None
        self.requests = 0

    def cache_path(self, arxiv_id: str, version: str) -> str:
        return os.path.join(self.cache_dir, f"{arxiv_id}{version}.pdf")

    def _wait_turn(self) -> None:
        if self._last_request is not None:
            wait = MIN_REQUEST_INTERVAL_S - (self.clock() - self._last_request)
            if wait > 0:
                self.sleep(wait)
        self._last_request = self.clock()

    def fetch(self, arxiv_id: str, version: str) -> str:
        path = self.cache_path(arxiv_id, version)
        if os.path.exists(path):
            return path
        if self.offline:
            raise FileNotFoundError(f"{path} not cached and --offline is set")
        self._wait_turn()
        self.requests += 1
        url = PDF_URL.format(arxiv_id=arxiv_id, version=version)
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        with self.urlopen(req, timeout=FETCH_TIMEOUT_S) as resp:
            data = resp.read()
        if not data.startswith(b"%PDF"):
            raise ValueError(f"{url} did not return a PDF")
        os.makedirs(self.cache_dir, exist_ok=True)
        tmp = path + ".part"
        with open(tmp, "wb") as f:
            f.write(data)
        os.replace(tmp, path)
        return path


# --------------------------------------------------------------------------------------------- running
def load_production_detector() -> Callable[[Any], list]:
    """The production detector, imported lazily. ProcessorWorker.__init__ connects to MinIO and Kafka, so the
    class is never instantiated; _parse_doc_sections uses no instance state.
    TODO(backend follow-up, ING-8 Notes): switch to a pure parse_doc_sections(doc) once it is exposed."""
    from services.processor.worker import ProcessorWorker

    def detect(doc) -> list:
        return ProcessorWorker._parse_doc_sections(None, doc)

    return detect


def open_pdf(path: str):
    try:
        import pymupdf
    except ImportError:  # older PyMuPDF releases
        import fitz as pymupdf
    return pymupdf.open(path)


def sections_to_headings(sections: Sequence[Dict[str, Any]]) -> List[Heading]:
    return [{"title": s.get("title", ""), "level": s.get("level")} for s in sections]


def run(
    rows: Sequence[Dict[str, Any]],
    fetcher: PoliteFetcher,
    detect: Optional[Callable[[Any], list]] = None,
    pdf_opener: Optional[Callable[[str], Any]] = None,
) -> Dict[str, Any]:
    """Score every row. A paper that can't be fetched/parsed fails closed: it counts with zero predictions
    (so it lowers micro recall) and the run is marked incomplete."""
    if detect is None:
        detect = load_production_detector()
    if pdf_opener is None:
        pdf_opener = open_pdf
    papers = []
    for r in rows:
        entry = {k: r[k] for k in ("arxiv_id", "version", "layout", "license")}
        try:
            doc = pdf_opener(fetcher.fetch(r["arxiv_id"], r["version"]))
            try:
                predicted = sections_to_headings(detect(doc))
            finally:
                close = getattr(doc, "close", None)
                if close:
                    close()
            entry["error"] = None
        except Exception as e:  # noqa: BLE001 - recorded per paper, run marked incomplete
            predicted = []
            entry["error"] = f"{type(e).__name__}: {e}"
        entry.update(score_paper(predicted, r["expected_headings"]))
        papers.append(entry)
    errors = sum(1 for p in papers if p["error"])
    return {"micro": micro_average(papers), "papers": papers, "errors": errors, "complete": errors == 0}


def git_sha() -> Optional[str]:
    try:
        return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, text=True).strip()
    except Exception:  # noqa: BLE001
        return None


def _fmt(x: Optional[float]) -> str:
    return "  -  " if x is None else f"{x:.3f}"


def format_table(result: Dict[str, Any]) -> str:
    lines = [f"{'paper':<18} {'layout':<14} {'pred':>4} {'exp':>4} {'tp':>3} {'P':>6} {'R':>6} {'F1':>6} {'lvl':>6}"]
    for p in result["papers"]:
        lines.append(
            f"{p['arxiv_id'] + p['version']:<18} {p['layout'][:14]:<14} {p['n_predicted']:>4} {p['n_expected']:>4} "
            f"{p['tp']:>3} {_fmt(p['precision']):>6} {_fmt(p['recall']):>6} {_fmt(p['f1']):>6} "
            f"{_fmt(p['level_accuracy']):>6}" + (f"  ERROR {p['error']}" if p["error"] else "")
        )
    m = result["micro"]
    lines.append(
        f"{'MICRO':<18} {'':<14} {m['n_predicted']:>4} {m['n_expected']:>4} {m['tp']:>3} {_fmt(m['precision']):>6} "
        f"{_fmt(m['recall']):>6} {_fmt(m['f1']):>6} {_fmt(m['level_accuracy']):>6}"
    )
    return "\n".join(lines)


def main(argv: Optional[Sequence[str]] = None) -> int:
    ap = argparse.ArgumentParser(
        description="Score the production PDF section detector against hand-annotated headings (ING-8). "
        "Fetches PDFs from arxiv.org (<= 1 request / 3 s) into a git-ignored cache. "
        "Exit 0 = all papers scored, 2 = could not run or some papers failed (see 'error')."
    )
    ap.add_argument("--dataset", default=DEFAULT_DATASET, help="JSONL dataset (default: evals/extraction.jsonl)")
    ap.add_argument("--cache-dir", default=DEFAULT_CACHE_DIR, help="PDF cache, must not be under evals/ "
                    "(default: .cache/extraction)")
    ap.add_argument("--results-dir", default=DEFAULT_RESULTS_DIR, help="where extraction-<ts>.json is written "
                    "(default: evals/results, git-ignored)")
    ap.add_argument("--offline", action="store_true", help="use cached PDFs only; never contact arxiv.org")
    args = ap.parse_args(argv)

    if not os.path.exists(args.dataset):
        print(f"could not run: dataset {args.dataset} not found", file=sys.stderr)
        return 2
    rows = load_dataset(args.dataset)
    problems = validate_dataset(rows)
    if problems:
        print("dataset invalid:\n  " + "\n  ".join(problems), file=sys.stderr)
        return 2
    fetcher = PoliteFetcher(cache_dir=args.cache_dir, offline=args.offline)
    result = run(rows, fetcher)

    ts = _dt.datetime.now(_dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    result.update({"timestamp": ts, "commit": git_sha(), "dataset": os.path.relpath(args.dataset, REPO_ROOT),
                   "dataset_rows": len(rows), "network_requests": fetcher.requests})
    os.makedirs(args.results_dir, exist_ok=True)
    out_path = os.path.join(args.results_dir, f"extraction-{ts}.json")
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(result, f, indent=2)
    print(format_table(result))
    print(f"\ncommit {result['commit']}  rows {len(rows)}  errors {result['errors']}  -> {out_path}")
    for p in result["papers"]:
        if p["false_positives"] or p["missed"]:
            print(f"\n{p['arxiv_id']}{p['version']}")
            print(f"  false positives: {p['false_positives']}\n  missed: {p['missed']}")
    return 0 if result["complete"] else 2


if __name__ == "__main__":
    sys.exit(main())

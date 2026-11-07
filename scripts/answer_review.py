#!/usr/bin/env python3
"""
ArxivLens: blind human review of /v1/ask answers (EVAL-7). Report-only, never gated. Usage: evals/README.md.
export: per answered case of a results JSON, one row per (claim, cited passage). Results hold no text, so each case is
  re-asked; a case whose (marker, chunk_id, score, supported) fingerprint differs is skipped as drift. Writes a blind
  Markdown sheet (no score/supported/status) and an IDs-only judgments JSONL. score: precision, rates, claim
  correctness, judged counts, human x system matrix (--results); --save writes the committable copy. Humans label.
"""

import argparse
import json
import os
import re
import sys
from typing import Any, Callable, Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from scripts import evaluate_answers as ea  # noqa: E402

SUPPORTS = ("yes", "partial", "no", "contradicts")
CLAIM_CORRECT = ("yes", "no", "unsure")
ID_KEYS = ("run_id", "row_id", "case_id", "unit", "marker", "chunk_id", "arxiv_id")
LABEL_KEYS = ("supports", "claim_correct", "reviewer", "date")
COMMITTED_KEYS, LOCAL_KEYS = set(ID_KEYS + LABEL_KEYS), set(ID_KEYS + LABEL_KEYS + ("notes",))
DATE, ARXIV, RUN_ID = re.compile(r"^\d{4}-\d{2}-\d{2}$"), re.compile(r"^\d{4}\.\d{4,5}$"), re.compile(r"^[\w.-]+$")
CASE_FILES = ("answer.jsonl", "refusal.jsonl", "refusal_near_miss.jsonl")


def results_cases(results: Dict[str, Any]) -> List[Dict[str, Any]]:
    """Per-case records from an eval_gate results file ({"answers": {"cases"}}) or evaluate_answers --json-out."""
    return ((results.get("answers") or results).get("cases")) or []


def units(resp: Dict[str, Any]) -> List[Tuple[str, List[int]]]:
    """(text, in-range markers) per unit: claims[] items when present, else answer sentences (as EVAL-6)."""
    in_range = {c.get("marker") for c in resp.get("citations") or []}
    claims = resp.get("claims")
    if isinstance(claims, list) and claims:
        raw = [(c.get("text") or "", c.get("markers") or []) if isinstance(c, dict) else ("", []) for c in claims]
    else:
        raw = [(s, ea.MARKER.findall(s)) for s in ea.split_sentences(resp.get("answer") or "")]
    return [(text, sorted({int(m) for m in marks if str(m).isdigit()} & in_range)) for text, marks in raw]


def build_rows(run_id: str, results: Dict[str, Any], questions: Dict[str, str], ask: ea.AskFn,
               fetch_passages: Optional[Callable[[List[int]], Dict[int, str]]] = None
               ) -> Tuple[List[Dict[str, Any]], List[str]]:
    """Sheet rows (with text, for the local sheet only) and drifted case ids."""
    rows: List[Dict[str, Any]] = []
    drift: List[str] = []
    for case in results_cases(results):
        if case.get("outcome") != "answered":
            continue
        cid = case["id"]
        resp = ask(questions[cid])[0] if cid in questions else None  # unknown id: the case set changed
        want, got = [sorted((c.get("marker"), c.get("chunk_id"), round(c.get("score") or 0, 3), c.get("supported"))
                            for c in cs or []) for cs in (case.get("citations"), (resp or {}).get("citations"))]
        if ea.classify(resp) != "answered" or want != got:  # fingerprint: same pairs, scores and verdicts
            drift.append(cid)
            continue
        by_marker = {c["marker"]: c for c in resp["citations"]}
        passages = (fetch_passages([c[1] for c in want]) if fetch_passages
                    else {c["chunk_id"]: c.get("content") or "" for c in resp["citations"]})
        for u, (text, marks) in enumerate(units(resp)):
            for m in marks:
                c = by_marker[m]
                rows.append({"run_id": run_id, "row_id": f"{cid}:u{u}:m{m}", "case_id": cid, "unit": u,
                             "marker": m, "chunk_id": c["chunk_id"], "arxiv_id": c.get("arxiv_id"),
                             "question": questions[cid], "claim": text,
                             "passage": passages.get(c["chunk_id"], "")})
    return rows, drift


def write_sheet(rows: List[Dict[str, Any]], path: str) -> None:
    with open(path, "w", encoding="utf-8") as f:
        f.write(f"# Answer review sheet: {rows[0]['run_id'] if rows else '(empty)'}\n\n"
                "Judge each claim against its passage only. Write labels in the .judgments.jsonl file.\n")
        case = None
        for r in rows:
            if r["case_id"] != case:
                case = r["case_id"]
                f.write(f"\n## {case}: {r['question']}\n")
            quoted = "\n".join("> " + line for line in (r["passage"] or "(passage not found)").splitlines())
            f.write(f"\n### {r['row_id']} (marker [{r['marker']}], {r['arxiv_id']}, chunk {r['chunk_id']})\n\n"
                    f"**Claim:** {r['claim']}\n\n{quoted}\n")


def validate_judgment(row: Any, committed: bool = False) -> List[str]:
    """Errors for one judgments row. Committed rows may hold only IDs and labels (no notes, no text)."""
    if not isinstance(row, dict):
        return ["row is not an object"]
    allowed = COMMITTED_KEYS if committed else LOCAL_KEYS
    errs = [f"key not allowed: {k}" for k in sorted(set(row) - allowed)]
    errs += [f"missing key: {k}" for k in ID_KEYS if k not in row]
    for k, ok in (("supports", SUPPORTS), ("claim_correct", CLAIM_CORRECT)):
        if row.get(k) not in ok + (None,):
            errs.append(f"bad {k}: {row.get(k)!r}")
    for k in ("run_id", "row_id", "case_id", "arxiv_id"):
        if not isinstance(row.get(k), str) or len(row[k]) > 64:
            errs.append(f"{k} must be a short string")
    if committed:  # IDs only: no free text hidden in an ID field
        errs += [f"bad {k} shape" for k, ok in (("arxiv_id", ARXIV), ("run_id", RUN_ID), ("case_id", RUN_ID))
                 if not isinstance(row.get(k), str) or not ok.match(row[k])]
        if row.get("row_id") != f"{row.get('case_id')}:u{row.get('unit')}:m{row.get('marker')}":
            errs.append("row_id must be <case_id>:u<unit>:m<marker>")
    for k in ("unit", "marker", "chunk_id"):
        if not isinstance(row.get(k), int) or isinstance(row.get(k), bool):
            errs.append(f"{k} must be an integer")
    if row.get("supports") is not None or row.get("claim_correct") is not None:
        rev = row.get("reviewer")
        if not isinstance(rev, str) or not rev.strip() or len(rev) > 64:
            errs.append("judged row needs a reviewer (short name or handle)")
        if not isinstance(row.get("date"), str) or not DATE.match(row["date"]):
            errs.append("judged row needs date YYYY-MM-DD")
    if not isinstance(row.get("notes", ""), str):
        errs.append("notes must be a string")
    return errs


def load_judgments(path: str, committed: bool = False) -> List[Dict[str, Any]]:
    rows = ea.load_cases(path)
    if not all(isinstance(r, dict) for r in rows):
        raise ValueError(f"{path}: every line must be a JSON object")
    errs = [f"line {i + 1}: {e}" for i, r in enumerate(rows) for e in validate_judgment(r, committed)]
    seen: Dict[str, Any] = {}
    for r in rows:
        key = (r.get("case_id"), r.get("unit"))
        if r.get("claim_correct") is not None and seen.setdefault(key, r["claim_correct"]) != r["claim_correct"]:
            errs.append(f"claim {key}: conflicting claim_correct labels")
    if len({r.get("row_id") for r in rows}) != len(rows):
        errs.append("duplicate row_id")
    if errs:
        raise ValueError(f"{path}: " + "; ".join(errs))
    return rows


def score(judgments: List[Dict[str, Any]], results: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    judged = [j for j in judgments if j.get("supports") is not None]
    n = len(judged)
    counts = {s: sum(1 for j in judged if j["supports"] == s) for s in SUPPORTS}
    claims = {(j["case_id"], j["unit"]): j["claim_correct"] for j in judgments if j.get("claim_correct") is not None}
    claim_counts = {c: sum(1 for v in claims.values() if v == c) for c in CLAIM_CORRECT}
    out: Dict[str, Any] = {
        "rows_judged": n, "rows_unjudged": len(judgments) - n, "supports_counts": counts,
        **{name: counts[k] / n if n else None for name, k in
           (("citation_precision", "yes"), ("partial_rate", "partial"), ("contradiction_rate", "contradicts"))},
        "claims_judged": len(claims), "claim_correct_counts": claim_counts,
        "claim_correctness": claim_counts["yes"] / len(claims) if claims else None, "confusion_matrix": None,
    }
    if results is not None:  # human supports x system supported; "missing" = pair not in the results file
        system = {(c["id"], cit.get("chunk_id"), cit.get("marker")): cit.get("supported")
                  for c in results_cases(results) for cit in c.get("citations") or []}
        matrix = {s: {"true": 0, "false": 0, "missing": 0} for s in SUPPORTS}
        for j in judged:
            v = system.get((j["case_id"], j["chunk_id"], j["marker"]))
            matrix[j["supports"]]["missing" if v is None else str(v is True).lower()] += 1
        out["confusion_matrix"] = matrix
    return out


def print_score(m: Dict[str, Any]) -> None:
    pct = lambda v: "n/a" if v is None else f"{v:.2%}"  # noqa: E731
    print(f"Rows judged / unjudged : {m['rows_judged']} / {m['rows_unjudged']}\n"
          f"Citation precision     : {pct(m['citation_precision'])}  (supports=yes / judged)\n"
          f"Partial / contradicts  : {pct(m['partial_rate'])} / {pct(m['contradiction_rate'])}\n"
          f"Claim correctness      : {pct(m['claim_correctness'])} of {m['claims_judged']} claims")
    print("human x system supported:", json.dumps(m["confusion_matrix"]) if m["confusion_matrix"] else "n/a, --results")


def fetch_passages_db(db_url: str) -> Callable[[List[int]], Dict[int, str]]:
    import psycopg  # lazy: only for --db-url

    def fetch(ids: List[int]) -> Dict[int, str]:
        with psycopg.connect(db_url) as conn:
            return dict(conn.execute("SELECT id, content FROM paper_chunks WHERE id = ANY(%s)", (ids,)).fetchall())
    return fetch


def _read_json(path: str) -> Dict[str, Any]:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Blind human review of /v1/ask answers (EVAL-7, report-only).")
    sub = p.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("export", help="Write a blind review sheet + empty judgments file for a results JSON")
    e.add_argument("--results", required=True, help="evals/results/<run>.json (eval_gate or evaluate_answers)")
    e.add_argument("--api-url", default=ea.DEFAULT_API, help="Query service, re-asked for claim text")
    e.add_argument("--db-url", help="Fetch passage text by chunk_id from Postgres instead of the response")
    e.add_argument("--out-dir", default=os.path.join(ea.EVALS_DIR, "results"))
    e.add_argument("--timeout", type=float, default=30.0)
    s = sub.add_parser("score", help="Score a judgments JSONL")
    s.add_argument("--judgments", required=True)
    s.add_argument("--results", help="Results JSON of the same run, for the confusion matrix")
    s.add_argument("--save", help="Write the committable copy (IDs + labels, no notes), e.g. evals/answer_judgments/")
    s.add_argument("--json-out")
    for sp in (e, s):
        sp.add_argument("--force", action="store_true", help="Overwrite an existing judgments file")
    args = p.parse_args(argv)

    if args.cmd == "export":
        run_id = os.path.splitext(os.path.basename(args.results))[0]
        jpath = os.path.join(args.out_dir, f"{run_id}.judgments.jsonl")
        if os.path.exists(jpath) and not args.force:
            print(f"{jpath} exists (a reviewer may have filled it); use --force to overwrite", file=sys.stderr)
            return 2
        questions = {c["id"]: c["question"] for n in CASE_FILES for c in ea.load_cases(os.path.join(ea.EVALS_DIR, n))}
        rows, drift = build_rows(run_id, _read_json(args.results), questions,
                                 ea.make_http_ask(args.api_url, args.timeout),
                                 fetch_passages_db(args.db_url) if args.db_url else None)
        os.makedirs(args.out_dir, exist_ok=True)
        write_sheet(rows, os.path.join(args.out_dir, f"{run_id}.review.md"))
        with open(jpath, "w", encoding="utf-8") as f:
            f.writelines(json.dumps(dict({k: r.get(k) for k in ID_KEYS + LABEL_KEYS}, notes="")) + "\n" for r in rows)
        print(f"{len(rows)} rows -> {jpath}; drifted cases skipped: {', '.join(drift) or 'none'}")
        return 0

    judgments = load_judgments(args.judgments)
    m = score(judgments, _read_json(args.results) if args.results else None)
    print_score(m)
    if args.save:
        run_ids = sorted({j["run_id"] for j in judgments})
        if len(run_ids) != 1:
            raise ValueError(f"--save needs exactly one run_id, got {run_ids}")
        path = os.path.join(args.save, f"{run_ids[0]}.jsonl") if os.path.isdir(args.save) else args.save
        if os.path.exists(path) and not args.force:
            print(f"{path} exists (another reviewer's labels?); use --force to overwrite", file=sys.stderr)
            return 2
        with open(path, "w", encoding="utf-8") as f:
            f.writelines(json.dumps({k: j.get(k) for k in ID_KEYS + LABEL_KEYS}) + "\n" for j in judgments)
        load_judgments(path, committed=True)
        print(f"committable judgments -> {path}")
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(m, f, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

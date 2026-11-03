# Section-extraction baseline (ING-8)

Scorer: `scripts/evaluate_extraction.py`. Rules: [`README.md`](README.md#section-extraction-eval-ing-8).
Detector measured: `services/processor/worker.py` `ProcessorWorker._parse_doc_sections` (production, unmodified).

## Status: not run

**not run: the dataset is not annotated yet, and the eval needs network access.** The ING-8 agent sandbox (2026-11-03)
can't reach arxiv.org (the proxy answers 403 to CONNECT for `arxiv.org` and `export.arxiv.org`) and doesn't have
`pymupdf` (PyPI is unreachable). Without the abs pages and PDFs, nobody can check licences, hand-annotate headings
for `evals/extraction.jsonl`, or run the detector. Writing labels from memory would make the baseline untrustworthy,
so none were written.

To produce the baseline, on a machine with arxiv.org access:
```bash
# 1. add >= 10 hand-annotated, licence-checked rows to evals/extraction.jsonl (>= 3 layouts), then:
python3 -m pytest -q tests/test_evaluate_extraction.py      # dataset test must pass, not skip
python3 scripts/evaluate_extraction.py                      # needs arxiv.org + pymupdf + processor deps
git status --porcelain | grep -i '\.pdf' && echo "PDF LEAK" || echo "no pdf"
```
Then replace this section with the micro and per-paper P/R/F1 table, the commit SHA, the dataset row count and the
results file name.

| Run | Commit | Rows | Micro P | Micro R | Micro F1 | Level acc. |
|---|---|---|---|---|---|---|
| — | — | — | not run | not run | not run | not run |

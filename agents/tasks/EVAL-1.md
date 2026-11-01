# EVAL-1: Versioned golden eval sets + `make eval` quality gate

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P0 (QA-8 cannot be judged without it) |
| **PRD ref** | Product PRD §8 Success metrics |
| **Depends on** | — (runs against whatever corpus is indexed; works best after OPS-H1) |
| **Status** | Ready |

## Goal
The PRD's quality targets (Recall@5 ≥ 0.80, supported ≥ 90%, refusal ≥ 95%) have no versioned dataset behind them.
Make quality measurable and gateable so every retrieval/answering change is judged on numbers, not vibes.

## Scope
- Create `evals/`:
  - `evals/corpus.txt` — list of arXiv IDs the eval assumes are indexed (start with the papers already used in `scripts/evaluate_retrieval.py`).
  - `evals/retrieval.jsonl` — `{"id","query","relevant_arxiv_ids":[...]}`; move the existing hardcoded queries here and grow to ≥ 30.
  - `evals/answer.jsonl` — `{"id","question","expected_arxiv_ids":[...]}` for in-corpus questions (≥ 20).
  - `evals/refusal.jsonl` — `{"id","question"}` clearly out of corpus (≥ 20, e.g. cooking, sports, unrelated science).
  - `evals/thresholds.json` — the Product PRD §8 targets.
  - `evals/README.md` — how to add cases, how to seed the corpus (`scripts/ingest_batch.py` or `/v1/papers/ingest`).
- Refactor `scripts/evaluate_retrieval.py` to read `evals/retrieval.jsonl` (no hardcoded queries).
- Add `scripts/evaluate_answers.py`: calls `/v1/ask` for answer + refusal sets; reports % supported citations,
  % answers citing an expected paper, correct-refusal rate, p50/p95 latency.
- Add `scripts/eval_gate.py`: runs both, writes `evals/results/<timestamp>.json` (git-ignored), prints a table, exits non-zero if any metric is below `thresholds.json`.
- Out of scope: changing retrieval logic or thresholds in `rag.go`. The Makefile `eval` target is backend-owned; request the change in Notes (or it already calls `scripts/eval_gate.py` — check).

## Acceptance criteria
- [ ] AC1 — No eval queries remain hardcoded in `scripts/evaluate_retrieval.py`.
- [ ] AC2 — Datasets meet the minimum sizes above and every `relevant_arxiv_ids`/`expected_arxiv_ids` entry is in `corpus.txt`.
- [ ] AC3 — `python scripts/eval_gate.py --help` works without infra; with the stack up it prints all metrics and exits 0/1 by threshold.
- [ ] AC4 — Baseline numbers for the current templated `/v1/ask` recorded in `evals/BASELINE.md` (or "not run: infra unavailable" plus the exact command for the human).
- [ ] AC5 — `evals/results/` added to `.gitignore` (request in Notes if outside your scope — pm-lead/backend can apply).

## Verification
```bash
make test-unit
python scripts/eval_gate.py --help
make stack-up && python scripts/eval_gate.py   # when infra is available
```

## Notes

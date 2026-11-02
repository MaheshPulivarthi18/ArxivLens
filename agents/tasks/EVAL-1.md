# EVAL-1: Versioned golden eval sets + `make eval` quality gate

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P0 (QA-8 cannot be judged without it) |
| **PRD ref** | Product PRD §8 Success metrics |
| **Depends on** | — (runs against whatever corpus is indexed; works best after OPS-H1) |
| **Status** | In review (rework round 2). Review Blocker 1 is blocked on ADR-0006 (Proposed, PR #3) |

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
- [x] AC1 — No eval queries remain hardcoded in `scripts/evaluate_retrieval.py`. *Evidence: the 4 old queries are now `evals/retrieval.jsonl` r001–r004; the script loads cases via `load_cases()`; `test_retrieval_script_has_no_hardcoded_queries` passes.*
- [x] AC2 — Datasets meet the minimum sizes above and every `relevant_arxiv_ids`/`expected_arxiv_ids` entry is in `corpus.txt`. *Evidence: retrieval 34, answer 22, refusal 22 cases, corpus 31 IDs; `tests/test_evals_data.py` checks sizes, schema, corpus membership, unique IDs (18 passed).*
- [~] AC3 — `python scripts/eval_gate.py --help` works without infra; with the stack up it prints all metrics and exits 0/1 by threshold. *Evidence: `--help` exits 0 with no infra or heavy deps. Threshold logic is unit-tested on fake results (pass, each metric below target, partial run, hybrid not beating baselines). With no stack it exits 2 ("could not run"). **Not verified against a live stack.***
- [x] AC4 — Baseline numbers for the current templated `/v1/ask` recorded in `evals/BASELINE.md` (or "not run: infra unavailable" plus the exact command for the human). *Evidence: `evals/BASELINE.md` says "not run: infra unavailable in agent sandbox" and gives the exact commands.*
- [ ] AC5 — `evals/results/` added to `.gitignore` (request in Notes if outside your scope — pm-lead/backend can apply). *Requested below (`.gitignore` is backend-owned per `scripts/agent_guardrails.sh`).*

## Verification
```bash
make test-unit
python scripts/eval_gate.py --help
make stack-up && python scripts/eval_gate.py   # when infra is available
```

## Notes
- 2026-11-01 (ai-engineer, attempt 1): implemented on `agent/ai/EVAL-1`. Files: `evals/{corpus.txt,retrieval.jsonl,answer.jsonl,refusal.jsonl,thresholds.json,README.md,BASELINE.md}`, `scripts/{eval_gate.py,evaluate_answers.py}`, refactored `scripts/evaluate_retrieval.py` (dataset-driven, lazy heavy imports, returns a summary dict, `--dataset/--json-out`), `tests/test_evals_data.py`, README eval commands. Diff is about 710 changed lines, which is over the 400 target; about 140 of those are data.
- **Unverified (sandbox has no Docker, HF, arXiv, or Go 1.25):** no eval has been run against a live stack, so there are no baseline numbers. `make test-unit` and `make lint` fail at the Go 1.25 toolchain download. `tests/test_m0.py` needs torch, which is not installed. `oxlint` is not installed. The dataset queries have not been checked against real retrieval. The content of 2608.20331 and 2608.20320 was carried over from the old script without checking.
- **Gate semantics:** recall@5 is gated on the "Hybrid RRF + Rerank" mode. Retrieval coverage must be 1.0, so any query whose paper is not indexed fails the gate instead of being silently skipped. Any failed /v1/ask call (error or timeout, in either the answer or refusal set) fails the gate through the `ask_errors == 0` check. Failed calls are also included in p50/p95 at their elapsed time (a timeout counts as at least the `--timeout` value), and they never count as a refusal, hit, or supported citation. If every request fails, the gate exits 2. (Fix round 1, after pm-lead review: previously, failed in-corpus calls changed no gated metric.) Partial runs (`--skip-*`) never pass. Exit codes: 0 pass, 1 fail, 2 could not run.
- **Follow-up request, backend-engineer:** (1) add `evals/results/` to `.gitignore` (AC5). (2) Add `tests/test_evals_data.py` to `make test-py` / `test-unit`. It needs no infra and runs in under 1 s; today `test-py` only runs `tests/test_m0.py`.
- **Follow-up request, pm-lead:** the AGENTS.md §5 gotcha "Retrieval eval queries are hardcoded in `scripts/evaluate_retrieval.py`" is stale once this merges. Product PRD §8 "Measured with ..." could also mention `make eval` / `scripts/eval_gate.py`.
- **Findings for future ai-engineer tasks (not changed here, since they are out of scope):** (a) the eval's "Hybrid RRF + Rerank" mode reranks with a BGE bi-encoder dot product, not the cross-encoder `/internal/rerank` that `/v1/ask` uses. (b) Recall@5 is chunk-level with denominator `min(|relevant chunks|, 5)`, so it behaves like precision@5. It is strict, but pm-lead should confirm the intended definition, and an ADR is needed if it changes. (c) `rag.go` marks a citation `supported=true` (score 0.75) when the embedder call fails, which inflates the supported-citation rate. Cover this in AI-1/QA-8. (d) The PRD target `/v1/search` p95 < 1.5 s is not gated, because EVAL-1 scope did not include it.
- 2026-11-02 (ai-engineer, rework round 2, after the human "Needs work" review on PR #2):
  - **Blocker 1 (Recall@5 definition): still open and BLOCKED on ADR-0006** (Status: Proposed, PR #3). The gated value is chunk-level hits / min(|relevant chunks|, 5), which acts like precision@5 and is not paper-level recall. I did not change the definition, because that would pre-empt an ADR that only the human can accept. Once ADR-0006 is Accepted, implement its definition in `compute_recall_at_k` / `run_retrieval_evaluation` and re-baseline. Until then, the gate's Recall@5 number must not be read as the PRD §8 Recall@5.
  - **Blocker 2 (rerank path): fixed.** Mode "Hybrid RRF + Rerank" now reorders the Hybrid RRF top-10 by `POST <embedder>/internal/rerank`, the production cross-encoder endpoint that `/v1/ask` reaches via `EmbedClient.Rerank`. It no longer uses a local BGE bi-encoder dot product. The embedder URL comes from `--embedder-url` or `ARXIVLENS_EMBEDDER_URL` (default `http://localhost:8081`, the same default as `arxivlens query --embedder-url`). The eval checks `/healthz` first and refuses to run if `"reranker"` is not true, because `api.py` silently falls back to bi-encoder scoring in that state. Any rerank HTTP error or score-count mismatch raises, so the gate exits 2. The summary records `rerank_path`, and the gate has a `rerank via production /internal/rerank` check that also withholds the gated Recall@5 when that value is missing. Tests (HTTP mocked, no infra) assert the exact URL, method, body, and timeout, and assert that the gated ordering follows the endpoint's scores. The misleading `eval_gate.py` comment is fixed.
  - **Major 3 (coverage): fixed.** Coverage is now checked per arXiv ID (`relevant_chunks_for_case` / `missing_arxiv_ids`). A case is skipped unless every relevant ID has chunks, and skipped cases fail the gate through coverage. Missing IDs are printed per case and saved as `missing_arxiv_ids` in the results JSON. Added tests for the two-paper / one-indexed case.
  - **Remaining production/eval mismatch, for the owners to decide (not changed here):** production `HybridSearch` (`internal/query/search.go`, backend-owned) attaches rerank scores but does **not** reorder by them, and `/v1/ask` fetches only `maxCitations` (5) RRF hits before reranking. Production also uses LIMIT 50 per arm plus a `ce.model` filter, while the eval uses 20 and no filter. So the gated mode measures the production *reranker model and endpoint*, not the exact `/v1/ask` ordering. Suggested follow-up for backend-engineer and ai-engineer: either sort by `RerankScore` in production (rerank a larger RRF pool, then truncate) or redefine the gated mode to match. The second option should go through an ADR alongside ADR-0006.
  - Unverified: no live run (no Docker, HF, Go 1.25, or npm in the sandbox). `make test-unit`, `make lint`, and `make eval` were not run.

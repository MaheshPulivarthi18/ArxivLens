# QA report — EVAL-1 · PR #2 · round 1

**Verdict:** PASS
**Reviewed commit:** `dc1fe80fb07f5eb7a029af2ce96f743095dd898d` · **Date:** 2026-11-01
**Ticket:** `agents/tasks/EVAL-1.md` · **PRD:** Product PRD §8 Success metrics (Quality rows and the `/v1/ask` p95 row)
**Evidence pack:** `agents/reviews/EVAL-1/evidence/` (commit 9d34b50)

This verdict comes with conditions for pm-lead. Two ticket outcomes are not delivered on this branch: AC5 (`.gitignore`) and running the new tests in CI. Both need backend-owned files, which the ai-engineer cannot edit, so the developer cannot fix them. I list them as `[spec]` findings that pm-lead needs to turn into a backend ticket. They are not developer rework.

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI | pass ([00](evidence/00-ci-checks.png)) |
| Python M0 unit tests | CI | pass, `tests/test_m0.py` 3 passed. **This job does not run `tests/test_evals_data.py`** ([01](evidence/01-ci-python-job-scope.png), [02](evidence/02-makefile-ci-wiring.png)) |
| Frontend (lint, test, build) | CI | pass ([00](evidence/00-ci-checks.png)) |
| Agent guardrails | CI | pass, role=ai scope OK. Warning: 811 changed lines, over the 400 target ([03](evidence/03-ci-guardrails.png), [04](evidence/04-scope-diffstat.png)) |
| `tests/test_evals_data.py` | local (Python 3.13, pytest 9.1) | **20 passed** in 0.09 s ([09](evidence/09-ac3-unit-tests.png)) |
| `python scripts/eval_gate.py --help` (ticket verification) | local | exit 0, no infra ([07](evidence/07-ac3-gate-help.png)) |
| `eval_gate.py` with no stack | local | exit 2 ("could not run"), no result file written ([08](evidence/08-ac3-gate-no-infra.png)) |
| `make test-unit`, `make lint` | not run locally: the sandbox has no Go 1.25 toolchain, no torch/HF download for `test_m0.py`, and no npm/oxlint. Covered by the green CI jobs above | — |
| `make eval` / `make stack-up && python scripts/eval_gate.py` | not run: infra unavailable (no Docker, no Hugging Face, no arXiv access in the sandbox) | before → after: n/a. This PR adds the gate and has no baseline yet (AC4) |

## Acceptance criteria
| AC | Met? | Evidence |
|---|---|---|
| AC1 — no hardcoded eval queries in `scripts/evaluate_retrieval.py` | ✅ | [05](evidence/05-ac1-no-hardcoded-queries.png): `origin/main` has 4 `"query":`/`target_arxiv` literals at lines 64–77. HEAD has no matches. The script loads `evals/retrieval.jsonl` through `load_cases()`, and the 4 old queries are r001–r004 verbatim. Guarded by `test_retrieval_script_has_no_hardcoded_queries`. Mutation "re-hardcode a query" is caught ([13](evidence/13-test-quality-mutations.png)). |
| AC2 — dataset minimum sizes, every referenced ID in `corpus.txt` | ✅ | [06](evidence/06-ac2-dataset-sizes.png), an independent check script: retrieval 34/30, answer 22/20, refusal 22/20. Corpus has 31 well-formed, unique IDs. The schema is exact, no referenced ID is missing from the corpus, case IDs are unique across all 78 cases, and every corpus ID is used. |
| AC3 — `--help` works without infra; with the stack up, it prints all metrics and exits 0/1 by threshold | ⚠ partly unverified | `--help` ✅ [07](evidence/07-ac3-gate-help.png). Fail-closed without a stack: exit 2 and no result file ([08](evidence/08-ac3-gate-no-infra.png)). 20 unit tests cover the threshold logic ([09](evidence/09-ac3-unit-tests.png)), and 10 of 11 semantic mutations are caught ([13](evidence/13-test-quality-mutations.png)). **The live-stack run (real metrics, exit 0/1) is not verified because infra is unavailable.** The ticket lists that command as "when infra is available". |
| AC4 — baseline in `evals/BASELINE.md` or "not run: infra unavailable" plus the exact commands | ✅ (the allowed "not run" form) | [11](evidence/11-ac4-baseline.png): "not run: infra unavailable in agent sandbox", with the exact commands and a metric/target table. It claims no numbers. |
| AC5 — `evals/results/` in `.gitignore` (or request in Notes if outside scope) | ⚠ not delivered on this branch; the ticket's allowed "request" path was taken | [12](evidence/12-ac5-gitignore.png): `git check-ignore evals/results/x.json` returns not ignored, and this PR does not change `.gitignore`. `.gitignore` is backend-only in the guardrail allow-list. The request is in the ticket Notes, and the checkbox is accurately left unticked. See finding 2. |

## PRD fit
Yes. `evals/thresholds.json` matches Product PRD §8 exactly: Recall@5 ≥ 0.80, supported ≥ 0.90, correct refusal ≥ 0.95, `/v1/ask` p95 < 3000 ms, and hybrid+rerank beats both single-mode baselines on MRR and NDCG@5 ([10](evidence/10-thresholds-vs-prd.png)). The gate is fail-closed. A missing metric fails, retrieval coverage must be 1.0, a partial `--skip-*` run never passes, any `/v1/ask` error fails, and "could not run" exits 2. A refusal is counted only when there are zero citations on a successful response, which matches `rag.go:87-92`. The gate does not cover `/v1/search` p95 or ingest < 60 s, which were outside the ticket's scope (finding 6).

## Findings
1. [spec] **`tests/test_evals_data.py` does not run in CI or in `make test-unit`.** The Makefile `test-py` target runs only `tests/test_m0.py`, and `ci.yml` only calls `make test-py` ([01](evidence/01-ci-python-job-scope.png), [02](evidence/02-makefile-ci-wiring.png)). If someone weakens the gate or a dataset in a later PR, CI stays green. The Makefile and `.github/` are backend-owned, and the developer requested this change in Notes. OPS-H4 (KAN-16) is listed in `agents/jira.md`, but there is no `agents/tasks/OPS-H4.md`, so I could not confirm that it covers this. **pm-lead: confirm OPS-H4 covers this, or create a backend ticket for it.** The tests need only stdlib and pytest and run in about 0.1 s.
2. [spec] **AC5 is not delivered.** Until `evals/results/` is git-ignored, `make eval` output can be committed by accident. This is a one-line backend change, and the same ticket as finding 1 can cover it.
3. [nit] The comment at `scripts/eval_gate.py:23` (`# the configuration /v1/ask actually uses`) is inaccurate. The eval's "rerank" is a BGE bi-encoder dot product (`evaluate_retrieval.py:189`, carried over from `main`). `/v1/ask` uses the cross-encoder `/internal/rerank` (`internal/query/client.go:93`) ([14](evidence/14-rerank-mode-mismatch.png)). The gated Recall@5 therefore does not measure the production ranker.
4. [nit] The diff has 811 changed lines, about twice the 400 target ([03](evidence/03-ci-guardrails.png)). About 140 lines are data, and the rest is one cohesive feature. I do not recommend splitting it now.
5. [spec] The Recall@5 definition is chunk-level with denominator `min(|relevant chunks|, 5)` (`evaluate_retrieval.py:45-50`), so it behaves like precision@5. pm-lead should confirm the intended definition before the first baseline is recorded. A change after that needs an ADR.
6. [spec] PRD §8 rows that are not gated: `/v1/search` p95 < 1.5 s and ingest < 60 s. PRD §8 "Measured with …" and AGENTS.md §5 ("Retrieval eval queries are hardcoded…") will be stale after this merge.
7. [spec] Outside this PR: `rag.go:151-154` marks a citation `supported=true` (score 0.75) when the embedder call fails. That can inflate `supported_citation_rate` and let the gate pass in a degraded stack. It should be covered in AI-1 or QA-8.
8. [nit] Nobody has checked the content of `2608.20331` and `2608.20320` (r001–r004, carried over from the old script) against arXiv. Because coverage must be 1.0, the gate fails if either cannot be ingested. Verify both at the first live baseline.

## Missing tests
- The exact-threshold boundary for the `>=` checks (recall = 0.80, supported = 0.90 and refusal = 0.95 should pass). Changing `>=` to `>` currently survives ([13](evidence/13-test-quality-mutations.png)).
- `main()` end to end with a stubbed `ask` and stubbed retrieval: exit 1 on a failed check, 0 on all-pass, and the results JSON written to `--results-dir`.
- `evaluate()` with zero citations across the answer set (`supported_citation_rate` should be 0.0 and the gate should fail).
- A malformed or blank JSONL line, and an unknown key.
- `make_http_ask` with an HTTP 500 and with a response body that is not JSON.

## Hand-off
PASS → pm-lead final review, with these conditions: (a) confirm or create the backend ticket for findings 1 and 2; (b) decide the Recall@5 definition (finding 5) before the first live baseline is recorded.

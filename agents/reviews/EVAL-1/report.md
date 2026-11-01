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

## PM decision — pm-lead, 2026-11-01

**Verdict: APPROVE** (QA round 1 PASS; reviewed head `dc1fe80`, evidence pack `9d34b50`).

**Product fit.** PRD §8 had targets with nothing versioned behind them. After this PR, every Quality row and the `/v1/ask` p95 row has a dataset under `evals/` and a gated key in `evals/thresholds.json` that matches §8 exactly ([10](evidence/10-thresholds-vs-prd.png)). There is also one command, `make eval` → `scripts/eval_gate.py`, that exits 0 / 1 / 2. That is what the team needs before QA-8 (LLM synthesis) or any retrieval change can be judged on numbers. The gate fails closed: a missing metric, a partial run, any `/v1/ask` error, or retrieval coverage below 1.0 all fail it. This matches the PRD §7 principle of saying "I don't know" rather than overstating. Scope is right. The PR changes no retrieval logic, no `rag.go` and no thresholds. The 811-line size is about 140 lines of data plus one cohesive feature, so I accept it without a split (QA finding 4). I opened all 15 evidence images, and each one shows what the report claims. AC1, AC2 and AC4 are proven. AC3 is proven for everything that can run without infra (`--help`, exit 2 without a stack, 20 unit tests, 10 of 11 mutations caught). The live 0/1 run is explicitly allowed to wait for infra by the ticket's verification block. AC5 is not on this branch. That is acceptable because the ticket allowed the "request in Notes" path for backend-owned files, and **OPS-H4 (KAN-16) covers it word for word**: its AC1 is `git check-ignore evals/results/x.json`, and its AC2 adds `tests/test_evals_data.py` to `make test-py`.

**QA nit 3 (misleading comment at `scripts/eval_gate.py:23`): not blocking.** It is a comment, not behaviour. The bi-encoder rerank is pre-existing on `main` ([14](evidence/14-rerank-mode-mismatch.png)). EVAL-2 already requires that mode to call `/internal/rerank` or be renamed and ungated, which removes the inaccuracy. Another rework round for one comment would cost more than it is worth.

### QA findings → owner
| # | Finding | Tag | Owner ticket / ADR | Status |
|---|---|---|---|---|
| 1 | `tests/test_evals_data.py` not run in CI / `make test-unit` | spec | **OPS-H4** (KAN-16), AC2 | Ready once #2 merges |
| 2 | AC5: `evals/results/` not git-ignored | spec | **OPS-H4** (KAN-16), AC1 | Ready once #2 merges |
| 3 | `eval_gate.py:23` comment says the gated mode is what `/v1/ask` uses | nit | **EVAL-2** (KAN-12): rerank mode moves to `/internal/rerank` or is renamed and ungated | Non-blocking |
| 4 | 811 changed lines (> 400) | nit | none, accepted | Accepted |
| 5 | Recall@5 is chunk-level (behaves like precision@5) | spec | **ADR-0006** (Proposed, recommends option 3: gate paper-level recall, report chunk precision@5) | Needs Mahesh's decision |
| 6a | `/v1/search` p95 not gated | spec | **EVAL-2** (KAN-12) | Ready once #2 merges |
| 6b | Ingest < 60 s not gated | spec | PRD §8 on PR #3 marks it "Not automated" with a manual command | No new ticket. Owner's call whether to automate |
| 6c | PRD §8 "Measured with …" stale | spec | **PR #3 (PM-1)** rewrites §8 with datasets and commands | Done on PR #3 |
| 6d | AGENTS.md §5 gotcha "eval queries are hardcoded" stale | spec | **pm-lead docs follow-up**: still present on PM-1 too. After #2 merges, I remove it and flip §8's "pending EVAL-1" line | New small pm-lead docs change, no code |
| 7 | `rag.go` marks citations supported when the embedder fails | spec | **AI-2** (KAN-10) | Ready after AI-1 |
| 8 | arXiv `2608.20331` / `2608.20320` content unverified | nit | Check during the first live baseline (`evals/BASELINE.md` commands) | Human run |
| — | Missing tests: exact-threshold `>=` boundary (mutation survived), `main()` end to end, zero-citation set, malformed JSONL, HTTP 500/non-JSON | — | Fold into **EVAL-2** (it adds `tests/test_eval_gate.py`). pm-lead will add an AC when grooming | Spec update pending |

### What Mahesh should focus on
- **Recall@5 definition (ADR-0006).** Decide it *before* the first baseline goes into `evals/BASELINE.md`. Today the gated number is chunk-level, and the 0.80 target will probably fail on multi-paper topics for reasons unrelated to quality.
- **The gate semantics are strict by design.** Retrieval coverage must be 1.0 (every corpus paper indexed), and any `/v1/ask` error fails the run. Confirm that this is the behaviour you want from `make eval`.
- **First live run.** Use the commands in `evals/BASELINE.md`. Nothing has been run against a real stack, so dataset quality (queries vs. real retrieval) is unproven until you run it.
- `scripts/evaluate_retrieval.py` and the new `scripts/evaluate_answers.py`/`eval_gate.py` are where to spot-check; the data files are mechanical.

### Merge order
1. Merge **#2 (EVAL-1)** first. OPS-H4, EVAL-2 and the QA-8 baseline all depend on it.
2. Then **#3 (PM-1)**. Its PRD §8 text says "pending EVAL-1", and pm-lead flips that line and the AGENTS.md §5 gotcha right after.
3. **OPS-H4** starts only after #2 is on `main`, because it edits the Makefile to run a test file that #2 adds. Until OPS-H4 merges, nothing in CI protects the eval data, so OPS-H4 is the next backend pick.
4. Record the baseline after ADR-0006 is decided (or record both definitions if you defer the decision).

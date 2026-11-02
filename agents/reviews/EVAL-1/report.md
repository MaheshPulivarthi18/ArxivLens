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

---

# QA report — EVAL-1 · PR #2 · round 2

**Verdict:** FAIL
**Reviewed commit:** `f750bb909141f3e67b7c2d1930158a3b3c11bb7d` · **Date:** 2026-11-02
**Ticket:** `agents/tasks/EVAL-1.md` · **PRD:** Product PRD §8 Success metrics (Quality rows and the `/v1/ask` p95 row)
**Trigger:** the human owner left a "Needs work" review on `5001cce` with Blocker 1, Blocker 2 and Major 3. Commit `f750bb9` is the developer's response.

**Why FAIL:** Blocker 1 from the human review is not resolved. The gate still uses chunk-level Recall@5 (which acts like precision@5) as the PRD §8 Recall@5. This can only be fixed after ADR-0006 is Accepted, and only the human can accept it. The developer was right not to work around it. Blocker 2 is fixed in the way the human asked: the gate now calls the production `/internal/rerank` endpoint, and a test proves it. However, `/v1/ask` does not reorder results by rerank score. So the gated Recall@5 still does not measure what `/v1/ask` returns, which was the human's concern. Major 3 is resolved. CI is green.

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI | ✅ success ([15](evidence/15-r2-ci-checks.png)) |
| Python M0 unit tests | CI | ✅ success ([15](evidence/15-r2-ci-checks.png)). It still runs only `tests/test_m0.py`, so CI does not run `tests/test_evals_data.py` (OPS-H4) |
| Frontend (lint, test, build) | CI | ✅ success ([15](evidence/15-r2-ci-checks.png)) |
| Agent guardrails | CI + local | ✅ success. Local run: role=ai OK, with a size warning ([23](evidence/23-r2-guardrails.png)) |
| `pytest tests/test_evals_data.py` | local | ✅ 33 passed in 0.05 s ([16](evidence/16-r2-unit-tests.png)). Round 1 had 20 tests |
| Mutation checks on round-2 code | local, temp copy | 7 of 9 caught. The 2 survivors are listed below ([21](evidence/21-r2-mutations.png)) |
| `python scripts/eval_gate.py --help` / no-stack run | local | ✅ exit 0 / exit 2 "could not run" ([24](evidence/24-r2-acs-at-head.png)) |
| `make test-unit`, `make lint` | not run locally | No Go 1.25 or npm in the sandbox. CI's Go and Frontend jobs cover them |
| `make eval` | not run locally | No Docker, Postgres or Hugging Face in the sandbox. There is no baseline yet, so there are no before/after numbers |

## Human review items (`5001cce` "Needs work")
| Item | Status at `f750bb9` | Evidence |
|---|---|---|
| **Blocker 1**: the gated Recall@5 is chunk-level `hits / min(\|relevant chunks\|, 5)`, which acts like precision@5. The definition needs an Accepted ADR | ❌ **Not resolved. Blocked on ADR-0006, which is a human decision.** `compute_recall_at_k` is unchanged, the caller still expands each paper to all of its chunks, and `eval_gate.py:67` still gates this value as `recall@5`. ADR-0006 is `Status: Proposed`, exists only on the unmerged `agent/pm/PM-1` branch (PR #3), and is not on `main`. The ticket Status says it is blocked on ADR-0006. There are no multi-chunk or multi-paper tests that tell recall and precision apart: changing the denominator to `len(relevant)` passes all 33 tests | [17](evidence/17-r2-blocker1-recall-definition.png), [21](evidence/21-r2-mutations.png) |
| **Blocker 2**: the gated rerank is a BGE bi-encoder dot product, not the production cross-encoder | ⚠ **The requested fix is done. The underlying concern is only partly resolved.** Mode 4 now sends `POST <embedder>/internal/rerank`, the same endpoint as Go `EmbedClient.Rerank` (`client.go:93`). The local `np.dot` rerank is gone. `/healthz` must report `"reranker": true`, so the API's bi-encoder fallback (`api.py:106-109`) is refused. HTTP errors and score-count mismatches raise. The gate ignores the mode unless `rerank_path` is the production value. Tests mock the HTTP call and check URL, method, body, timeout and the reordering, and 6 related mutations are caught. **What remains:** `/v1/ask` asks `HybridSearch` for only `maxCitations` (5) RRF hits, and `search.go:127-139` attaches rerank scores but **never sorts by them**. `rag.go` uses the score only to drop hits. The eval instead reorders the RRF **top-10** by rerank score, with a per-arm pool of 20 and no `ce.model` filter, while production uses 50 and filters on `ce.model`. The gated Recall@5 can therefore still pass or fail independently of what `/v1/ask` returns. The developer disclosed this in the ticket Notes. See finding 2 | [18](evidence/18-r2-blocker2-rerank-endpoint.png), [19](evidence/19-r2-blocker2-prod-ordering.png), [21](evidence/21-r2-mutations.png) |
| **Major 3**: coverage counted a case as covered if any one of its relevant papers was indexed | ✅ **Resolved.** `relevant_chunks_for_case` and `missing_arxiv_ids` check coverage per arXiv ID. A case is skipped unless every relevant ID has chunks. Missing IDs are printed and saved under `missing_arxiv_ids`, and coverage below 1.0 fails the gate. Three tests cover the case where only one of two papers is indexed, and both related mutations are caught | [20](evidence/20-r2-major3-coverage.png), [21](evidence/21-r2-mutations.png) |
| Should-fix: no "Fixes #N" / "Closes #N" in the PR description | ⚠ Not addressed. The project tracks work in Jira (KAN-8 is linked in the PR body), not in GitHub issues. See finding 6 | n/a (PR body) |

## Acceptance criteria (re-checked at `f750bb9`)
| AC | Met? | Evidence |
|---|---|---|
| AC1: no hardcoded queries in `evaluate_retrieval.py` | ✅ | [24](evidence/24-r2-acs-at-head.png): the AC1/AC2 tests pass, including `test_retrieval_script_has_no_hardcoded_queries`. The refactor did not add any queries back ([16](evidence/16-r2-unit-tests.png)) |
| AC2: dataset sizes and corpus membership | ✅ | [24](evidence/24-r2-acs-at-head.png): retrieval 34, answer 22, refusal 22. The data files are unchanged in `f750bb9` ([22](evidence/22-r2-scope-diffstat.png)) |
| AC3: `--help` works without infra; with a stack, prints all metrics and exits 0/1 | ⚠ partly verified (same as round 1) | [24](evidence/24-r2-acs-at-head.png): `--help` exits 0 and shows the new `--embedder-url`. With no stack it exits 2. Threshold and production-rerank logic are unit-tested ([16](evidence/16-r2-unit-tests.png)). Not run against a live stack |
| AC4: baseline recorded, or "not run" plus the commands | ✅ | [24](evidence/24-r2-acs-at-head.png): `evals/BASELINE.md` says "not run: infra unavailable" |
| AC5: `evals/results/` git-ignored, or requested in Notes | ⚠ requested, not delivered (same as round 1) | [24](evidence/24-r2-acs-at-head.png): `evals/results/x.json` is not ignored. This is assigned to OPS-H4 (KAN-16) |

## PRD fit
The PRD §8 Quality target "Recall@5 ≥ 0.80" is still gated with a chunk-level number that is not paper-level recall. Until ADR-0006 is decided and implemented, a PASS or FAIL from `make eval` on that row does not show whether the PRD target is met. The rest of the round-2 changes make the gate measure the production reranker model, and coverage is now honestly fail-closed.

## Findings
1. [blocking] **Human Blocker 1 is unresolved and needs a human decision on ADR-0006** (Proposed, PR #3). `scripts/evaluate_retrieval.py:111-116` still divides by `min(|relevant chunks|, 5)`, and `eval_gate.py:67` gates that value as PRD Recall@5 ([17](evidence/17-r2-blocker1-recall-definition.png)). The developer cannot fix this without pre-empting the ADR. **Next step: Mahesh accepts or rejects ADR-0006. The ai-engineer then implements the accepted definition** (normally: dedupe the ranked chunks to arXiv IDs and compute recall against `relevant_arxiv_ids`, with chunk precision@5 reported separately) **and adds multi-chunk and multi-paper tests that tell recall and precision apart**. Today a denominator mutation passes all tests ([21](evidence/21-r2-mutations.png)).
2. [blocking] **The gated mode still does not match what `/v1/ask` returns** (the remaining part of human Blocker 2). The eval reorders the RRF top-10 by cross-encoder score. `/v1/ask` (`rag.go:66`) takes the RRF top-5, attaches rerank scores without sorting (`search.go:127-139`), and uses them only as a filter (`rag.go:75,80`). The pools also differ: 20 per arm with no `ce.model` filter in the eval, and 50 per arm with the filter in production ([19](evidence/19-r2-blocker2-prod-ordering.png)). The human's fix option ("call the same rerank client/endpoint") is implemented, but the comparison is still not of the production retrieval path. **Owner decision needed (pm-lead, then human):** either (a) production sorts by `RerankScore` over a larger RRF pool before truncating (backend-engineer owns `search.go`; ai-engineer owns `rag.go`), or (b) the gated mode is redefined to match `/v1/ask` exactly, for example RRF top-5 with the 50-per-arm pool and `ce.model` filter, plus the `rag.go` filter. The developer suggests putting (b) in an ADR together with ADR-0006. If the human considers the endpoint fix enough for this PR, pm-lead should record that and open the follow-up ticket.
3. [nit] **The gate's `rerank via production /internal/rerank` check is self-reported.** `run_retrieval_evaluation` always writes `rerank_path = PRODUCTION_RERANK_PATH` (`evaluate_retrieval.py:265`). The real protection is the `/healthz` and HTTP calls before that line, and nothing tests that `run_retrieval_evaluation` calls them. Deleting the `require_cross_encoder(...)` call passes all 33 tests ([21](evidence/21-r2-mutations.png)). Add a test that stubs `psycopg` and the embedder and asserts the health check is called and that a failed health check means no results are returned.
4. [nit] **Coverage is checked against `paper_chunks`, not `chunk_embeddings`** (`COVERAGE_SQL`, `evaluate_retrieval.py:130`). A paper that has been chunked but not embedded, or embedded with a different `ce.model`, counts as covered even though dense and RRF search cannot return it. This could confuse the first live run.
5. [nit] Size: `f750bb9` alone has 481 changed lines (355+, 126−), and the whole PR has about 1,250 non-evidence lines ([22](evidence/22-r2-scope-diffstat.png)). That is over the 400-line target. Most of it is review rework the human asked for, so I do not recommend a split.
6. [nit] **The PR description is out of date.** It still says the gated mode "uses a BGE bi-encoder … EVAL-2 fixes it" and links that to EVAL-2. The description should be updated for round 2, and the "Fixes #N" should-fix should be answered (KAN-8 is in Jira, and there is no GitHub issue).
7. [spec] **EVAL-2 (KAN-12) overlap.** Round 2 has already done EVAL-2's "move the rerank mode to `/internal/rerank`" item. pm-lead should update EVAL-2's scope so the work is not done twice. The production-ordering question in finding 2 should get its own ticket or ADR.
8. [spec] Not changed since round 1: AC5 and running `tests/test_evals_data.py` in CI are still OPS-H4 (KAN-16). The new tests have the same CI gap: CI ran 4 checks and none of them ran the 33 eval tests ([15](evidence/15-r2-ci-checks.png)).

## Missing tests
- Recall@5 on a paper-level labelled case where one relevant paper has at least 5 chunks, plus a two-paper case. This is needed to tell recall from precision (Blocker 1, after ADR-0006).
- `run_retrieval_evaluation` with `psycopg`, the embedder and `urlopen` stubbed. It should assert the `/healthz` check runs before any rerank call, that a failed health check gives no summary, and that `missing_arxiv_ids` ends up in the summary for a partially indexed case.
- `rerank_candidates` with tied scores (the docstring says ties keep RRF order, but no test checks it) and with an empty candidate list (no HTTP call).
- Still open from round 1, assigned to EVAL-2: the exact-threshold `>=` boundary, `main()` end to end, malformed JSONL, and an HTTP 500 from `/v1/ask`.

## Hand-off
FAIL → back to ai-engineer. Finding 1 cannot be worked until **Mahesh decides ADR-0006** (PR #3), so the task stays `Blocked (needs ADR-0006)`. For finding 2, pm-lead needs to decide, with the human, between "production sorts by rerank" (a backend and ai change) and "gate what `/v1/ask` actually does" (an ADR), or record that the endpoint fix is enough for this PR. Major 3 is done.

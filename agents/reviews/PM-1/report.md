# QA report — PM-1 · PR #3 · round 1

**Verdict:** FAIL
**Reviewed commit:** `c697b292651e7ead916dd0afd8db2b3e65a7588c` · **Date:** 2026-11-01
**Ticket:** `agents/tasks/PM-1.md` · **PRD:** Product PRD §6, §8, §11; Technical PRD §9, §11, §12
**Evidence pack:** `agents/reviews/PM-1/evidence/`. The check scripts that produced it are in `agents/reviews/PM-1/checks/`, so anyone can re-run them.

All three ACs are met, and CI is green. The verdict is FAIL because of one blocking truthfulness defect. The PR's job is to make the PRD status columns match the code. QA-4 is still marked ✅ with no note, but this PR's own AI-2 spec says the code breaks QA-4 (finding 1). The fix is a one-cell edit.

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI | pass ([00](evidence/00-ci-checks.png)) |
| Python M0 unit tests | CI | pass ([00](evidence/00-ci-checks.png)) |
| Frontend (lint, test, build) | CI | pass ([00](evidence/00-ci-checks.png)) |
| Agent guardrails | CI + local | pass, role=pm. Warning: 514 changed lines, over the 400 target ([01](evidence/01-scope-guardrails.png), [02](evidence/02-ci-guardrails-log.png)) |
| Ticket verification (`git diff --stat origin/main`) | local | only `docs/` and `agents/` ([01](evidence/01-scope-guardrails.png), [03](evidence/03-ac3-no-source-code.png)) |
| `make test-unit`, `make lint` | not run locally: the sandbox has no Go 1.25 toolchain (it tries to download one). No code changed, and CI covers both | — |
| `make eval` | not applicable: docs-only PR, no retrieval or answering change | n/a |

## Acceptance criteria
| AC | Met? | Evidence |
|---|---|---|
| AC1 — every ⏳ row has an acceptance statement or a linked task ID | ✅ | [04](evidence/04-ac1-pending-rows.png): all 5 non-✅ rows in Product PRD §6 (ING-6, QA-8, QA-9, UI-5 ◐, OPS-6) carry an acceptance statement, and every linked task file exists. ING-6 and QA-8 summarise the ACs in their linked specs correctly. QA-9, OPS-6 and saved questions in UI-5 are labelled *Proposed acceptance (needs human approval)*. Technical PRD §11 has a Task column, and its 5 linked specs exist. |
| AC2 — every open question links an ADR | ✅ | [05](evidence/05-ac2-open-questions-adrs.png): Product §11 Q1–Q5 → 0001/0003/0005/0007/0006, Technical §12 Q1–Q5 → 0002/0004/0008/0009/0006. All 10 links resolve, each ADR title matches its question, and every linked ADR is `Status: Proposed`. The 4 new ADRs (0006–0009) follow the template and leave the Decision section blank (`_(Filled by the human.)_`). No ADR in the repo is Accepted. |
| AC3 — no source code changed (docs/ and agents/ only) | ✅ | [03](evidence/03-ac3-no-source-code.png): no changed file outside `docs/` and `agents/`. [01](evidence/01-scope-guardrails.png): the 12 files match the ticket's AC3 list exactly. Guardrails are OK for role=pm. |

### Scope items beyond the ACs (checked for truthfulness)
- **§6 ✅/⏳ vs code** ([06](evidence/06-status-claims-vs-code.png)): every ✅ row I spot-checked has the code behind it (crawler 3 s rate limit, sha256, `/v1/papers/ingest`, `DiscoverAndIngest`, `UNIQUE (arxiv_id, version)`, RRF, `/internal/rerank` cross-encoder, `SupportThreshold`, early refusal, appendix/supplementary penalty, category filter, `maxRetries: 3`, `--dry-run`/`--error-type`, `processed_events`, traceparent, UI components). The ⏳ rows have no code behind them: no scheduler or watermark, no streaming, no LLM client, no Prometheus. UI-5 ◐ is accurate: per-citation BibTeX exists in `ReferencesStack.jsx`, and there are no saved questions and no bulk `.bib` export. **Exception: QA-4. See finding 1.**
- **§8 datasets and commands** ([09](evidence/09-eval-keys-vs-pr2.png)): all 5 gate keys cited match `evals/thresholds.json` on PR #2, and so does the gated mode name `"Hybrid RRF + Rerank"`. The claim "until it merges, `make eval` falls back to `scripts/evaluate_retrieval.py`" matches the Makefile `eval` target. `tests/test_m4_chaos.py::test_idempotency_duplicate_event_row_invariance` exists ([08](evidence/08-prd-links-dangling.png)). The bi-encoder caveat on MRR/NDCG is accurate (PR #2 `evaluate_retrieval.py:188-190`).
- **Technical PRD §5 HNSW gap** ([11](evidence/11-new-spec-claims.png)): confirmed. `grep -rni hnsw migrations/` is empty, `benchmark_hnsw.py:32-35` creates `chunk_embeddings_hnsw_idx` with `m=16, ef_construction=64`, and `migrate` takes one `--file` (default `000001_init_schema.up.sql`).
- **Technical PRD §9 reference hardware:** clearly labelled *Proposed (pm-lead, needs human approval)*, and the PRD states that existing gates apply unchanged until it is approved. That is acceptable for a PM proposal.
- **New task specs** ([10](evidence/10-new-spec-structure.png), [11](evidence/11-new-spec-claims.png)): OPS-H4, OPS-H5, AI-2, AI-3 and EVAL-2 all have Owner, Priority, PRD ref, Depends on, Status, Goal, Scope, Out of scope, 4–5 checkable ACs, Verification and Notes. Every file named in a Scope is inside its owner's paths (AGENTS.md §1). The factual claims I checked are true: rag.go:152-153 has the fallback, rag.go:120/146 make one `EmbedTexts` call per hit and per sentence, the M5/M6 test name exists, and the PR #2 rerank mode is a bi-encoder.

## PRD fit
Mostly yes. The PRDs now say how every unfinished requirement will be accepted, every open question has an ADR, and §8 names a dataset and a command for each metric, or says plainly that a metric is not measured. That is what PM-1 asked for. The one exception is QA-4, which still says "done" for a requirement this PR itself documents as violated on the embedder-failure path.

## Findings
1. [blocking] **QA-4 is marked ✅ with no note, but the code does not verify every citation, and this PR's own AI-2 spec says so.** `internal/query/rag.go:151-153` sets `score = 0.75`, `supported = true` whenever the sentence or chunk embedding is unavailable. That is an unverified citation labelled *supported*. QA-4 reads "Each citation is verified … and labeled supported/unsupported". AI-2's Goal (added in this PR) says "QA-4 requires every citation to be *verified*. An unverified citation labelled supported breaks the core trust promise." The QA-4 row's Acceptance/task cell is "—", and AI-2 is linked only from a §8 caveat ([07](evidence/07-qa4-fallback.png)). This ticket exists so that "PRDs match the code", and the scope says to verify every ✅. **Fix:** mark QA-4 `◐ partial` (verified on the happy path, fails open when the embedder is unavailable) and link [AI-2](../agents/tasks/AI-2.md) in its Acceptance/task cell. No other change is needed.
2. [nit] **Merge order: 7 paths in the PRDs dangle until PR #2 (EVAL-1) merges.** `evals/{retrieval,answer,refusal}.jsonl`, `evals/corpus.txt`, `evals/thresholds.json`, `scripts/eval_gate.py` and `scripts/evaluate_answers.py` do not exist on this branch or on `main`. All 7 are added by PR #2 ([08](evidence/08-prd-links-dangling.png)). §8 and Technical PRD §10 both say "pending EVAL-1 (PR #2)", so a reader is warned, and none of these are clickable markdown links. If #3 merged first, though, the §8 Dataset column would point at files that are missing, and so would the "Thresholds are mirrored in `evals/thresholds.json`" sentence. **Merge #2 before #3.** The PM decision on PR #2 already says this. The board update on `agent/pm/board-2026-11-01` also links the 5 new specs, which exist only on this branch, so merge that after #3 too.
3. [nit] 514 changed lines, over the 400 target ([02](evidence/02-ci-guardrails-log.png)). About 325 of them are new files: 5 task specs (198 lines) and 4 ADRs (127 lines). Splitting now would cost more than it saves. I do not recommend a split.
4. [spec] AI-3 AC4 says "Technical PRD §11 row status updated in the PR", but §11 has no status column, only Item/Notes/Task. Say what "updated" means, for example "mark the row ✅ in Notes" or "remove the row".
5. [nit] AI-2's Notes say AI-1 "explicitly excludes behaviour changes". AI-1 actually allows a bug fix of 20 lines or fewer (`agents/tasks/AI-1.md:25`), and the AI-2 fix is about 3 lines. A separate task is still the right call, because fail-closed is a product decision and not a bug fix. The rationale should say that.
6. [spec] For Mahesh, not a defect: Technical PRD §9 reference hardware (Apple M-series, 16 GB, MPS) and the proposed acceptance statements for QA-9, OPS-6 and saved questions in UI-5 are pm-lead proposals that need human approval. The new ADRs 0006–0009 also need decisions. ADR-0006 (the Recall@5 definition) should be decided before the first EVAL-1 baseline is recorded.

## Missing tests
- None applicable. This is a docs-only change. The `checks/` scripts in this pack can be re-run against a later round to re-verify links, ADR statuses and status claims.

## Hand-off
FAIL → back to pm-lead for finding 1, which is a one-cell PRD edit. Findings 2–6 are non-blocking. After the fix, a round-2 check needs only `checks/qa4.sh` and `checks/ac1.py` re-run.

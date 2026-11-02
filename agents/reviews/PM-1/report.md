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

---

# QA report — PM-1 · PR #3 · round 2

**Verdict:** PASS
**Reviewed commit:** `f4cd9abc03e985c59308fd07c12b43a075050860` · **Date:** 2026-11-02
**Ticket:** `agents/tasks/PM-1.md` · **PRD:** Product PRD §6, §8, §11; Technical PRD §9, §11, §12
**Evidence (round 2):** `agents/reviews/PM-1/evidence/12-r2-*` to `22-r2-*`, rendered with `render_evidence.py` from real output on the head commit. The round-1 `checks/` scripts were re-run unchanged.

The blocking round-1 finding is fixed. QA-4 is now `◐ partial`. Its row describes the fail-open path accurately and links AI-2. Findings 4 and 5 are fixed too. The fix commit `f4cd9ab` changes 5 files (+5/−3) and stays inside the PM's paths. All 3 ACs still hold on head, and all 4 CI checks are green.

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI | pass ([12](evidence/12-r2-ci-checks.png)) |
| Python M0 unit tests | CI | pass ([12](evidence/12-r2-ci-checks.png)) |
| Frontend (lint, test, build) | CI | pass ([12](evidence/12-r2-ci-checks.png)) |
| Agent guardrails | CI + local | pass, role=pm ([13](evidence/13-r2-ci-guardrails-log.png), [18](evidence/18-r2-ac3-guardrails.png)). The 1125-line warning counts the QA pack. The PM-owned diff (`docs/`, `agents/tasks/`) is +443/−73. |
| Ticket verification (`git diff --stat origin/main`) | local | only `docs/` and `agents/` ([19](evidence/19-r2-ac3-changed-files.png)) |
| `make test-unit`, `make lint` | not run locally: the sandbox has no Go 1.25 toolchain. No code changed, and CI covers both | — |
| `make eval` | not applicable: docs-only PR | n/a |

## Round-1 findings: status
| # | Round-1 finding | Status at `f4cd9ab` | Evidence |
|---|---|---|---|
| 1 | [blocking] QA-4 ✅ while `rag.go:151-153` fails open | **Resolved.** The status is now `◐ partial`. The Acceptance/task cell says "fails open … labelled supported with score 0.75 without being verified" and links [AI-2](../agents/tasks/AI-2.md), which exists. The description matches the code: the `else` branch at `rag.go:151-154` covers both a sentence-embed failure and a missing chunk embedding. Technical PRD §7.2 adds a matching "Known gap" note that links AI-2. | [14](evidence/14-r2-fix-commit.png), [15](evidence/15-r2-qa4.png), [16](evidence/16-r2-ac1-pending-rows.png) |
| 2 | [nit] 7 PRD paths dangle until PR #2 merges | **Still open (merge order).** PR #2 is still open and unmerged. The same 7 paths are absent on this branch and are added by PR #2. **Merge #2 before #3.** | [22](evidence/22-r2-prd-links.png) |
| 3 | [nit] Diff over 400 lines | Unchanged. No split recommended. | [13](evidence/13-r2-ci-guardrails-log.png) |
| 4 | [spec] AI-3 AC4 "row status updated" with no status column | **Resolved.** AC4 now requires the Notes cell to start with `✅ Done (AI-3, PR #<n>):` and gives a grep check. The §11 header is `Priority \| Item \| Notes \| Task`, so the instruction can be carried out. | [21](evidence/21-r2-ai3-ac4.png) |
| 5 | [nit] AI-2 rationale misstated AI-1's scope | **Resolved.** The Notes now say that AI-1 allows bug fixes of 20 lines or fewer, and that AI-2 is separate because failing closed is a product-behaviour decision. | [14](evidence/14-r2-fix-commit.png) |
| 6 | [spec] Human decisions pending (§9 hardware, proposed acceptances, ADRs 0006–0009) | Unchanged. This is for Mahesh and is not a defect. | — |

## Acceptance criteria (re-checked at head)
| AC | Met? | Evidence |
|---|---|---|
| AC1 — every ⏳ row has an acceptance statement or a linked task ID | ✅ | [16](evidence/16-r2-ac1-pending-rows.png): 6 rows in Product PRD §6 are not ✅ (ING-6, QA-4 ◐, QA-8, QA-9, UI-5 ◐, OPS-6). Every one is OK, and the count of rows without an acceptance/task is 0. All linked specs exist. Every Task link in the Technical PRD §11 roadmap resolves. |
| AC2 — every open question links an ADR | ✅ | [17](evidence/17-r2-ac2-open-questions-adrs.png): Product §11 Q1–Q5 and Technical §12 Q1–Q5 each link an existing ADR with a matching title, and every one is `Status: Proposed`. The new ADRs 0006–0009 have a blank Decision section. No ADR is Accepted. |
| AC3 — no source code changed (docs/ and agents/ only) | ✅ | [19](evidence/19-r2-ac3-changed-files.png): every changed file is under `docs/` or `agents/`. [18](evidence/18-r2-ac3-guardrails.png): local guardrails pass for role=pm. |

The ✅/⏳ claims vs the code were re-run ([20](evidence/20-r2-status-claims-vs-code.png)). Every ✅ row still has code behind it, UI-5 ◐ still holds, and no ⏳ row has code behind it. The only status that changed is QA-4, as covered in finding 1.

## PRD fit
Yes. The status columns now match the code, including the fail-open path in QA-4. Every unfinished row says how it will be accepted. Every open question has a Proposed ADR. §8 names a dataset and a command for each metric.

## Findings (new in round 2)
1. [nit] `agents/tasks/PM-1.md` AC1 evidence text still lists five non-✅ rows and leaves out QA-4 (now ◐ → AI-2). The PRD itself is correct, so this is stale ticket prose. pm-lead can update it when closing the ticket.
2. [spec] AI-2 has no AC that brings the PRD back in line once the fix lands. AI-3 has one (AC4). Without it, the PRD will drift again. Suggested AC5 for AI-2: "In the same PR, Product PRD QA-4 goes back to ✅ and the Technical PRD §7.2 'Known gap' note is removed." AGENTS.md DoD item 4 covers this in general terms, so this is not blocking.
3. [nit] (carried over from round 1, finding 2) **Merge order: #2 before #3.** After that, merge the board update branch that links these specs.

## Missing tests
- None applicable (docs-only).

## Hand-off
PASS → pm-lead final review, then a human merges, after PR #2. The new findings are non-blocking, and finding 2 is a spec suggestion for pm-lead.

---

# PM decision — PM-1 · PR #3

**Verdict:** APPROVE (ready for human merge, **after PR #2**)
**Reviewer:** pm-lead · **Reviewed head:** `830fe69b30772c1b7777ef7d753318251d383d94` · **Date:** 2026-11-02

**Conflict of interest:** pm-lead wrote PM-1, so this is a self-review. It comes after an independent QA FAIL → PASS, and I judged it to the same bar as any engineer's PR. I approve only because each AC has evidence that QA produced independently, I re-ran it, and I read the PRD text myself. A human merge is still required.

**Head vs reviewed commit:** QA round 2 reviewed `f4cd9ab`. Head `830fe69` adds only `agents/reviews/PM-1/**` (23 files, +442, no other path; `git diff --name-only f4cd9ab 830fe69`). CI on `830fe69`: Go, Python M0, Frontend and Agent guardrails all `completed / success`.

## Acceptance criteria
| AC | Verdict | Why |
|---|---|---|
| AC1 — every ⏳ row has acceptance or a task | Met | 6 non-✅ rows (ING-6, QA-4 ◐, QA-8, QA-9, UI-5 ◐, OPS-6), and the number missing acceptance is 0 ([16](evidence/16-r2-ac1-pending-rows.png)). All linked specs exist. QA-9, OPS-6 and saved questions are labelled *Proposed acceptance (needs human approval)*, which is right because they are not in the PRD yet. |
| AC2 — every open question links an ADR | Met | 10/10 links resolve, titles match, all `Proposed`. ADRs 0006–0009 have a blank Decision section, and no ADR is Accepted ([17](evidence/17-r2-ac2-open-questions-adrs.png); re-checked with `grep -rn 'Status: Accepted' docs/adr/` → none). |
| AC3 — docs/ and agents/ only | Met | [19](evidence/19-r2-ac3-changed-files.png), [18](evidence/18-r2-ac3-guardrails.png), CI guardrails ([13](evidence/13-r2-ci-guardrails-log.png)). |

## Product fit (read directly, not only via QA)
- §6 statuses now tell the truth about the code, including QA-4's fail-open path. That is the core purpose of this ticket, and the round-1 blocking defect was exactly a status that wasn't true.
- §8: each metric names a dataset, a command and a gate key, or says plainly "not automated / not gated yet". The keys match PR #2's `thresholds.json` ([09](evidence/09-eval-keys-vs-pr2.png)).
- Technical PRD §9 reference hardware is clearly a *Proposed* pm-lead choice. The text says existing gates apply unchanged until Mahesh decides. No requirement was invented without that label.
- AI-2 (fail closed) is `Ready` without an ADR. I accept that: QA-4 already requires every citation to be *verified*, so "unverified ⇒ not supported" enforces an existing requirement rather than making a new product choice. Adding a third state is explicitly out of scope and routed to ADR-0003.
- Scope: 514 PM-authored lines, over the 400 target. About 325 of them are new specs and ADRs, which this ticket asked for. I don't think a split would help. No scope creep beyond the ticket's Scope list.

## Evidence files checked (all opened: .png with the image viewer, .txt read in full)
| File | What I checked |
|---|---|
| 00 ci-checks | Round-1 head `c697b29`: 4/4 checks success. |
| 01 scope-guardrails | 12 files, all docs/ or agents/tasks/. 514 lines. Guardrails OK, role=pm. |
| 02 ci-guardrails-log | The CI job log agrees with the local run (warning at 514 lines, OK). |
| 03 ac3-no-source-code | `grep -vE '^(docs/|agents/)'` on the round-1 diff is empty. |
| 04 ac1-pending-rows | Round 1: 5 non-✅ rows OK. QA-4 is still ✅, which is what made round 1 fail. |
| 05 ac2-open-questions-adrs | Round 1: 10 links → Proposed ADRs. |
| 06 status-claims-vs-code | Every ✅ row has grep hits in the right files. UI-5's per-citation BibTeX is found. The ⏳ features are absent. |
| 07 qa4-fallback | `rag.go:151-153` sets `supported = true` and `score = 0.75` in the else branch. This confirms the round-1 blocking finding was real. |
| 08 prd-links-dangling | Round 1: 7 paths exist only on PR #2. |
| 09 eval-keys-vs-pr2 | All 5 gate keys and `GATED_MODE` match PR #2. The Makefile fallback matches what the §8 prose claims. |
| 10 new-spec-structure | 5 new specs have every template field, 4–5 ACs each, and dependencies that resolve. |
| 11 new-spec-claims | The HNSW index is absent from migrations, `migrate` runs a single file, the M5/M6 test exists, the rag.go line refs are right, the eval rerank is a bi-encoder, and file ownership matches AGENTS.md §1. |
| 12 r2-ci-checks | Round-2 head `f4cd9ab`: 4/4 success. |
| 13 r2-ci-guardrails-log | OK for role=pm. The 1125-line count includes the QA pack. |
| 14 r2-fix-commit | 5 files, +5/−3: QA-4 → ◐ with AI-2, §7.2 "Known gap", AI-3 AC4, AI-2 Notes. Small and focused. |
| 15 r2-qa4 | The QA-4 row now matches `rag.go`, and AI-2 is linked from §6, §8 and Technical §7.2. |
| 16 r2-ac1-pending-rows | 6 non-✅ rows, 0 missing acceptance. |
| 17 r2-ac2-open-questions-adrs | Same as round 1. Still no Accepted ADR. |
| 18 r2-ac3-guardrails | Local guardrails pass for role=pm. |
| 19 r2-ac3-changed-files | Every path is under docs/ or agents/. |
| 20 r2-status-claims-vs-code | The re-run is the same as 06. Only QA-4's status changed. |
| 21 r2-ai3-ac4 | AC4 can now be done: the §11 header is `Priority \| Item \| Notes \| Task`. |
| 22 r2-prd-links | The 7 PR #2 paths are still absent → merge order #2 before #3. |

Evidence-tooling nit (not a PR defect): in the PNGs, ✅ and ⏳ render as empty boxes because the font is missing (04, 06, 07, 14, 16, 20). The .txt files print the Unicode names, so nothing is ambiguous. `render_evidence.py` should use a font that has these glyphs.

## Non-blocking follow-ups (next PM docs pass; not fixed here to avoid another QA cycle)
1. `agents/tasks/PM-1.md` AC1 evidence prose: add QA-4 (◐ → AI-2) to the list of non-✅ rows. (QA r2 finding 1)
2. `agents/tasks/AI-2.md`: add AC5, "In the same PR, Product PRD QA-4 returns to ✅ and the Technical PRD §7.2 'Known gap' note is removed". It must land before AI-2 starts, and AI-2 cannot start until AI-1 merges. Until then, AGENTS.md DoD item 4 covers it. (QA r2 finding 2)
3. Product PRD §6 legend says "verified against the code 2026-11-01". QA-4 was re-verified on 2026-11-02. Update the date in the same pass.
4. After PR #2 merges: remove the AGENTS.md §5 "hardcoded eval queries" gotcha, the matching ai-engineer lesson, and the "pending EVAL-1" notes in Product §8 and Technical §10. (Already in the PM-1 Notes.)

## For Mahesh (human decisions; none block the merge)
- Decide ADR-0006 (Recall@5 definition; ideally before the first EVAL-1 baseline), ADR-0007, ADR-0008 and ADR-0009.
- Approve or replace the Technical PRD §9 *Proposed* reference hardware, and confirm your dev machine matches the primary profile.
- Approve or replace the *Proposed acceptance* statements for QA-9, OPS-6 and saved questions in UI-5.
- **Merge order: #2 (EVAL-1) before #3.** Merge the board-update branch after #3.

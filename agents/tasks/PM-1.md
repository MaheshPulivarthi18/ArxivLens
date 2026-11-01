# PM-1: Reconcile PRD status columns + acceptance criteria for all ⏳ rows

| | |
|---|---|
| **Owner** | pm-lead |
| **Priority** | P1 |
| **PRD ref** | Product PRD §6, §8, §11; Technical PRD §9, §11, §12 |
| **Depends on** | — |
| **Status** | In review |

## Goal
The PRDs are the source of truth agents plan from, so they must match the code and be specific enough to test.

## Scope
- Verify each ✅/⏳ in Product PRD §6 against the code (grep). Known mismatch: UI-5 — per-citation BibTeX copy exists; mark ◐ partial with a note.
- Add an "Acceptance" column (or a linked task spec) for every ⏳ row.
- Technical PRD §9: replace "laptop-class machine" with a concrete reference spec (CPU, RAM, GPU/MPS or none) so latency targets are reproducible.
- Product PRD §8: point each quality metric at its dataset in `evals/` (after EVAL-1) and the command that measures it.
- Product PRD §11 / Technical PRD §12 open questions: link each to its ADR (0001–0005).
- Spec the Backlog items in `agents/board.md` that are now unblocked.

## Acceptance criteria
- [x] AC1 — Every ⏳ row has an acceptance statement or a linked task ID. Evidence: Product PRD §6 has a new "Acceptance / task" column. ING-6 → ING-6.md, QA-8 → QA-8.md, UI-5 (now ◐ partial) → FE-2.md + Proposed acceptance for saved questions, QA-9 and OPS-6 → Proposed acceptance statements (backlog). Technical PRD §11 roadmap has a new Task column.
- [x] AC2 — Every open question links an ADR. Evidence: Product PRD §11 Q1→0001, Q2→0003, Q3→0005, Q4→0007 (new), Q5→0006 (new). Technical PRD §12 Q1→0002, Q2→0004, Q3→0008 (new), Q4→0009 (new), Q5→0006. All new ADRs are `Status: Proposed`.
- [x] AC3 — No source code changed (docs/ and agents/ only). Evidence: `git status --short` / `git diff --stat origin/main` list only `docs/PRD-Product.md`, `docs/PRD-Technical.md`, `docs/adr/0006..0009`, `agents/tasks/{PM-1,OPS-H4,OPS-H5,AI-2,AI-3,EVAL-2}.md`.

## Verification
```bash
git diff --stat origin/main   # only docs/ and agents/
```

## Notes
- 2026-11-01 (pm-lead, attempt 1):
  - **§6 verification (grep):** every ✅ row matches the code (discover in `internal/query/ingester.go`; appendix/supplementary/table penalty in `rag.go`; `--dry-run`/`--error-type` in `cmd/arxivlens/main.go`; category filter in `server.go`). ⏳ rows confirmed absent: no scheduler (ING-6), no streaming (QA-9), no Prometheus/metrics (OPS-6). UI-5 changed to ◐ partial (per-citation BibTeX in `ReferencesStack.jsx`; no saved questions; FE-2 = export all).
  - **§8:** every metric now names its `evals/` dataset and command (`make eval` → `scripts/eval_gate.py`), marked pending EVAL-1 (PR #2). Metrics with no measurement (on-demand ingest < 60 s, stuck papers, click-through) say so explicitly.
  - **Technical PRD §9 reference spec (PM choice, needs confirmation):** primary = Apple silicon M-series (M1 Pro+), 16 GB, MPS; CPU-only = x86-64 8 vCPU / 16 GB, informational. **Mahesh should confirm his actual machine** matches the primary profile (README says the workers run on the host for MPS, so Apple silicon is assumed).
  - **New finding:** no migration creates the HNSW index (only `scripts/benchmark_hnsw.py` does), and `arxivlens migrate` runs a single file. Documented in Technical PRD §5 and specced as OPS-H5.
  - **New ADRs (Proposed):** 0006 Recall@5 definition (recommend paper-level gated + chunk precision@5 diagnostic), 0007 multi-user/hosted v2 scope, 0008 HNSW params at scale, 0009 long-context embedding model.
  - **New specs:** OPS-H4, OPS-H5, AI-2, AI-3, EVAL-2. AI-2 is separate from AI-1 because AI-1 excludes behaviour changes.
  - **Follow-up after PR #2 merges:** remove the AGENTS.md §5 gotcha "Retrieval eval queries are hardcoded in `scripts/evaluate_retrieval.py`", the matching ai-engineer lesson, and the "pending EVAL-1" notes in Product PRD §8 and Technical PRD §10. AGENTS.md was left unchanged here, because the gotcha is still true on main.
  - Not specced (blocked or not yet cheap): QA-9 (after QA-8), OTLP export, Prometheus/OPS-6, auth (scope depends on ADR-0007), on-demand ingest via Kafka (ADR-0004).
- 2026-11-01 (pm-lead, fix round 1, after independent review):
  1. Technical PRD §9 "Reference hardware" and its gating rule are now labelled *Proposed (pm-lead, needs human approval)*. Until approved, existing gates apply unchanged on any machine. The Product PRD §8 references to it are marked (Proposed).
  2. EVAL-2 now gates `/v1/search` p95 unconditionally and records the machine profile in the results JSON. Profile-aware gating is deferred until Mahesh decides on §9. The spec says `search_p95_latency_ms_max` is a new gate key, taken from the existing PRD target, and needs Mahesh's confirmation in the PR (new AC4).
  3. EVAL-2 now names `tests/test_eval_gate.py` and adds its pytest command to Verification.
  4. OPS-H5 has a soft dependency on OPS-H1 for AC5, or the workers can be started by hand (AGENTS.md §5).
  The PR #2 follow-up note (remove the AGENTS.md §5 gotcha, the ai-engineer lesson, and the "pending EVAL-1" notes) was already recorded above.
- Round 1 QA FAIL → fixed QA-4 status, AI-3 AC4, AI-2 Notes

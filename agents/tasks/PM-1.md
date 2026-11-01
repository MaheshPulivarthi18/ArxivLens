# PM-1: Reconcile PRD status columns + acceptance criteria for all ⏳ rows

| | |
|---|---|
| **Owner** | pm-lead |
| **Priority** | P1 |
| **PRD ref** | Product PRD §6, §8, §11; Technical PRD §9, §11, §12 |
| **Depends on** | — |
| **Status** | Ready |

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
- [ ] AC1 — Every ⏳ row has an acceptance statement or a linked task ID.
- [ ] AC2 — Every open question links an ADR.
- [ ] AC3 — No source code changed (docs/ and agents/ only).

## Verification
```bash
git diff --stat origin/main   # only docs/ and agents/
```

## Notes

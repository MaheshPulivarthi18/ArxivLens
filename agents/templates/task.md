# <TASK-ID>: <short imperative title>

| | |
|---|---|
| **Owner** | ai-engineer / backend-engineer / frontend-engineer / pm-lead |
| **Priority** | P0 / P1 / P2 |
| **PRD ref** | e.g. QA-8, Technical PRD §11 |
| **Depends on** | task IDs and/or ADR numbers (must be Done / Accepted) |
| **Jira** | `KAN-n` (find with JQL `labels = "task-<ID>"`) |
| **Design required** | yes / no — yes if P0, new CLI/table/service, or a shared contract changes |
| **Status** | Ready / Design / In progress / In QA / PM review / Ready to merge / Blocked (reason) / Done |

## Goal
One or two sentences: the user-visible or system outcome, and why it matters.

## Scope
- Files/directories expected to change:
- Out of scope:

## Acceptance criteria
- [ ] AC1 — observable, testable statement
- [ ] AC2 —
- [ ] Docs: PRD status / README updated if behaviour changed

## Verification
```bash
make test-unit
make lint
# task-specific commands
```

## Notes
(Owner appends: decisions made, things tried, blockers with exact errors, follow-up task requests.)

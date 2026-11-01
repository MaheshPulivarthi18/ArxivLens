# agents/ — Team memory

Agents forget everything between sessions. This folder is what they remember. Every file here is plain Markdown,
reviewed in PRs like code, so the team's memory is versioned, diffable, and correctable by a human.

| File | Purpose | Written by | Read |
|---|---|---|---|
| `board.md` | Kanban: Ready / In progress / In review / Blocked / Done | orchestrator, pm-lead | every session |
| `tasks/<ID>.md` | One spec per task: scope, acceptance criteria, verification, notes | pm-lead (spec), owner (notes) | by the owner |
| `PROGRESS.md` | Append-only log, newest first, one entry per task per cycle | orchestrator | last 20 entries |
| `memory/lessons.md` | Short, deduplicated rules learned from friction, per role | `/retro` | every session |
| `memory/archive.md` | Retired lessons | `/retro` | rarely |
| `metrics.md` | One row per sprint cycle: throughput, first-pass rate, blocks | orchestrator | `/retro` |
| `templates/` | Task spec and PR templates | humans | pm-lead, orchestrator |

Decisions live in `docs/adr/`. Only the human owner moves an ADR to `Accepted`.

## How the loop runs

```
            ┌─────────────── /sprint (scheduled) ───────────────┐
 memory ──► sync PRs/ADRs ──► pm-lead plans ──► owner builds ──► harness verifies
   ▲                                                               │
   │                          pm-lead reviews ◄────────────────────┘
   │                                │ APPROVE
   │                                ▼
   └── /retro lessons ◄── record ◄── open PR ──► human merges / comments / accepts ADRs
```

## Human touchpoints (the only ones)
1. Merge or comment on agent PRs.
2. Accept or reject ADRs in `docs/adr/`.
3. Approve `retro:` PRs that change agent instructions.

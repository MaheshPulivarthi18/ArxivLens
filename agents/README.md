# agents/ — Team memory

Agents forget everything between sessions. This folder is what they remember. Every file here is plain Markdown,
reviewed in PRs like code, so the team's memory is versioned, diffable, and correctable by a human.

| File | Purpose | Written by | Read |
|---|---|---|---|
| `board.md` | Kanban: Ready / In progress / In QA / PM review / Ready to merge / Blocked / Done | orchestrator, pm-lead | every session |
| `tasks/<ID>.md` | One spec per task: scope, acceptance criteria, verification, notes | pm-lead (spec), owner (notes) | by the owner |
| `PROGRESS.md` | Append-only log, newest first, one entry per task per cycle | orchestrator | last 20 entries |
| `memory/lessons.md` | Short, deduplicated rules learned from friction, per role | `/retro` | every session |
| `memory/archive.md` | Retired lessons | `/retro` | rarely |
| `metrics.md` | One row per sprint cycle: throughput, first-pass rate, blocks | orchestrator | `/retro` |
| `reviews/<ID>.md` | QA report(s) + PM verdict per task, also posted on the PR | qa-reviewer, pm-lead | pm-lead, developer on rework |
| `templates/` | Task spec, PR and QA report templates | humans | pm-lead, qa-reviewer, orchestrator |

Decisions live in `docs/adr/`. Only the human owner moves an ADR to `Accepted`.

## How the loop runs

```
                    ┌──────────────── /sprint (scheduled) ────────────────┐
 memory ──► sync PRs/ADRs ──► pm-lead specs ──► developer builds ──► PR + CI
   ▲                                                                   │
   │                       FAIL / CHANGES ◄── qa-reviewer (PASS?) ◄─────┘
   │                       (back to developer)        │ PASS
   │                                                  ▼
   │                                       pm-lead final review
   │                                                  │ APPROVE
   │                                                  ▼
   └── /retro lessons ◄── record ◄── Ready to merge ──► human merges
```

## Human touchpoints (the only ones)
1. Merge PRs that reached **Ready to merge** (QA PASS + PM APPROVE), or comment to send them back.
2. Accept or reject ADRs in `docs/adr/`.
3. Approve `retro:` PRs that change agent instructions.

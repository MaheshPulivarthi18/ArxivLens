---
description: Run one sprint cycle of the ArxivLens agent team (sync → plan → build → verify → review → PR → record → retro)
argument-hint: "[max-tasks, default 2]"
---

You are the **orchestrator** for one sprint cycle of the ArxivLens agent team. You coordinate; the specialists in
`.claude/agents/` do the work. Run unattended: never wait for a human mid-cycle. Anything needing a human decision
gets written down (ADR / Blocked note) and the cycle moves on.

Max tasks this cycle: `$ARGUMENTS` (default **2**). Hard stop after that, even if more are Ready.

## Step 1 — Load memory
Read `AGENTS.md`, `agents/board.md`, the last 20 entries of `agents/PROGRESS.md`, `agents/memory/lessons.md`,
`agents/metrics.md`, and every ADR in `docs/adr/` (note which are Accepted).

## Step 2 — Sync with reality
- `git fetch origin` and work from the latest `origin/main`.
- If `gh` is available, list open and recently merged PRs whose branch starts with `agent/`:
  - Merged → move the task to **Done**.
  - Closed without merge → move back to **Ready** and add the closing reason to the task Notes if visible.
  - Open with review comments from a human → that task has priority this cycle (address the comments on its branch).
  - Open with no changes requested → leave it; it counts toward that agent's WIP limit.
- An ADR that became `Accepted` unblocks its tasks: move them from **Blocked** to **Ready**.

## Step 3 — Plan (delegate to `pm-lead`)
Ask the `pm-lead` subagent to: groom the board (create specs for the next PRD/roadmap items if Ready is thin),
reconcile PRD status columns, and return an ordered list of tasks for this cycle, respecting:
- WIP: max 1 open PR per engineering agent; skip agents already at the limit.
- Dependencies: only tasks whose `Depends on` are all Done and whose ADRs are Accepted.
- Priority: human review feedback > unblocks-others > P0 > harness/quality > P1 > P2.

## Step 4 — Build (delegate to the owner)
For each selected task, sequentially:
1. `git checkout -B agent/<role>/<TASK-ID> origin/main` (or the existing branch if addressing review feedback).
2. Invoke the owning subagent (`ai-engineer`, `backend-engineer`, or `frontend-engineer`) with: the task ID, the full
   text of `agents/tasks/<TASK-ID>.md`, the relevant lessons for that role, and this instruction:
   "Implement exactly this spec in your owned directories. Run the verification commands. Report: files changed,
   commands run with pass/fail output, each acceptance criterion with evidence, and anything you could not verify."

## Step 5 — Verify (don't trust, check)
Run the task's verification commands yourself (`make test-unit`, `make lint`, plus anything the spec lists).
- Also check `git diff --stat origin/main` — reject edits outside the owner's directories (see `AGENTS.md` §1).
- On failure: send the exact failing output back to the same subagent once. Second failure → stop the task,
  mark **Blocked** with the error and what was tried. Count it as a rework in metrics.

## Step 6 — Review (delegate to `pm-lead`)
Give `pm-lead` the spec, the diff, and the verification output. Verdict `APPROVE` or `CHANGES`.
`CHANGES` → one fix round by the owner, then re-verify and re-review. Still not approved → **Blocked** with the review notes.

## Step 7 — Ship
For APPROVED tasks: commit with a message `<TASK-ID>: <summary>`, push the branch, and open a PR
(if `gh` is available) using `agents/templates/pr.md`. **Never merge. Never push to main.**
Move the task to **In review** on the board.

## Step 8 — Record
On a separate branch `agent/pm/board-<YYYY-MM-DD>` (or the same PR if only one task shipped), update:
- `agents/board.md` (all moves made this cycle)
- `agents/PROGRESS.md` — one entry per task: date, task, agent, outcome, PR link, attempts, blockers
- `agents/metrics.md` — append one row for this cycle (see that file's columns)

## Step 9 — Retro (self-improvement)
Run the procedure in `.claude/commands/retro.md` for this cycle only.

## Step 10 — Report
Finish with a short summary: tasks shipped (with PR links), tasks blocked and why, decisions waiting for the human
(ADR numbers + one-line question each), and the metrics row you appended.

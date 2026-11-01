---
description: Retrospective - turn this cycle's friction into lessons and proposed process improvements
---

Run a retrospective on the most recent sprint cycle (or the range given in `$ARGUMENTS`). Delegate the analysis to
`pm-lead`, then apply its output.

## Inputs
`agents/PROGRESS.md` (recent entries), `agents/metrics.md`, task Notes of tasks touched this cycle, PR review comments
if `gh` is available, and `agents/memory/lessons.md`.

## Procedure
1. **Find friction.** For each task: did it need a retry? a fix round? get Blocked? take more than one cycle?
   Was the cause a vague spec, missing harness, unclear ownership, flaky test, missing context, or a real technical problem?
2. **Write lessons.** Append to the matching role section in `agents/memory/lessons.md`, format:
   `- [YYYY-MM-DD][TASK-ID] <what happened> → <rule for next time>` (one line, concrete, actionable).
   If an existing lesson already covers it, increment its `(seen N×)` counter instead of adding a duplicate.
3. **Promote recurring lessons.** Any lesson `seen ≥ 2×` is a process bug. Propose the fix as a concrete edit to
   the right file (`AGENTS.md`, an area `AGENTS.md`, an agent definition in `.claude/agents/`, the task template,
   or the Makefile/CI via a new backend task). Put prompt/process edits on a branch `agent/pm/retro-<YYYY-MM-DD>`
   and open a PR titled `retro: <summary>` — a human approves process changes, agents never self-merge them.
4. **Prune.** Lessons not relevant for 30 days or superseded by a promoted rule move to `agents/memory/archive.md`.
   Keep `lessons.md` under ~60 lines so it stays cheap to load every session.
5. **Metrics trend.** Compare the last 5 rows of `agents/metrics.md`. If first-pass rate is falling or blocked count
   is rising, name the top cause in the PROGRESS entry.

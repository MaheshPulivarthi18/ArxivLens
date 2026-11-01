---
description: Run one sprint cycle of the ArxivLens agent team (sync → plan → build → PR → QA → PM review → ready to merge → record → retro)
argument-hint: "[max-new-tasks, default 2]"
---

You are the **orchestrator** for one sprint cycle of the ArxivLens agent team. You coordinate; the specialists in
`.claude/agents/` do the work. Run unattended: never wait for a human mid-cycle. Anything needing a human decision
gets written down (ADR / Blocked note) and the cycle moves on.

Max **new** tasks started this cycle: `$ARGUMENTS` (default **2**). Reviewing PRs already open does not count toward it.

## The workflow every task follows

```
Ready ─► [Design ─► Design approved] ─► Build ─► In QA ─► PM review ─► Ready to merge ─► Done
(PM spec)  (developer)  (pm-lead)      (dev)  (qa-reviewer) (pm-lead)               (human merges)
                                         ▲        │ FAIL        │ CHANGES
                                         └────────┴─────────────┘  (max 2 rework rounds, then Blocked)
```
The Design steps apply only to tasks marked `design-required` (see `docs/design/README.md`).
Every stage change is mirrored to Jira following `agents/jira.md` (status = stage, plus a role-tagged comment).

Roles never review their own work: the developer builds, **qa-reviewer** verifies against ticket + PRD + test results,
**pm-lead** gives the final product sign-off, the **human** merges.

## Step 1 — Load memory
Read `agents/jira.md` and, if the Atlassian tools are available, the Jira board:
`searchJiraIssuesUsingJql` with `project = KAN AND labels = arxivlens AND statusCategory != Done ORDER BY rank`.
Then read `AGENTS.md`, `agents/board.md`, the last 20 entries of `agents/PROGRESS.md`, `agents/memory/lessons.md`,
`agents/metrics.md`, and every ADR in `docs/adr/` (note which are Accepted).
If the board doesn't yet have the columns **In QA**, **PM review**, **Ready to merge**, add them now (replacing
"In review": PRs with no QA report go to In QA).

## Step 2 — Sync with reality
`git fetch origin`; work from the latest `origin/main`. List PRs with REST (GraphQL may be blocked):
`gh api 'repos/MaheshPulivarthi18/ArxivLens/pulls?state=all&per_page=50'`. For branches starting with `agent/`:
- Merged → **Done**.
- Closed without merge → back to **Ready**, closing reason in the task Notes if visible.
- Open with human comments since the last agent commit → highest priority: the owner addresses them (Step 4), then QA again.
- Open, new commits since the last QA report (or no report yet) → queue for **QA** (Step 5).
- Open, QA PASS but no PM verdict → queue for **PM review** (Step 6).
- An ADR that became `Accepted` unblocks its tasks: **Blocked → Ready**.
- Jira: merged PRs → transition the story to **Done**. New intake tickets and human (untagged) comments are handled
  as `agents/jira.md` describes — human comments get the same top priority as human PR comments.

## Step 3 — Plan (delegate to `pm-lead`)
Ask `pm-lead` to groom the board (spec next PRD/roadmap items if Ready is thin; fix any `[spec]` findings from QA
reports) and return an ordered list of new tasks for this cycle, respecting:
- WIP: max 1 open PR per developer agent; skip agents at the limit.
- Dependencies: only tasks whose `Depends on` are Done and whose ADRs are Accepted.
- Priority: human feedback > QA/PM rework > unblocks-others > P0 > harness/quality > P1 > P2.

## Step 4 — Design (only `design-required` tasks) → Build → open PR
For each selected new task and each rework item, sequentially:
0. **Design gate.** If the ticket (or its Jira story) is `design-required` and there is no approved
   `docs/design/<TASK-ID>.md` yet: the owner writes it from `agents/templates/design.md` as the first commit on the
   branch (Jira: **Design**). Then `pm-lead` reviews it against the ticket and PRD and fills
   "PM decision". Approved → Jira **In Progress**, continue to build in this cycle. Changes requested → one
   revision; still not approved → Blocked with the PM's notes. No product code before approval.
1. `git checkout -B agent/<role>/<TASK-ID> origin/main` (or the existing PR branch for rework).
2. Invoke the owner (`ai-engineer`, `backend-engineer`, `frontend-engineer`, or `pm-lead` for docs tasks) with: the
   full ticket `agents/tasks/<TASK-ID>.md`, the PRD rows it references, the role's lessons, and — for rework — the
   latest QA report / PM verdict / human comments. Instruction: "Implement exactly this spec in your owned directories.
   Run the verification commands you can. Under `agents/` touch only your task spec's Notes — the board,
   PROGRESS and metrics are updated in Step 8 on the record branch. Report files changed, commands run with output, each AC with evidence,
   and anything you could not verify and why."
3. Sanity-check `git diff --stat origin/main` and run `scripts/agent_guardrails.sh origin/main agent/<role>/<TASK-ID>`.
   Fix ownership violations before pushing.
4. Commit `<TASK-ID>: <summary>`, push, and open the PR (new tasks) with `agents/templates/pr.md`, or push to the
   existing PR (rework). **Never merge. Never push to main.** Board: **In QA**.
   Jira: **In QA**; set `**PR:** #n` in the story description; comment `[<role>] PR #n opened → <link>`.

## Step 5 — QA (delegate to `qa-reviewer`, fresh context)
For each PR queued for QA, invoke `qa-reviewer` with only: PR number, branch, head SHA, the ticket path, and the PRD
rows, and the approved design doc if one exists. Do **not** pass the developer's self-report — QA judges from the spec,
the PRD, the design, the diff and the evidence.
- CI not finished → wait for it if the cycle has time (poll check runs every ~60 s, up to 15 min), else `PENDING`.
- QA commits its report to `agents/reviews/<TASK-ID>.md` on the PR branch and posts it as a PR comment.
- **PASS** → Step 6. **FAIL** → one rework round by the owner in this cycle (Step 4), then QA again.
  Second FAIL → **Blocked** with the report linked. **PENDING** → stays In QA for next cycle.
- Jira: comment `[qa-reviewer] PASS|FAIL|PENDING — <one-line reason> → <review file link>`; PASS → **PM review**;
  FAIL → back to **In Progress**.

## Step 6 — PM review (delegate to `pm-lead`)
Give `pm-lead` the ticket, the PRD rows, the diff summary, and the QA report. pm-lead checks product fit, scope, docs,
and that QA's evidence covers every AC. Verdict appended to `agents/reviews/<TASK-ID>.md` and posted as a PR comment:
- **APPROVE** → board **Ready to merge**; add the PR label `ready-to-merge` if the API allows it
  (`gh api repos/MaheshPulivarthi18/ArxivLens/issues/<pr>/labels --method POST -f 'labels[]=ready-to-merge'`).
- **CHANGES** → rework round (Step 4) → QA → PM again. Counted toward the 2-round limit.
- Jira: comment `[pm-lead] APPROVE|CHANGES — …`; APPROVE → **Ready to merge**; CHANGES → **In Progress**.

## Step 7 — Merge gate (human)
Agents never merge. The human merges PRs labelled/listed as **Ready to merge**. Next cycle's Step 2 moves them to Done.

## Step 8 — Record
On a branch `agent/pm/board-<YYYY-MM-DD>` (one PR per cycle), update:
- `agents/board.md` — every move made this cycle
- `agents/PROGRESS.md` — one entry per task: outcome, PR, attempts, QA rounds, PM rounds, blockers
- Jira — confirm every story touched this cycle matches the repo (status, PR link, labels); fix drift.
- `agents/metrics.md` — one row for this cycle; count QA FAILs and PM CHANGES in "Fix rounds", and note the QA pass
  rate (first-round PASS / reviewed) in Notes

## Step 9 — Retro (self-improvement)
Run `.claude/commands/retro.md` for this cycle. QA findings are the richest source: a finding type that recurs
(e.g. "missing refusal test", "AC without evidence") becomes a lesson for that developer role, and if it recurs
again, a proposed rule in their agent definition or the ticket template.

## Step 10 — Report
Short summary: PRs ready to merge (links), PRs in QA / rework and why, tasks blocked, decisions waiting for the human
(ADR numbers + one-line question each), and the metrics row.

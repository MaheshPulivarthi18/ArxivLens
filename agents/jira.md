# Jira integration

Jira is the **team's dashboard and the human's inbox**. The repo stays the **source of truth** for everything an
agent needs to do the work (specs, acceptance criteria, designs, QA reports, lessons). Jira tickets link to those
files; they never hold a second copy. If Jira and the repo disagree, the repo wins and the next sync fixes Jira.

## Connection
| | |
|---|---|
| Site | https://maheshsworkspace-42184075.atlassian.net |
| cloudId | `cfafea90-87d0-4419-8ce6-688c328518f6` |
| Project | `KAN` (rename it in Jira settings any time; the key stays) |
| Tool | Atlassian MCP connector (acts as Mahesh's account) |
| Scope | Only issues with the label `arxivlens` in project `KAN`. Never touch anything else on the site. |

If the Atlassian tools are not available in a session, **skip Jira sync, continue the cycle on the repo**, and write
"Jira sync skipped: connector unavailable" in the PROGRESS entry. The next run with Jira reconciles from the repo.

## Mapping
| Repo | Jira |
|---|---|
| PRD feature area | **Epic** (description links the PRD section) |
| Task `agents/tasks/<ID>.md` | **Story** under its epic, labels `task-<ID>`, `agent-<role>`, `arxivlens` |
| Human decision (ADR) | **Task** assigned to Mahesh, label `human-decision` |
| Tech design `docs/design/<ID>.md` | Link in the story description + comment when written |
| PR | Link in the story description (`**PR:** #n`) + comment when opened |
| QA report / PM verdict | Comment with verdict + link to `agents/reviews/<ID>.md` |

Find a task's story with JQL: `project = KAN AND labels = "task-<ID>"`. Never rely on remembered issue keys.

### Current keys (for humans; agents use JQL)
| Epic | Key | Stories |
|---|---|---|
| Grounded LLM answers | KAN-1 | QA-8 (KAN-7) |
| Retrieval & answer quality gates | KAN-2 | EVAL-1 (KAN-8), AI-1 (KAN-9), AI-2 (KAN-10), AI-3 (KAN-11), EVAL-2 (KAN-12) |
| Dev harness & infrastructure | KAN-3 | OPS-H1 (KAN-13), OPS-H2 (KAN-14), OPS-H3 (KAN-15), OPS-H4 (KAN-16), OPS-H5 (KAN-17) |
| Continuous ingestion | KAN-4 | ING-6 (KAN-18) |
| Research UI & citations | KAN-5 | FE-1 (KAN-19), FE-2 (KAN-20) |
| Product planning & decisions | KAN-6 | PM-1 (KAN-21), ADR decisions (KAN-22, KAN-23) |

## Status and stage
The project currently has the statuses **To Do, In Progress, In Review, Done**. Stages that have no status of their own
are shown with a `stage-*` label. If Mahesh adds statuses named exactly **Design**, **In QA**, **PM review** or
**Ready to merge**, use those instead of the labels (check `listJiraIssueTransitions` for the story).

| Workflow stage | Jira status | Stage label (keep exactly one) | Who moves it |
|---|---|---|---|
| Ready (spec done) | To Do | — | pm-lead |
| Design (if `design-required`) | In Progress | `stage-design` | developer |
| Design approved | In Progress | `stage-design-approved` | pm-lead |
| Building | In Progress | `stage-build` | developer |
| In QA | In Review | `stage-qa` | developer (when PR opens), qa-reviewer (on re-review) |
| PM review | In Review | `stage-pm-review` | qa-reviewer (on PASS) |
| Ready to merge | In Review | `ready-to-merge` | pm-lead (on APPROVE) |
| Done | Done | — | orchestrator, after the human merges |
| Blocked | unchanged | `blocked` (+ comment with the reason) | whoever is blocked |

Labels are set, not appended (`editJiraIssue` replaces the list): read the current labels, swap the `stage-*` one,
write the full list back.

## Comments: who did what
Every comment starts with the role in brackets, so the ticket reads like a team thread even though the API acts as
one account: `[pm-lead]`, `[ai-engineer]`, `[backend-engineer]`, `[frontend-engineer]`, `[qa-reviewer]`, `[orchestrator]`.
One comment per stage change, 1–4 lines, always with a link (spec, design, PR, review file). No pasted code or logs.

## Intake: tickets the human creates
A story or task in `KAN` with label `arxivlens` but **no** `task-*` label is new intake from Mahesh. pm-lead turns it
into a spec (`agents/tasks/<ID>.md`), adds `task-<ID>` and `agent-<role>`, links the spec, and comments
`[pm-lead] Specced as <ID> → <link>`. If it's unclear, comment the questions and add `needs-info`; don't guess.

## Human comments on tickets
A comment by Mahesh that doesn't start with a `[role]` tag is human feedback. Treat it like a human PR comment:
top priority for the owning agent next cycle.

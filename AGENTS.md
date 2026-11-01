# AGENTS.md — Operating manual for the ArxivLens agent team

This file is the **entry point for every agent session** (human-launched or scheduled).
Agents have no memory between sessions. Everything an agent needs to know lives in the repo.

## 0. Session protocol (do this every time)

1. Read this file, then the `AGENTS.md` in the directory you will work in.
2. Read `agents/board.md` (what is ready / in progress / blocked).
3. Read the last 20 entries of `agents/PROGRESS.md` and your role's section of `agents/memory/lessons.md`.
4. Read your task spec in `agents/tasks/<ID>.md`. **No spec → no code.**
5. At the end of the session: update the task spec's `Status`/`Notes`, `agents/board.md`,
   append one entry to `agents/PROGRESS.md`, and add lessons (if any) to `agents/memory/lessons.md`.

## 1. The team

| Agent | Definition | Owns (may edit) | Never edits |
|---|---|---|---|
| **PM / Lead** | `.claude/agents/pm-lead.md` | `docs/` (incl. ADRs as *Proposed*), all `AGENTS.md`/`CLAUDE.md`, `.claude/` (via `retro:` PRs) | Source code, tests, CI |
| **AI engineer** | `.claude/agents/ai-engineer.md` | `services/embedder/*.py`, `internal/query/rag.go` + `rag_test.go`, `scripts/`, `evals/`, `tests/` | `frontend/`, `migrations/`, Kafka code |
| **Backend & distributed systems** | `.claude/agents/backend-engineer.md` | `cmd/`, `internal/` (except `rag.go`), `services/processor/`, `services/common/`, `services/embedder/Dockerfile`, `shared/`, `migrations/`, `tests/`, `docker-compose.yml`, `Makefile`, `.github/`, `requirements*.txt`, `go.mod` | `frontend/`, `rag.go` |
| **Frontend** | `.claude/agents/frontend-engineer.md` | `frontend/` | Everything outside `frontend/` |
| **QA / PR reviewer** | `.claude/agents/qa-reviewer.md` | `agents/reviews/` only | Product code, tests, CI, ticket ACs |

### Workflow (every task, no exceptions)

| Stage | Who | Output | Next |
|---|---|---|---|
| 1. Spec | pm-lead | `agents/tasks/<ID>.md` with PRD ref + acceptance criteria; Jira story under the PRD's epic | Ready |
| 1a. Design *(design-required only)* | owning developer | `docs/design/<ID>.md` (approach, alternatives, test plan) | PM approves before any code |
| 2. Build | owning developer | branch `agent/<role>/<ID>` + **draft** PR (CI runs on it) | In QA |
| 3. QA | qa-reviewer (independent, fresh context) | `agents/reviews/<ID>/report.md` + `evidence/` screenshots of real test/CI output, per AC; linked on the Jira ticket | PASS → PM review · FAIL → back to developer |
| 4. Final review + raise PR | pm-lead | checks ticket, PRD, design and every piece of evidence; APPROVE → writes the evidence into the PR description and marks the PR **ready for review** | Ready to merge |
| 5. PR review + merge | **human** | reviews the PR (evidence is in it) and merges | Done |

Max 2 rework rounds (QA FAIL or PM CHANGES) per task per cycle, then Blocked with the reports linked.
Nobody reviews their own work. The developer's self-report is never passed to QA.

Every role may also edit `agents/` (task notes, board, progress, lessons), `docs/design/` (its own task's design), the PRD status columns, and `README.md`.

**Jira** (`agents/jira.md`) is the team dashboard and the human's inbox: every stage change is mirrored there with a
role-tagged comment, and new tickets Mahesh creates become specs. The repo stays the source of truth — Jira links to
specs, designs, PRs and QA reports, never copies them.
**CI enforces this** on `agent/*` branches with `scripts/agent_guardrails.sh` (also blocks agents from accepting ADRs,
committing secrets/PDFs/weights, and warns above 400 changed lines).

Orchestration loop: `.claude/commands/sprint.md` (run as `/sprint`). Retrospective: `/retro`.

### Shared contracts (change only through an Accepted ADR)
- Event envelope and topics: `shared/events/events.go` + `shared/events/events.py` (must stay in sync)
- HTTP API: routes in `internal/query/server.go` and the API table in `README.md`
- DB schema: `migrations/` — a new numbered migration that only *adds* a table or index is fine without an ADR;
  changing or dropping existing tables/columns needs an ADR. Never edit an applied migration.
- Embedding model + dimension (`BAAI/bge-base-en-v1.5`, 768-d `halfvec`)

If a task needs a contract change, stop, write `docs/adr/NNNN-*.md` with `Status: Proposed`,
mark the task `Blocked (needs ADR)`, and move on.

## 2. Hard rules

- **Never push to `main`. Never merge your own PR.** One branch per task: `agent/<role>/<TASK-ID>`. A human merges.
- Stay inside your owned directories. Need a change elsewhere → create a follow-up task for the owner.
- Never weaken a test, threshold, or eval gate to make it pass. If a threshold is wrong, propose an ADR.
- Never commit secrets, `.env`, PDFs, model weights, or `node_modules/`.
- Max **1 open PR per agent** and max **2 attempts** per task per session. After 2 failed attempts:
  mark `Blocked`, write what you tried and the exact error in the task's `Notes`.
- Keep diffs small (target < 400 changed lines). Split bigger work into new tasks.

## 3. Commands (the harness)

| Command | What | Needs infra? |
|---|---|---|
| `make help` | List targets | — |
| `make test-unit` | Go vet + Go tests + `pytest tests/test_m0.py` | No |
| `make lint` | `go vet`, `gofmt` check, frontend `oxlint` | No |
| `make build` | Go binary + frontend build | No |
| `make infra-up` / `make infra-down` | Postgres+pgvector, Kafka, MinIO via compose | Docker |
| `make test-full` | All milestone suites M0–M6 | Docker + workers running |
| `make eval` | Retrieval eval (Recall@5 / MRR / NDCG@5) | Docker + indexed corpus |

**Definition of Done** for any task:
1. All acceptance criteria in the task spec are met and each one is checked off with evidence.
2. `make test-unit` and `make lint` pass. If the task touches the pipeline or query path, the relevant milestone suite passes too.
3. If the task touches retrieval or answering: `make eval` does not regress (paste before/after numbers in the PR).
4. Docs updated: PRD status column, README if user-facing behaviour or commands changed.
5. PR opened with the template in `agents/templates/pr.md`; board and PROGRESS updated.
6. QA report says **PASS** and pm-lead says **APPROVE** in `agents/reviews/<TASK-ID>/report.md`. Only then is it Ready to merge.

**CI is the source of truth for test results.** Agent sandboxes may lack Go 1.25, npm, Docker or Hugging Face access,
so "not run locally" is acceptable — "passes" without a CI run or command output is not.

## 4. Repo map

```
cmd/arxivlens/        Go CLI (migrate, crawl, download, status, query, dlq)
internal/             Go: crawler, downloader, kafka (retry/DLQ), dlq, db, storage, query, telemetry
services/             Python: processor, embedder (worker + FastAPI), common
shared/events/        Event envelope contracts (Go + Python)
migrations/           SQL schema
scripts/              ML primitives (m0_skeleton), batch ingest, eval, HNSW benchmark
tests/                Milestone suites M0–M6 (pytest)
frontend/             React 19 + Vite UI
docs/                 Product PRD, Technical PRD, ADRs
agents/               Team memory: board, tasks, progress log, lessons, metrics, retros
.claude/              Agent definitions, slash commands, guardrail settings
```

## 5. Known gotchas

- `go.mod` requires Go 1.25. Older local toolchains will try to download it.
- `tests/test_m0.py` downloads `BAAI/bge-base-en-v1.5` (~440 MB) on first run.
- Processor and embedder workers are not yet in `docker-compose.yml` (task OPS-H1), so `make test-full` needs them started by hand.
- `/v1/ask` answers are templated, not LLM-generated (task QA-8).
- Retrieval eval queries are hardcoded in `scripts/evaluate_retrieval.py` (task EVAL-1 moves them to `evals/`).

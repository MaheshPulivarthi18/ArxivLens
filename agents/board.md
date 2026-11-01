# Board

Last updated: 2026-11-01 by setup (human-reviewed initial backlog)

WIP limit: 1 open PR per engineering agent. Order within a column = priority.

## Ready
| ID | Title | Owner | Pri | Depends on |
|---|---|---|---|---|
| [OPS-H1](tasks/OPS-H1.md) | Run processor + embedder workers in docker-compose; one-command full test | backend-engineer | P0 | — |
| [EVAL-1](tasks/EVAL-1.md) | Versioned golden eval sets + `make eval` quality gate | ai-engineer | P0 | — |
| [FE-1](tasks/FE-1.md) | Frontend test harness (Vitest) + tests for answer/citation states | frontend-engineer | P1 | — |
| [PM-1](tasks/PM-1.md) | Reconcile PRD status columns + acceptance criteria for all ⏳ rows | pm-lead | P1 | — |
| [AI-1](tasks/AI-1.md) | Unit tests for `rag.go` filtering and citation validation (no infra) | ai-engineer | P1 | — |
| [OPS-H3](tasks/OPS-H3.md) | Baseline Go unit tests for crawler parsing, ID normalization, retry backoff | backend-engineer | P1 | — |
| [OPS-H2](tasks/OPS-H2.md) | Unify Python dependencies into one `requirements.txt` | backend-engineer | P1 | OPS-H1 |
| [ING-6](tasks/ING-6.md) | Scheduled crawler with per-category `since` watermark | backend-engineer | P1 | OPS-H3 |
| [FE-2](tasks/FE-2.md) | "Export all citations" (.bib) for an answer | frontend-engineer | P2 | FE-1 |

## In progress
| ID | Title | Owner | Branch |
|---|---|---|---|

## In review
| ID | Title | Owner | PR |
|---|---|---|---|

## Blocked
| ID | Title | Owner | Waiting on |
|---|---|---|---|
| [QA-8](tasks/QA-8.md) | LLM answer synthesis behind citation validation | ai-engineer | ADR-0001, ADR-0002 (human decision), EVAL-1, AI-1 |

## Backlog (not yet specced — pm-lead grooms these)
- QA-9 Streaming answers (frontend + ai) — after QA-8
- Batch embedding in RAG validation (ai) — Technical PRD §11 P1
- OTLP span export to Jaeger/Tempo (backend) — Technical PRD §11 P1
- Prometheus metrics: consumer lag, DLQ depth, stage latency (backend) + OPS-6 dashboard (frontend)
- Auth + rate limiting on query API (backend) — before any shared deployment
- On-demand ingest via Kafka (backend) — depends on ADR-0004

## Done
| ID | Title | PR |
|---|---|---|

# Lessons

Short rules learned from friction. Loaded every session, so keep it under ~60 lines.
Format: `- [YYYY-MM-DD][TASK-ID] what happened → rule for next time (seen N×)`
A lesson seen ≥ 2× gets promoted into `AGENTS.md` or an agent definition via a `retro:` PR, then removed here.

## All agents
- [2026-11-01][SETUP] Local Go toolchains older than 1.25 try to download 1.25 and fail offline → use CI or `GOTOOLCHAIN=local` only for quick checks; never downgrade `go.mod`. (seen 1×)
- [2026-11-01][SETUP] PRD status columns can lag the code (UI-5 per-citation BibTeX already exists in `ReferencesStack.jsx`) → grep the code before assuming a ⏳ feature is missing. (seen 1×)
- [2026-11-01][SPRINT] Agent sandbox egress blocked the Go 1.25 toolchain/modules, npm, Docker Hub and Hugging Face → check which tasks the environment can verify before picking them; prefer Python/docs tasks there and state that Go/frontend checks rely on CI. Never route around egress blocks. (seen 1×)
- [2026-11-01][SPRINT] Builder subagents edited board/PROGRESS during the build step, which collides with the separate record branch → tell builders to touch only their task spec under agents/. (seen 1×)

## pm-lead
- [2026-11-01][PM-1] A PRD edit quietly added a policy (which machine gates latency) → anything that changes what a gate or threshold means must be labelled Proposed or go into an ADR. (seen 1×)
- [2026-11-01][PM-1] `migrations/` didn't match the PRD data model (no HNSW index) and "Recall@5" computes something closer to precision@5 → grep migrations and the metric code, not just names. (seen 1×)

## ai-engineer
- [2026-11-01][SETUP] Eval queries are hardcoded inside `scripts/evaluate_retrieval.py` → don't add more there; EVAL-1 moves them to `evals/`. (seen 1×)
- [2026-11-01][EVAL-1] The first gate draft let failed /v1/ask calls drop out of the gated metrics, so a dead service could PASS → eval gates must fail closed: count errors, put failures into latency (timeouts at ≥ timeout), and add a test where some requests fail. (seen 1×)

## backend-engineer

## frontend-engineer

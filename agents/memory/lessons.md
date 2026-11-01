# Lessons

Short rules learned from friction. Loaded every session, so keep it under ~60 lines.
Format: `- [YYYY-MM-DD][TASK-ID] what happened → rule for next time (seen N×)`
A lesson seen ≥ 2× gets promoted into `AGENTS.md` or an agent definition via a `retro:` PR, then removed here.

## All agents
- [2026-11-01][SETUP] Local Go toolchains older than 1.25 try to download 1.25 and fail offline → use CI or `GOTOOLCHAIN=local` only for quick checks; never downgrade `go.mod`. (seen 1×)
- [2026-11-01][SETUP] PRD status columns can lag the code (UI-5 per-citation BibTeX already exists in `ReferencesStack.jsx`) → grep the code before assuming a ⏳ feature is missing. (seen 1×)

## pm-lead

## ai-engineer
- [2026-11-01][SETUP] Eval queries are hardcoded inside `scripts/evaluate_retrieval.py` → don't add more there; EVAL-1 moves them to `evals/`. (seen 1×)

## backend-engineer

## frontend-engineer

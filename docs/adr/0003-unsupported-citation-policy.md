# ADR-0003: What to do with unsupported citations

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi
- **Blocks:** follow-up after QA-8 (not QA-8 itself)

## Context
Product PRD open question 2. Each sentence's citation is scored (cosine ≥ 0.50 → supported). With an LLM, some sentences
will be unsupported. Principle: "evidence first; say I don't know".

## Options
1. **Flag** (current) — show with an "unsupported" badge. Transparent; may look noisy.
2. **Hide** — drop unsupported sentences. Clean; can silently change meaning.
3. **Regenerate** — if > X% sentences unsupported, retry once with a stricter prompt; still flag what remains. Better answers; doubles latency/cost on bad cases.

## Recommendation (from pm-lead)
Option 1 now (already implemented, keeps trust visible), and decide on Option 3 after QA-8 eval data shows how often it happens.

## Decision
_(Human to fill)_

## Consequences
Frontend must keep unsupported citations visibly flagged (FE-1 tests it).

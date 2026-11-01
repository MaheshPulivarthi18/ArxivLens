# ADR-0001: LLM provider, latency and cost budget for answer synthesis

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi
- **Blocks:** QA-8

## Context
Product PRD open question 1. `/v1/ask` is templated today (README Known limitations). QA-8 adds LLM synthesis.
Constraints from the PRDs: `/v1/ask` p95 < 3 s, local/self-hosted first (non-goal: hosted SaaS), answers must cite only
supplied passages.

## Options
1. **Hosted API model** (any major provider) — best answer quality and instruction-following for citation format; per-token cost; needs a key and network; fits the 3 s budget with a smaller/faster model tier.
2. **Local model via an OpenAI-compatible server** (e.g. Ollama / llama.cpp / vLLM) — zero marginal cost, private, offline; weaker citation discipline at small sizes; latency on a laptop may exceed 3 s.
3. **Both behind one interface** — local by default for dev/CI-free use, hosted when a key is configured.

## Recommendation (from pm-lead)
Option 3. Build a provider-agnostic `synthesizer` interface (QA-8 already requires a template fallback), ship one hosted
implementation and one OpenAI-compatible local implementation. Pick the default by measuring both on `evals/` (EVAL-1)
rather than by preference.

## Decision
_(Human to fill)_ Provider(s) and model(s): ___ · Max cost per answer: ___ · p95 latency budget for the LLM call: ___ · Default when no key: template / local.

## Consequences
QA-8 can start. Eval runs with a hosted model cost money — cap eval set size or run them on demand, not in every CI run.

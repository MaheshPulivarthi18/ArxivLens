# ADR-0005: Default continuously crawled categories

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi
- **Blocks:** ING-6 default value only (ING-6 is implemented with configurable categories)

## Context
Product PRD open question 3. The crawl CLI defaults to `cat:cs.AI OR cat:cs.LG`. A scheduled crawler (ING-6) makes the
default matter for storage, embedding time, and politeness (≤ 1 req / 3 s).

## Options
1. `cs.AI, cs.LG` (current default)
2. `cs.AI, cs.LG, cs.CL` (adds NLP — many RAG/LLM papers live there)
3. User-configured only, no default

## Recommendation (from pm-lead)
Option 2, with a per-run limit so the first backfill doesn't take days. Revisit after measuring corpus growth per week.

## Decision
_(Human to fill)_

## Consequences
Sets the default for `crawl-loop` and the eval corpus scope.

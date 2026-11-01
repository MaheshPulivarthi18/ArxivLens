# ADR-0009: Swap to a long-context embedding model?

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi (only the human owner sets Accepted/Rejected)
- **Blocks:** nothing now. Any embedding-model change waits on it (this is a shared contract per `AGENTS.md` §1).

## Context
Technical PRD open question 4. The embedding model `BAAI/bge-base-en-v1.5` (768-d `halfvec`, 512-token max) is a
shared contract. Chunks are 512 tokens with a 75-token overlap (Technical PRD §6.2), so an argument that spans a chunk
boundary can be split. Embeddings are keyed by `(chunk_id, model)`, so two models can co-exist during a migration.
Changing the dimension changes the column type, and therefore the schema.

## Options
1. **Keep BGE-base (512 tokens).** No cost. Boundary effects are softened by the overlap and the section-title prefix.
2. **A long-context model with the same dimension (768-d, 8k tokens).** Fewer, larger chunks with no column change. Re-embedding the whole corpus is expensive, and longer chunks may hurt passage-level citation precision and the support score.
3. **A long-context model with a different dimension.** Needs a new column or table and a migration. Highest cost.
4. **Decide with data.** Embed the `evals/` corpus with a candidate model alongside the current one (the `(chunk_id, model)` key allows that), then compare `make eval` numbers before choosing.

## Recommendation (from pm-lead)
Option 1 for now, and Option 4 after EVAL-1 merges and QA-8 ships. The gating question is whether retrieval recall or
supported-citation rate is the bottleneck. EVAL-1 data will show that. Do not change the model without before/after `make eval`
numbers.

## Decision
_(Filled by the human.)_

## Consequences
Any change means a corpus re-embed, a chunker change (token target), and an update to the shared contract in `AGENTS.md` §1.

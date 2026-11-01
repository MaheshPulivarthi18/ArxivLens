# ADR-0002: Run LLM synthesis in the Go query service or the Python embedder API

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi
- **Blocks:** QA-8

## Context
Technical PRD open question 1. Retrieval orchestration and citation validation live in Go (`internal/query/rag.go`).
The Python embedder API (`services/embedder/api.py`) holds the embedding and reranker models.

## Options
1. **Go (in `rag.go`)** — synthesis sits next to validation, one fewer network hop, single place to reason about the answer path, Go HTTP client to the LLM is simple. Prompt iteration needs a Go rebuild.
2. **Python (new `/internal/synthesize` on the embedder API)** — richer LLM ecosystem, easy local-model hosting in-process; adds a hop and splits the answer logic across two languages and two owners.

## Recommendation (from pm-lead)
Option 1 (Go). The answer path stays in one file owned by one agent, validation can't be bypassed, and the LLM call is a
plain HTTP request either way. If a local model is chosen in ADR-0001, it runs as its own server and Go calls it over HTTP.

## Decision
_(Human to fill)_

## Consequences
ai-engineer owns synthesis in `rag.go` and its tests. The embedder API stays focused on embed/rerank/ingest.

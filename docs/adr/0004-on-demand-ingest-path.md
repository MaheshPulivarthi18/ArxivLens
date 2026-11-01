# ADR-0004: On-demand ingest — synchronous call or through Kafka

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi
- **Blocks:** backlog item "On-demand ingest via Kafka"

## Context
Technical PRD open question 2. `/v1/papers/ingest` and `/discover` bypass Kafka and run processor + embedder logic
in-process via `/internal/ingest` (Technical PRD §7.3). That gives a fast interactive UX but skips the DLQ and the
`processed_events` ledger, so failures there are not replayable and the code path differs from the pipeline.

## Options
1. **Keep synchronous** — simplest UX; two code paths to maintain; no DLQ for on-demand failures.
2. **Publish `papers.discovered` and poll** — one code path, full idempotency and DLQ; UI polls `GET /v1/papers/{id}` until `embedded`. Must still meet "< 60 s single paper".
3. **Hybrid** — publish to Kafka, but the request waits (with timeout) for status `embedded` before returning.

## Recommendation (from pm-lead)
Option 2. It removes a duplicate code path (lower maintenance — the main goal), and the UI already has an ingest modal with progress that can poll. Verify the < 60 s target with OPS-H1's compose stack before switching.

## Decision
_(Human to fill)_

## Consequences
Backend task to switch the path; frontend task to poll status in `IngestModal`.

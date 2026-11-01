# Tech design — <TASK-ID>: <title>

**Author:** <role> · **Ticket:** `agents/tasks/<TASK-ID>.md` · **PRD:** <row ids> · **Status:** Draft | Approved | Changes requested

## Problem
What the ticket asks for, in the author's own words (2–4 sentences). Quote the acceptance criteria IDs this covers.

## Approach
How it will work. Name the files/functions that change and any new ones. A small diagram or sequence if it helps.

## Alternatives considered
1. … — why not
2. … — why not

## Contracts and invariants
- Shared contracts touched (events, HTTP API, schema, embedding model): none / which, and the ADR that allows it
- Reliability invariants kept (idempotency, DLQ, traceparent, arXiv politeness)

## Test plan
| Acceptance criterion | Test (name / type) | Runs in CI? |
|---|---|---|
| AC1 | | |

Failure paths covered: …

## Risks and rollout
What could go wrong, how it's detected, how to roll back.

## PM decision
_(pm-lead fills)_ Approved / Changes requested — reasons.

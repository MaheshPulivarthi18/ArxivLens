# QA report — <TASK-ID> · PR #<n> · round <r>

**Verdict:** PASS | FAIL | PENDING
**Reviewed commit:** `<head sha>` · **Date:** YYYY-MM-DD
**Ticket:** `agents/tasks/<TASK-ID>.md` · **PRD:** <row ids>

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI | pass / fail / skipped |
| Python M0 unit tests | CI | |
| Frontend (lint, test, build) | CI | |
| Agent guardrails | CI | |
| Ticket verification commands | local / not run (reason) | |
| `make eval` (retrieval/answering only) | local / not run (reason) | before → after |

## Acceptance criteria
| AC | Met? | Evidence (file:line, test name, CI log, output) |
|---|---|---|
| AC1 | ✅ / ❌ / ⚠ unverified | |

## PRD fit
One or two sentences: does this deliver the requirement as the PRD describes it?

## Findings
1. [blocking] …
2. [nit] …
3. [spec] … (ticket problem — for pm-lead)

## Missing tests
- 

## Hand-off
PASS → pm-lead final review · FAIL → back to <owner> · PENDING → re-review next cycle

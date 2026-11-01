# ADR-0007: Is multi-user or hosted deployment in scope for v2?

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi (only the human owner sets Accepted/Rejected)
- **Blocks:** backlog item "Auth + rate limiting on query API" (its scope and priority); saved questions in UI-5 (local storage vs. per-user storage)

## Context
Product PRD open question 4. In v1, user accounts, multi-tenant libraries, and hosted SaaS are explicit non-goals (Product PRD §4).
Technical PRD §9 says production must put the query API behind auth. The answer decides whether auth is a
P2 hardening item or a v2 foundation, and whether UI features such as saved questions store data per user.

## Options
1. **Stay single-user, self-hosted for v2.** Auth is an optional shared-secret/API-key gate for exposing a local instance. Everything stays in local storage. Lowest cost, and it keeps the team on answer quality (QA-8, EVAL).
2. **Self-hosted, small-team multi-user.** One shared library, simple accounts (or reverse-proxy auth), per-user saved questions, and rate limiting. Moderate cost, and it needs a migration that adds user tables.
3. **Hosted multi-tenant SaaS.** Per-tenant libraries, billing, and quotas. Large scope, and it changes the data model and ops posture.

## Recommendation (from pm-lead)
Option 1 for v2, and revisit after QA-8 ships and the eval numbers hold. The product's value is grounded answers.
Multi-user adds no quality and costs large schema and ops changes. Spec "Auth + rate limiting" as an optional
API-key gate plus per-IP rate limit, so a single instance can be shared safely.

## Decision
_(Filled by the human.)_

## Consequences
- Option 1: saved questions (UI-5) use browser local storage, and auth stays a small backend task.
- Option 2/3: needs further ADRs for the user model and tenancy. Product PRD §4 non-goals change.

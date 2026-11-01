# Team metrics

One row per sprint cycle, appended by `/sprint` Step 8. `/retro` compares the last 5 rows.

- **Attempted**: tasks picked this cycle
- **Shipped**: PRs opened with pm-lead APPROVE
- **First-pass**: shipped tasks that passed verification and review with zero fix rounds
- **Fix rounds**: total verify-fail + review-CHANGES rounds
- **Blocked**: tasks moved to Blocked this cycle
- **Merged since last**: agent PRs merged by the human since the previous cycle (the real throughput)
- **Awaiting human**: open agent PRs + Proposed ADRs

Targets once the harness is stable: first-pass ≥ 70%, blocked ≤ 1 per cycle, merged ≈ shipped.

| Date | Attempted | Shipped | First-pass | Fix rounds | Blocked | Merged since last | Awaiting human | Notes |
|---|---|---|---|---|---|---|---|---|
| 2026-11-01 | 0 | 0 | — | 0 | 0 | 0 | 5 ADRs | Setup, baseline |
| 2026-11-01 | 2 | 2 | 0% (0/2) | 2 | 0 | 0 | 2 PRs + 9 ADRs | Cycle 1. Both tasks needed 1 review fix round. Sandbox egress blocked Go/npm/Docker/HF, so only Python and docs tasks were possible; Go and frontend checks rely on CI. |

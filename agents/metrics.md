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
| 2026-11-02 | 1 new + 2 reviews | 2 (PM-1, OPS-H3) | 100% (1/1 new: OPS-H3) | 2 (EVAL-1: human-review rework + QA FAIL) | 1 (EVAL-1) | 0 | 3 PRs (#2 blocked, #3, #6) + record PR + 9 ADRs | QA pass rate this cycle 2/3 (OPS-H3 r1 PASS, PM-1 r2 PASS, EVAL-1 r2 FAIL); first-round PASS 1/1. A human review overturned an APPROVE (EVAL-1). Only Go-in-CI work was feasible (no Go 1.25/npm/Docker/HF). |

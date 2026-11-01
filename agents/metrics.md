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

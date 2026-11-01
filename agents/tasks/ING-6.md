# ING-6: Scheduled crawler with per-category `since` watermark

| | |
|---|---|
| **Owner** | backend-engineer |
| **Priority** | P1 |
| **PRD ref** | ING-6; Technical PRD §11 "Scheduled crawler" |
| **Depends on** | OPS-H3 (crawler parsing extracted + tested). Default categories may later change via ADR-0005; keep them configurable. |
| **Status** | Ready |

## Goal
Keep the corpus fresh without someone running `arxivlens crawl` by hand (Product PRD goal 3).

## Scope
- New CLI command `arxivlens crawl-loop --categories "cs.AI,cs.LG" --interval 6h [--limit-per-run 100]`.
- New additive migration creating `crawl_watermarks(category TEXT PRIMARY KEY, last_published_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`.
- Each run, per category: crawl newest-first until reaching papers published ≤ watermark, then advance the watermark
  **only after** the events were produced successfully (crash-safe; re-runs are idempotent thanks to natural-key upserts).
- Respect the existing 3 s rate limit across categories (one shared limiter), handle SIGTERM gracefully between requests.
- Optional compose service `crawler` under profile `workers` running `crawl-loop`.
- Out of scope: changing the existing `crawl` command behaviour.

## Acceptance criteria
- [ ] AC1 — Unit test (no infra) for the stop-at-watermark logic with a fake feed: stops at the boundary, advances watermark to max seen.
- [ ] AC2 — Running `crawl-loop` twice in a row with no new papers produces zero new `papers.discovered` events on the second run (paste evidence).
- [ ] AC3 — Killing the process mid-run and restarting produces no duplicate `papers` rows (natural keys) and no skipped papers.
- [ ] AC4 — README CLI reference + Product PRD ING-6 status updated.

## Verification
```bash
go test ./internal/crawler/...
make stack-up && ./bin/arxivlens migrate && ./bin/arxivlens crawl-loop --categories cs.LG --interval 1m --limit-per-run 3
```

## Notes

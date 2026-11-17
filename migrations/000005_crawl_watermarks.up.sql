-- ING-6: per-category watermark of the scheduled crawler (`crawl-loop`). Every paper in cat:<category> with a
-- published time at or below last_published_at (and above the first run's horizon) has been upserted and its
-- papers.discovered event acked. It only moves forward (AdvanceWatermark uses GREATEST). No row = first run.
-- IF NOT EXISTS: `arxivlens migrate` re-applies every *.up.sql in order.
CREATE TABLE IF NOT EXISTS crawl_watermarks (
    category          TEXT PRIMARY KEY,
    last_published_at TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

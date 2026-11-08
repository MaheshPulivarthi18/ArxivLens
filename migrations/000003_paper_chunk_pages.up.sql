-- ING-11: the page each chunk's text starts on, when the chunker recorded it.
-- No row = page unknown (every chunk written before this migration, ADR-0014). No backfill, no default.
-- IF NOT EXISTS: `arxivlens migrate` re-applies every *.up.sql in order.
CREATE TABLE IF NOT EXISTS paper_chunk_pages (
    chunk_id   BIGINT PRIMARY KEY REFERENCES paper_chunks(id) ON DELETE CASCADE,
    page_start INT NOT NULL CHECK (page_start >= 1)
);

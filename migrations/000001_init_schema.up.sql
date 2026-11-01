-- Enable pgvector extension
CREATE EXTENSION IF NOT EXISTS vector;

-- Papers metadata table
CREATE TABLE IF NOT EXISTS papers (
  id              BIGSERIAL PRIMARY KEY,
  arxiv_id        TEXT NOT NULL,
  version         INT  NOT NULL DEFAULT 1,
  title           TEXT,
  abstract        TEXT,
  authors         TEXT[],
  categories      TEXT[],
  published_at    TIMESTAMPTZ,
  source_url      TEXT,
  pdf_storage_key TEXT,
  sha256          TEXT,
  status          TEXT NOT NULL DEFAULT 'discovered',
  status_error    TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (arxiv_id, version)
);
CREATE INDEX IF NOT EXISTS idx_papers_categories ON papers USING GIN (categories);
CREATE INDEX IF NOT EXISTS idx_papers_status ON papers (status);
CREATE INDEX IF NOT EXISTS idx_papers_arxiv_id ON papers (arxiv_id);

-- Paper document sections extracted from typography
CREATE TABLE IF NOT EXISTS paper_sections (
  id       BIGSERIAL PRIMARY KEY,
  paper_id BIGINT NOT NULL REFERENCES papers(id) ON DELETE CASCADE,
  title    TEXT,
  level    INT,
  position INT NOT NULL,
  UNIQUE (paper_id, position)
);

-- Token-bounded chunks with generated tsvector for full-text BM25 search
CREATE TABLE IF NOT EXISTS paper_chunks (
  id           BIGSERIAL PRIMARY KEY,
  paper_id     BIGINT NOT NULL REFERENCES papers(id) ON DELETE CASCADE,
  section_id   BIGINT REFERENCES paper_sections(id) ON DELETE SET NULL,
  chunk_index  INT  NOT NULL,
  content      TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  page_number  INT,
  token_count  INT,
  content_tsv  tsvector GENERATED ALWAYS AS
                 (to_tsvector('english', content)) STORED,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (paper_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS idx_paper_chunks_tsv ON paper_chunks USING GIN (content_tsv);
CREATE INDEX IF NOT EXISTS idx_paper_chunks_paper_id ON paper_chunks (paper_id);

-- 768-dimensional halfvec embeddings
CREATE TABLE IF NOT EXISTS chunk_embeddings (
  chunk_id   BIGINT NOT NULL REFERENCES paper_chunks(id) ON DELETE CASCADE,
  model      TEXT   NOT NULL,
  embedding  halfvec(768) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (chunk_id, model)
);

-- Idempotency ledger for transactional event deduplication per consumer group
CREATE TABLE IF NOT EXISTS processed_events (
  consumer_group TEXT NOT NULL,
  event_id       TEXT NOT NULL,
  processed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer_group, event_id)
);

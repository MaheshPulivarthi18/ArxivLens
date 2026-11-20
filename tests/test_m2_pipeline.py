import psycopg
import pytest
from services.common.config import config


def test_paper_sections_and_chunks_stored():
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        # Check papers status
        cur.execute("SELECT arxiv_id, status FROM papers WHERE status = 'embedded';")
        rows = cur.fetchall()
        assert len(rows) >= 2, f"Expected at least 2 embedded papers, got {len(rows)}"

        # Check paper_sections
        cur.execute("SELECT count(*) FROM paper_sections;")
        section_count = cur.fetchone()[0]
        assert section_count >= 20, f"Expected at least 20 sections, got {section_count}"

        # Check paper_chunks & generated content_tsv
        cur.execute("""
            SELECT id, chunk_index, token_count, content_hash, content_tsv
            FROM paper_chunks
            LIMIT 10;
        """)
        chunk_rows = cur.fetchall()
        assert len(chunk_rows) == 10
        for cid, cidx, tok_cnt, chash, tsv in chunk_rows:
            assert tok_cnt <= 512, f"Chunk {cid} exceeds token limit: {tok_cnt}"
            assert len(chash) == 64, f"Invalid content hash: {chash}"
            assert tsv is not None, f"Generated content_tsv is null for chunk {cid}"

    conn.close()


def test_chunk_embeddings_and_pgvector_cosine_ops():
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        # Check embeddings count
        cur.execute("SELECT count(*) FROM chunk_embeddings WHERE model = %s;", (config.embedding_model,))
        emb_count = cur.fetchone()[0]
        assert emb_count >= 100, f"Expected at least 100 chunk embeddings, got {emb_count}"

        # Perform a live pgvector cosine similarity query
        # Fetch one sample embedding vector from the DB
        cur.execute("SELECT embedding FROM chunk_embeddings LIMIT 1;")
        sample_vec_str = cur.fetchone()[0]

        # Query nearest neighbors using pgvector cosine distance operator <=>
        cur.execute("""
            SELECT pc.id, pc.content, ce.embedding <=> %s::halfvec AS cosine_dist
            FROM chunk_embeddings ce
            JOIN paper_chunks pc ON pc.id = ce.chunk_id
            WHERE ce.model = %s
            ORDER BY ce.embedding <=> %s::halfvec
            LIMIT 3;
        """, (sample_vec_str, config.embedding_model, sample_vec_str))
        
        results = cur.fetchall()
        assert len(results) == 3
        # The top result must be the exact same chunk with distance ~0.0
        top_dist = float(results[0][2])
        assert top_dist < 1e-3, f"Self-distance should be ~0.0, got {top_dist}"

    conn.close()


def test_embedded_chunks_have_recorded_start_pages():
    """ING-11b (design ING-11 condition 4): the real pipeline writes paper_chunk_pages for every chunk.

    For every 'embedded' paper, each paper_chunks row has a paper_chunk_pages row, its page_start is at least the
    section's page_number, and page_start never decreases with chunk_index (sections and windows are in reading
    order). Not grouped by section_id: the processor's title-keyed section map can merge repeated titles.
    """
    conn = psycopg.connect(config.postgres_url)
    with conn.cursor() as cur:
        cur.execute("""
            SELECT p.arxiv_id, p.version, c.chunk_index, c.page_number, cp.page_start
            FROM papers p
            JOIN paper_chunks c ON c.paper_id = p.id
            LEFT JOIN paper_chunk_pages cp ON cp.chunk_id = c.id
            WHERE p.status = 'embedded'
            ORDER BY p.id, c.chunk_index;
        """)
        rows = cur.fetchall()
    conn.close()
    assert rows, "no chunks of embedded papers to check"

    problems, prev = [], {}
    for arxiv_id, version, idx, page_number, page_start in rows:
        key = f"{arxiv_id}v{version} chunk {idx}"
        if page_start is None:
            problems.append(f"{key}: no paper_chunk_pages row")
            continue
        if page_number is None:
            problems.append(f"{key}: page row but NULL page_number")
        elif page_start < page_number:
            problems.append(f"{key}: page_start {page_start} < page_number {page_number}")
        last = prev.get((arxiv_id, version))
        if last is not None and page_start < last:
            problems.append(f"{key}: page_start {page_start} < previous chunk's {last}")
        prev[(arxiv_id, version)] = page_start
    assert not problems, f"{len(problems)} of {len(rows)} chunks: " + "; ".join(problems[:10])

"""OPS-H6d: re-saving a paper's chunks drops stale vectors and stale tail chunks (real Postgres, migrations applied).

Not in `make test-py` (needs a DB); runs in `make test-full`. Everything happens on one `autocommit=False` connection that
is always rolled back, so the shared stack DB that the M2/M3 suites count is left exactly as it was.
"""
import pytest

from services.common.db import get_chunks_for_embedding, get_db_connection, save_sections_and_chunks

MODEL = "ops-h6d-model"
OTHER_MODEL = "ops-h6d-other-model"
OLD_TS = "2020-01-02T03:04:05+00:00"  # explicit created_at, so a deleted-and-reinserted vector would show
SECTIONS = [{"title": "Intro", "level": 1}]


def _vec(hot: int) -> str:
    return "[" + ",".join("1" if i == hot else "0" for i in range(768)) + "]"


def _chunk(i, text, page_start=None):
    c = {"chunk_index": i, "section_title": "Intro", "content": text, "content_hash": f"hash-{text}", "token_count": 3}
    if page_start is not None:
        c["page_start"] = page_start
    return c


def _new_paper(cur, arxiv_id):
    cur.execute("INSERT INTO papers (arxiv_id) VALUES (%s) RETURNING id;", (arxiv_id,))
    return cur.fetchone()[0]


def _embed_all(cur, paper_id, models):
    for m in models:
        cur.execute(
            """
            INSERT INTO chunk_embeddings (chunk_id, model, embedding, created_at)
            SELECT pc.id, %s::text, %s::halfvec, %s::timestamptz FROM paper_chunks pc WHERE pc.paper_id = %s;
            """,
            (m, _vec(len(m)), OLD_TS, paper_id),
        )


def _state(cur, paper_id):
    """chunk_index -> (chunk id, content_hash, page_start, {model: (embedding text, created_at)})."""
    cur.execute(
        """
        SELECT pc.chunk_index, pc.id, pc.content_hash, pp.page_start
        FROM paper_chunks pc LEFT JOIN paper_chunk_pages pp ON pp.chunk_id = pc.id
        WHERE pc.paper_id = %s ORDER BY pc.chunk_index;
        """,
        (paper_id,),
    )
    rows = {r[0]: list(r[1:]) + [{}] for r in cur.fetchall()}
    cur.execute(
        """
        SELECT pc.chunk_index, ce.model, ce.embedding::text, ce.created_at
        FROM chunk_embeddings ce JOIN paper_chunks pc ON pc.id = ce.chunk_id WHERE pc.paper_id = %s;
        """,
        (paper_id,),
    )
    for idx, model, emb, ts in cur.fetchall():
        rows[idx][3][model] = (emb, ts)
    return {k: tuple(v) for k, v in rows.items()}


def _orphans(cur, chunk_ids):
    cur.execute(
        """
        SELECT (SELECT count(*) FROM paper_chunks WHERE id = ANY(%s::bigint[])),
               (SELECT count(*) FROM chunk_embeddings WHERE chunk_id = ANY(%s::bigint[])),
               (SELECT count(*) FROM paper_chunk_pages WHERE chunk_id = ANY(%s::bigint[]));
        """,
        (chunk_ids, chunk_ids, chunk_ids),
    )
    return cur.fetchone()


@pytest.fixture
def conn():
    c = get_db_connection()
    assert c.autocommit is False
    try:
        yield c
    finally:
        c.rollback()  # never leave rows behind in the shared stack DB
        c.close()


def test_reprocess_drops_stale_vectors_and_tail(conn):
    with conn.cursor() as cur:
        a, b = _new_paper(cur, "ops-h6d-a"), _new_paper(cur, "ops-h6d-b")
    save_sections_and_chunks(conn, a, SECTIONS, [_chunk(0, "a0", 1), _chunk(1, "a1", 1), _chunk(2, "a2", 2)])
    save_sections_and_chunks(conn, b, SECTIONS, [_chunk(0, "b0", 1)])
    with conn.cursor() as cur:
        _embed_all(cur, a, [MODEL, OTHER_MODEL])
        _embed_all(cur, b, [MODEL])
        before_a, before_b = _state(cur, a), _state(cur, b)
    assert sorted(before_a) == [0, 1, 2] and all(len(v[3]) == 2 for v in before_a.values()), before_a
    assert before_a[2][2] == 2, "fixture: chunk 2 must have a page row, so the cascade is checked"

    # Re-parse: chunk 0 unchanged, chunk 1 new text, chunk 2 gone.
    n = save_sections_and_chunks(conn, a, SECTIONS, [_chunk(0, "a0", 1), _chunk(1, "a1-new", 1)])
    assert n == 2
    with conn.cursor() as cur:
        after_a, after_b = _state(cur, a), _state(cur, b)
        orphans = _orphans(cur, [before_a[2][0]])

    # AC2: same hash -> same row id and identical vectors (embedding and created_at).
    assert after_a[0] == before_a[0], (before_a[0], after_a[0])
    # AC1: changed hash -> same row id, new hash, no vector for any model.
    assert after_a[1][0] == before_a[1][0] and after_a[1][1] == "hash-a1-new"
    assert after_a[1][3] == {}, after_a[1][3]
    missing = {r["chunk_index"]: r["existing_model"] for r in get_chunks_for_embedding(conn, a, MODEL)}
    assert missing == {0: MODEL, 1: None}, missing
    # AC3: tail gone with its page and vector rows (cascade); paper B untouched.
    assert sorted(after_a) == [0, 1]
    assert orphans == (0, 0, 0), orphans
    assert after_b == before_b

    # AC3 with a non-contiguous set: index 0 dropped, 1 kept (a `chunk_index > max` bound would keep 0).
    save_sections_and_chunks(conn, a, SECTIONS, [_chunk(1, "a1-new", 1)])
    with conn.cursor() as cur:
        again = _state(cur, a)
        assert sorted(again) == [1] and again[1] == after_a[1], again
        assert _orphans(cur, [after_a[0][0]]) == (0, 0, 0)
        assert _state(cur, b) == before_b, "tail delete must be scoped to the paper (B's index 0 is not kept for A)"

    # AC3: an empty chunk list leaves the paper with no chunks; paper B still untouched.
    assert save_sections_and_chunks(conn, a, SECTIONS, []) == 0
    with conn.cursor() as cur:
        assert _state(cur, a) == {}
        assert _state(cur, b) == before_b
        cur.execute("SELECT status FROM papers WHERE id = %s;", (a,))
        assert cur.fetchone()[0] == "chunked"

"""OPS-H6d: re-saving a paper's chunks drops stale vectors and stale tail chunks (real Postgres, migrations applied).
OPS-H6e: the embedder's save writes a vector only while its chunk still exists with the hash it was computed from.
OPS-H6f: a processor re-save racing an embedder save (two sessions) never leaves an old-text vector under the new hash.

Not in `make test-py` (needs a DB); runs in `make test-full`. The OPS-H6d/H6e tests run on one `autocommit=False`
connection that is always rolled back. The OPS-H6f tests need rows that two sessions can see, so they commit two papers
(`ops-h6f-a`, `ops-h6f-b`) and delete them in a `finally`; a module fixture checks that the `papers`, `paper_chunks` and
`chunk_embeddings` counts the M2/M3 suites rely on are the same after the module as before it. Every session has a
`lock_timeout` and every thread join a timeout, so a broken lock fails the test instead of hanging CI.
"""
import threading
import time
import types

import pytest

from services.common.db import get_chunks_for_embedding, get_db_connection, save_chunk_embeddings, save_sections_and_chunks

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


def _counts():
    c = get_db_connection()
    try:
        with c.cursor() as cur:
            cur.execute(
                "SELECT (SELECT count(*) FROM papers), (SELECT count(*) FROM paper_chunks),"
                " (SELECT count(*) FROM chunk_embeddings);"
            )
            return cur.fetchone()
    finally:
        c.rollback()
        c.close()


@pytest.fixture(scope="module", autouse=True)
def db_left_as_found():
    before = _counts()
    yield
    assert _counts() == before, "this module must leave the shared stack DB as it found it"


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


def _floats(hot: int) -> list:
    return [1.0 if i == hot else 0.0 for i in range(768)]


def test_save_embeddings_only_for_current_chunk_text(conn):
    with conn.cursor() as cur:
        a = _new_paper(cur, "ops-h6e-a")
    save_sections_and_chunks(conn, a, SECTIONS, [_chunk(0, "a0"), _chunk(1, "a1"), _chunk(2, "a2")])
    with conn.cursor() as cur:
        ids = {k: v[0] for k, v in _state(cur, a).items()}
    # The processor re-parses while an embedder run is in flight: chunk 1 gets new text, chunk 2 is dropped.
    save_sections_and_chunks(conn, a, SECTIONS, [_chunk(0, "a0"), _chunk(1, "a1-new")])
    with conn.cursor() as cur:
        # Give chunk 1 a vector (explicit OLD_TS), so AC3 can show the skip leaves an existing row exactly as it was.
        cur.execute(
            "INSERT INTO chunk_embeddings (chunk_id, model, embedding, created_at) VALUES (%s, %s, %s::halfvec, %s);",
            (ids[1], MODEL, _vec(1), OLD_TS),
        )
        kept = _state(cur, a)[1][3][MODEL]

    # AC2: matching hash -> inserted, then updated on conflict; both counted.
    assert save_chunk_embeddings(conn, [(ids[0], MODEL, _floats(5), "hash-a0")]) == 1
    with conn.cursor() as cur:
        assert _state(cur, a)[0][3][MODEL][0] == _vec(5)
    assert save_chunk_embeddings(conn, [(ids[0], MODEL, _floats(6), "hash-a0")]) == 1
    with conn.cursor() as cur:
        assert _state(cur, a)[0][3][MODEL][0] == _vec(6)

    # AC3: the old text's hash no longer matches -> not written, not counted, existing vector untouched.
    assert save_chunk_embeddings(conn, [(ids[1], MODEL, _floats(7), "hash-a1")]) == 0
    assert save_chunk_embeddings(conn, [(ids[1], OTHER_MODEL, _floats(7), "hash-a1")]) == 0
    with conn.cursor() as cur:
        assert _state(cur, a)[1][3] == {MODEL: kept}

    # AC4: chunk 2 was deleted by the tail delete -> skipped without an FK error; the transaction is still usable.
    assert save_chunk_embeddings(conn, [(ids[2], MODEL, _floats(8), "hash-a2")]) == 0
    with conn.cursor() as cur:
        assert _orphans(cur, [ids[2]]) == (0, 0, 0)

    # Mixed batch in one call: only the current chunk and the 3-tuple are written and counted.
    n = save_chunk_embeddings(
        conn,
        [
            (ids[2], MODEL, _floats(9), "hash-a2"),
            (ids[1], MODEL, _floats(9), "hash-a1"),
            (ids[0], OTHER_MODEL, _floats(9), "hash-a0"),
            (ids[0], "ops-h6e-3tuple", _floats(9)),
        ],
    )
    assert n == 2
    with conn.cursor() as cur:
        after = _state(cur, a)
    assert sorted(after) == [0, 1]
    assert after[0][3][MODEL][0] == _vec(6) and after[0][3][OTHER_MODEL][0] == _vec(9)
    assert after[0][3]["ops-h6e-3tuple"][0] == _vec(9)
    assert after[1][3] == {MODEL: kept}


# --- OPS-H6f: two sessions, processor (P) and embedder (E), on rows both can see ---------------------------------------

TIMEOUT_S = 10
H6F_PAPERS = ["ops-h6f-a", "ops-h6f-b"]


def _setting_timeouts(c):
    with c.cursor() as cur:
        cur.execute(f"SET lock_timeout = '{TIMEOUT_S}s';")  # session level once committed
    c.commit()
    return c


@pytest.fixture
def committed():
    """Paper A (chunks a0, a1, a2) and paper B (one chunk with A's chunk-1 content_hash), committed; removed in finally."""
    setup, sessions = _setting_timeouts(get_db_connection()), []

    def session():
        sessions.append(_setting_timeouts(get_db_connection()))
        return sessions[-1]

    try:
        with setup.cursor() as cur:
            a, b = _new_paper(cur, H6F_PAPERS[0]), _new_paper(cur, H6F_PAPERS[1])
        save_sections_and_chunks(setup, a, SECTIONS, [_chunk(0, "a0"), _chunk(1, "a1"), _chunk(2, "a2")])
        save_sections_and_chunks(setup, b, SECTIONS, [_chunk(0, "a1")])  # AC6: same hash on another paper
        setup.commit()
        with setup.cursor() as cur:
            ids, b_id = {k: v[0] for k, v in _state(cur, a).items()}, _state(cur, b)[0][0]
        setup.rollback()
        yield types.SimpleNamespace(a=a, b=b, ids=ids, b_id=b_id, setup=setup, session=session)
    finally:
        for c in sessions:  # releases any lock first, so a blocked thread finishes (or hits lock_timeout)
            c.rollback()
            c.close()
        setup.rollback()
        with setup.cursor() as cur:
            cur.execute("DELETE FROM papers WHERE arxiv_id = ANY(%s::text[]);", (H6F_PAPERS,))  # cascades
        setup.commit()
        setup.close()


def _pid(c):
    with c.cursor() as cur:
        cur.execute("SELECT pg_backend_pid();")
        return cur.fetchone()[0]


def _blocked_on_lock(observer, pid):
    """True once backend `pid` waits on a lock (polled, fresh snapshot each time); False after TIMEOUT_S."""
    deadline = time.monotonic() + TIMEOUT_S
    while time.monotonic() < deadline:
        with observer.cursor() as cur:
            cur.execute("SELECT wait_event_type FROM pg_stat_activity WHERE pid = %s;", (pid,))
            row = cur.fetchone()
        observer.rollback()
        if row and row[0] == "Lock":
            return True
        time.sleep(0.05)
    return False


def _in_thread(fn, *args):
    out = {}

    def run():
        try:
            out["value"] = fn(*args)
        except BaseException as e:  # surfaced by the test's assertion
            out["error"] = e

    t = threading.Thread(target=run, daemon=True)
    t.start()
    return t, out


def _final(k):
    with k.setup.cursor() as cur:
        a, b = _state(cur, k.a), _state(cur, k.b)
    k.setup.rollback()
    return a, b


def test_processor_resave_waits_for_inflight_embedder_write(committed):
    k = committed
    e, p = k.session(), k.session()
    # AC4: E embedded the old text of every chunk and is saving it (ascending, as the worker does), transaction open.
    batch = [(k.ids[i], MODEL, _floats(3), f"hash-a{i}") for i in (0, 1, 2)]
    assert save_chunk_embeddings(e, batch) == 3
    # P re-parses: chunks 0 and 1 get new text (0 is the first vector DELETE, so a lock placed after it is caught), 2 is kept.
    p_pid = _pid(p)
    resave = [_chunk(0, "a0-new"), _chunk(1, "a1-new"), _chunk(2, "a2")]
    t, out = _in_thread(save_sections_and_chunks, p, k.a, SECTIONS, resave)
    assert _blocked_on_lock(k.setup, p_pid), "the processor's re-save must wait for the embedder's open write"
    e.commit()
    t.join(2 * TIMEOUT_S)
    assert not t.is_alive() and out.get("value") == 3, out
    p.commit()
    final_a, final_b = _final(k)
    for i in (0, 1):
        assert final_a[i][0] == k.ids[i] and final_a[i][1] == f"hash-a{i}-new", final_a[i][:2]
        assert final_a[i][3] == {}, f"chunk {i}: old-text vector kept under the new hash, models: {sorted(final_a[i][3])}"
    assert final_a[2][1] == "hash-a2" and set(final_a[2][3]) == {MODEL}, "unchanged chunk keeps E's vector"
    assert final_b[0][0] == k.b_id and final_b[0][1] == "hash-a1" and final_b[0][3] == {}, final_b


def test_embedder_write_waits_for_processor_resave_then_skips(committed):
    k = committed
    e, p = k.session(), k.session()
    # AC5: P has re-saved (chunk 0 new text, 1 and 2 unchanged), transaction open; E then saves the old text's vectors.
    resave = [_chunk(0, "a0-new"), _chunk(1, "a1"), _chunk(2, "a2")]
    assert save_sections_and_chunks(p, k.a, SECTIONS, resave) == 3
    e_pid = _pid(e)
    # Stale item first: its statement starts before P commits, so only FOR SHARE's re-check can reject it.
    batch = [(k.ids[0], MODEL, _floats(4), "hash-a0"), (k.ids[1], MODEL, _floats(4), "hash-a1")]
    t, out = _in_thread(save_chunk_embeddings, e, batch)
    assert _blocked_on_lock(k.setup, e_pid), "the embedder's save must wait for the processor's open re-save"
    p.commit()
    t.join(2 * TIMEOUT_S)
    assert not t.is_alive() and out.get("value") == 1, out  # chunk 0 re-checked: no row, no error; chunk 1 still current
    e.commit()
    final_a, final_b = _final(k)
    assert final_a[0][1] == "hash-a0-new" and final_a[0][3] == {}, final_a[0][:2] + (sorted(final_a[0][3]),)
    assert set(final_a[1][3]) == {MODEL} and final_a[1][3][MODEL][0] == _vec(4)
    assert final_a[2][3] == {}
    assert final_b[0][3] == {}


def test_save_embeddings_keyed_on_chunk_id_not_hash(committed):
    k = committed
    e = k.session()
    # AC6: paper B's chunk shares A's chunk-1 hash. A hash paired with the wrong chunk id writes nothing, either way round.
    assert save_chunk_embeddings(e, [(k.ids[0], MODEL, _floats(1), "hash-a1"), (k.b_id, MODEL, _floats(1), "hash-a0")]) == 0
    assert save_chunk_embeddings(e, [(k.ids[1], MODEL, _floats(2), "hash-a1")]) == 1
    with e.cursor() as cur:
        a, b = _state(cur, k.a), _state(cur, k.b)
    assert a[0][3] == {} and set(a[1][3]) == {MODEL} and a[1][3][MODEL][0] == _vec(2), a
    assert b[0][3] == {}, b

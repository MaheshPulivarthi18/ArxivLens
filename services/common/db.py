import json
from typing import List, Dict, Any, Optional, Tuple, Union
import psycopg
from psycopg.rows import dict_row
from services.common.config import config


def get_db_connection() -> psycopg.Connection:
    return psycopg.connect(config.postgres_url, autocommit=False)


def get_paper_record(conn: psycopg.Connection, arxiv_id: str, version: int = 1) -> Optional[Dict[str, Any]]:
    with conn.cursor(row_factory=dict_row) as cur:
        cur.execute(
            """
            SELECT id, arxiv_id, version, title, abstract, authors, categories,
                   pdf_storage_key, sha256, status
            FROM papers
            WHERE arxiv_id = %s AND version = %s;
            """,
            (arxiv_id, version),
        )
        return cur.fetchone()


def update_paper_status(
    conn: psycopg.Connection,
    paper_id: int,
    status: str,
    error_msg: Optional[str] = None,
) -> None:
    with conn.cursor() as cur:
        cur.execute(
            """
            UPDATE papers
            SET status = %s,
                status_error = %s,
                updated_at = now()
            WHERE id = %s;
            """,
            (status, error_msg, paper_id),
        )


def check_and_record_ledger(
    conn: psycopg.Connection,
    consumer_group: str,
    event_id: str,
) -> bool:
    """
    Transactional ledger check (Layer 2 Idempotency).
    Returns True if event is NEW, False if DUPLICATE.
    """
    with conn.cursor() as cur:
        cur.execute(
            """
            INSERT INTO processed_events (consumer_group, event_id)
            VALUES (%s, %s)
            ON CONFLICT (consumer_group, event_id) DO NOTHING;
            """,
            (consumer_group, event_id),
        )
        return cur.rowcount > 0


def save_sections_and_chunks(
    conn: psycopg.Connection,
    paper_id: int,
    sections: List[Dict[str, Any]],
    chunks: List[Dict[str, Any]],
) -> int:
    """
    Upserts sections and chunks within an active transaction.
    Returns total chunk count.
    """
    with conn.cursor() as cur:
        # 1. Upsert sections
        section_id_map: Dict[str, int] = {}
        for pos, sec in enumerate(sections):
            cur.execute(
                """
                INSERT INTO paper_sections (paper_id, title, level, position)
                VALUES (%s, %s, %s, %s)
                ON CONFLICT (paper_id, position) DO UPDATE
                SET title = EXCLUDED.title,
                    level = EXCLUDED.level
                RETURNING id;
                """,
                (paper_id, sec["title"], sec.get("level", 1), pos),
            )
            sec_id = cur.fetchone()[0]
            section_id_map[sec["title"]] = sec_id

        # 2. Upsert chunks
        # OPS-H6f: lock the paper's existing chunk rows first. An embedder save (FOR SHARE, OPS-H6e) that got in earlier
        # commits before this returns, so the DELETE below sees and drops its vector; one that comes later waits for our
        # commit and re-checks the new hash. Ascending chunk_index, the embedder's order, so the two can't deadlock.
        cur.execute("SELECT id FROM paper_chunks WHERE paper_id = %s ORDER BY chunk_index FOR UPDATE;", (paper_id,))
        for chunk in chunks:
            sec_title = chunk.get("section_title", "")
            sec_id = section_id_map.get(sec_title)

            # OPS-H6d: a re-parse changed this chunk's text, so its vectors (every model) describe the old text. Drop
            # them so the embedder's L3 check re-embeds the chunk. Must run before the upsert overwrites content_hash.
            cur.execute(
                """
                DELETE FROM chunk_embeddings ce
                USING paper_chunks pc
                WHERE ce.chunk_id = pc.id
                  AND pc.paper_id = %s
                  AND pc.chunk_index = %s
                  AND pc.content_hash <> %s;
                """,
                (paper_id, chunk["chunk_index"], chunk["content_hash"]),
            )
            cur.execute(
                """
                INSERT INTO paper_chunks (
                    paper_id, section_id, chunk_index, content,
                    content_hash, page_number, token_count
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s)
                ON CONFLICT (paper_id, chunk_index) DO UPDATE
                SET content = EXCLUDED.content,
                    content_hash = EXCLUDED.content_hash,
                    page_number = EXCLUDED.page_number,
                    token_count = EXCLUDED.token_count,
                    section_id = EXCLUDED.section_id
                RETURNING id;
                """,
                (
                    paper_id,
                    sec_id,
                    chunk["chunk_index"],
                    chunk["content"],
                    chunk["content_hash"],
                    chunk.get("page_number", 1),
                    chunk.get("token_count", 0),
                ),
            )
            chunk_id = cur.fetchone()[0]
            # ING-11: no row = page unknown. Same transaction as the chunk and the L2 ledger row.
            if chunk.get("page_start") is not None:
                cur.execute(
                    """
                    INSERT INTO paper_chunk_pages (chunk_id, page_start) VALUES (%s, %s)
                    ON CONFLICT (chunk_id) DO UPDATE SET page_start = EXCLUDED.page_start;
                    """,
                    (chunk_id, chunk["page_start"]),
                )
            else:  # a reprocess without a recorded page must not keep a stale one
                cur.execute("DELETE FROM paper_chunk_pages WHERE chunk_id = %s;", (chunk_id,))

        # 3. OPS-H6d: drop this paper's chunks that the new parse no longer has (a shorter re-parse). The FK cascades
        # remove their vectors and page rows. An empty list matches nothing, so the paper is left with no chunks.
        cur.execute(
            "DELETE FROM paper_chunks WHERE paper_id = %s AND NOT (chunk_index = ANY(%s::int[]));",
            (paper_id, [c["chunk_index"] for c in chunks]),
        )

        # 4. Update status to 'chunked'
        cur.execute(
            "UPDATE papers SET status = 'chunked', updated_at = now() WHERE id = %s;",
            (paper_id,),
        )

    return len(chunks)


def get_chunks_for_embedding(
    conn: psycopg.Connection,
    paper_id: int,
    model_name: str,
) -> List[Dict[str, Any]]:
    """
    Returns chunks for a paper. Used by embedder with content_hash skip check (Layer 3).
    """
    with conn.cursor(row_factory=dict_row) as cur:
        cur.execute(
            """
            SELECT pc.id AS chunk_id, pc.chunk_index, pc.content, pc.content_hash,
                   ce.model AS existing_model
            FROM paper_chunks pc
            LEFT JOIN chunk_embeddings ce
              ON ce.chunk_id = pc.id AND ce.model = %s
            WHERE pc.paper_id = %s
            ORDER BY pc.chunk_index ASC;
            """,
            (model_name, paper_id),
        )
        return cur.fetchall()


_UPSERT_EMBEDDING_SQL = """
    INSERT INTO chunk_embeddings (chunk_id, model, embedding)
    VALUES (%s, %s, %s::halfvec)
    ON CONFLICT (chunk_id, model) DO UPDATE
    SET embedding = EXCLUDED.embedding,
        created_at = now();
"""

# OPS-H6e: write the vector only if the chunk still exists with the hash it was computed from. FOR SHARE makes a
# concurrent processor UPDATE (new hash) or tail DELETE of the row finish first; READ COMMITTED then re-checks the WHERE
# on the committed row, so a changed hash or a deleted chunk yields no row: nothing is written and no FK error is raised.
_UPSERT_EMBEDDING_IF_CURRENT_SQL = """
    INSERT INTO chunk_embeddings (chunk_id, model, embedding)
    SELECT pc.id, %s::text, %s::halfvec
    FROM paper_chunks pc
    WHERE pc.id = %s AND pc.content_hash = %s
    FOR SHARE OF pc
    ON CONFLICT (chunk_id, model) DO UPDATE
    SET embedding = EXCLUDED.embedding,
        created_at = now();
"""


def save_chunk_embeddings(
    conn: psycopg.Connection,
    chunk_embeddings: List[Union[Tuple[int, str, List[float]], Tuple[int, str, List[float], str]]],
) -> int:
    """
    Inserts or updates halfvec embeddings, inside the caller's transaction (no commit).

    Each item is `(chunk_id, model, vector)`, written unconditionally, or `(chunk_id, model, vector, content_hash)`,
    written only while `paper_chunks` still has that id with that content_hash (OPS-H6e); otherwise it is skipped and
    any existing vector is left alone. Returns the number of vectors written.
    """
    written = 0
    with conn.cursor() as cur:
        for item in chunk_embeddings:
            if len(item) not in (3, 4):
                raise ValueError(f"expected (chunk_id, model, vec[, content_hash]), got {len(item)} elements")
            chunk_id, model, vec = item[0], item[1], item[2]
            # Format vector as string for PostgreSQL halfvec: '[0.123, -0.456, ...]'
            vec_str = "[" + ",".join(f"{x:.6f}" for x in vec) + "]"
            if len(item) == 3:
                cur.execute(_UPSERT_EMBEDDING_SQL, (chunk_id, model, vec_str))
                written += 1
            else:
                cur.execute(_UPSERT_EMBEDDING_IF_CURRENT_SQL, (model, vec_str, chunk_id, item[3]))
                written += cur.rowcount
    return written

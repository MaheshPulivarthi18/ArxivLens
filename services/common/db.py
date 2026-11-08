import json
from typing import List, Dict, Any, Optional, Tuple
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
        for chunk in chunks:
            sec_title = chunk.get("section_title", "")
            sec_id = section_id_map.get(sec_title)

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

        # 3. Update status to 'chunked'
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


def save_chunk_embeddings(
    conn: psycopg.Connection,
    chunk_embeddings: List[Tuple[int, str, List[float]]],
) -> int:
    """
    Inserts or updates halfvec embeddings for a list of (chunk_id, model, vector_float_list).
    """
    with conn.cursor() as cur:
        for chunk_id, model, vec in chunk_embeddings:
            # Format vector as string for PostgreSQL halfvec: '[0.123, -0.456, ...]'
            vec_str = "[" + ",".join(f"{x:.6f}" for x in vec) + "]"
            cur.execute(
                """
                INSERT INTO chunk_embeddings (chunk_id, model, embedding)
                VALUES (%s, %s, %s::halfvec)
                ON CONFLICT (chunk_id, model) DO UPDATE
                SET embedding = EXCLUDED.embedding,
                    created_at = now();
                """,
                (chunk_id, model, vec_str),
            )
    return len(chunk_embeddings)

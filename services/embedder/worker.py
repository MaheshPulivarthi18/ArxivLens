#!/usr/bin/env python3
"""
ArxivLens — Embedder Service Worker
Consumes: papers.chunked
Role: Batch embeddings using BAAI/bge-base-en-v1.5 (MPS/CPU) into PostgreSQL chunk_embeddings table.
"""

import logging
from typing import Dict, Any, List, Tuple
from confluent_kafka import Message

from scripts.m0_skeleton import BGEEmbedder
from services.common.config import config
from services.common.db import (
    get_db_connection,
    get_chunks_for_embedding,
    save_chunk_embeddings,
    update_paper_status,
    check_and_record_ledger,
)
from services.common.kafka_client import ServiceKafkaConsumer
from shared.events.events import (
    EventEnvelope,
    TOPIC_PAPERS_CHUNKED,
)

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("embedder")


class EmbedderWorker:
    def __init__(self):
        self.embedder = BGEEmbedder(model_name=config.embedding_model)
        self.model_name = config.embedding_model
        self.consumer_group = "embedder-v1"
        self.batch_size = 32

    def process_message(self, envelope: EventEnvelope, msg: Message) -> None:
        arxiv_id = envelope.paper_id
        version = envelope.version or 1
        paper_db_id = envelope.payload.get("paper_db_id")

        logger.info(f"Processing embedding for paper {arxiv_id} (paper_db_id={paper_db_id})...")

        conn = get_db_connection()
        try:
            # 1. Fetch chunks for paper
            chunks = get_chunks_for_embedding(conn, paper_db_id, self.model_name)
            if not chunks:
                logger.warning(f"No chunks found for paper_id={paper_db_id} ({arxiv_id})")
                return

            logger.info(f"Retrieved {len(chunks)} chunks for {arxiv_id}")

            # 2. Content Hashing Check (Layer 3 Idempotency)
            # Find chunks that actually need embedding
            chunks_to_embed = [c for c in chunks if c.get("existing_model") is None]

            if not chunks_to_embed:
                logger.info(f"All {len(chunks)} chunks already embedded with {self.model_name}. Skipping compute.")
            else:
                logger.info(f"Embedding {len(chunks_to_embed)} new/updated chunks in batches of {self.batch_size}...")
                embeddings_to_save: List[Tuple[int, str, List[float]]] = []

                for i in range(0, len(chunks_to_embed), self.batch_size):
                    batch = chunks_to_embed[i : i + self.batch_size]
                    batch_texts = [c["content"] for c in batch]
                    vectors = self.embedder.embed_texts(batch_texts, is_query=False)

                    for j, c in enumerate(batch):
                        vec_list = vectors[j].tolist()
                        embeddings_to_save.append((c["chunk_id"], self.model_name, vec_list))

                # 3. Save batch to database
                save_chunk_embeddings(conn, embeddings_to_save)
                logger.info(f"Successfully saved {len(embeddings_to_save)} embeddings for {arxiv_id}")

            # 4. Update status to 'embedded'
            update_paper_status(conn, paper_db_id, status="embedded")
            conn.commit()
            logger.info(f"Paper {arxiv_id} fully indexed and marked as 'embedded'!")

        except Exception as e:
            conn.rollback()
            raise e
        finally:
            conn.close()


def main():
    worker = EmbedderWorker()
    consumer = ServiceKafkaConsumer(
        group_id=worker.consumer_group,
        topics=[TOPIC_PAPERS_CHUNKED],
        max_poll_records=1,
    )
    logger.info("Embedder worker started. Listening for papers.chunked...")
    consumer.start(worker.process_message)


if __name__ == "__main__":
    main()

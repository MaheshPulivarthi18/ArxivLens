#!/usr/bin/env python3
"""
ArxivLens — Processor Service Worker
Consumes: papers.downloaded
Produces: papers.chunked
Role: PDF Parsing (PyMuPDF typography) + Token Chunking (BGE tokenizer + section prefixing)
"""

import io
import os
import sys
import logging
from typing import Dict, Any
from confluent_kafka import Message

from scripts.m0_skeleton import PDF_TEXT_FLAGS, PaperParser, TokenChunker
from services.common.config import config
from services.common.db import (
    get_db_connection,
    get_paper_record,
    save_sections_and_chunks,
    check_and_record_ledger,
)
from services.common.storage import StorageClient
from services.common.kafka_client import ServiceKafkaConsumer, ServiceKafkaProducer
from shared.events.events import (
    EventEnvelope,
    TOPIC_PAPERS_DOWNLOADED,
    TOPIC_PAPERS_CHUNKED,
)

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("processor")


class ProcessorWorker:
    def __init__(self):
        self.storage = StorageClient()
        self.parser = PaperParser()
        self.chunker = TokenChunker(model_name=config.embedding_model)
        self.producer = ServiceKafkaProducer(config.kafka_brokers)
        self.consumer_group = "processor-v1"

    def process_message(self, envelope: EventEnvelope, msg: Message) -> None:
        arxiv_id = envelope.paper_id
        version = envelope.version or 1
        storage_key = envelope.payload.get("pdf_storage_key") or f"raw/{arxiv_id}_v{version}.pdf"

        logger.info(f"Processing paper {arxiv_id} (v{version}) from MinIO key {storage_key}...")

        # 1. Download PDF bytes from MinIO claim-check
        pdf_bytes = self.storage.download_pdf_bytes(storage_key)
        logger.info(f"Downloaded PDF {storage_key} ({len(pdf_bytes)} bytes)")

        # 2. Parse typography & sections from PDF
        # We can pass temp file or io buffer to PyMuPDF
        import pymupdf
        doc = pymupdf.open(stream=pdf_bytes, filetype="pdf")

        # Extract paper record from DB for title
        conn = get_db_connection()
        try:
            paper_rec = get_paper_record(conn, arxiv_id, version)
            if not paper_rec:
                raise ValueError(f"Paper record not found in DB for {arxiv_id} v{version}")
            paper_id = paper_rec["id"]
            paper_title = paper_rec["title"] or "Untitled"

            # Parse sections
            sections = self._parse_doc_sections(doc)
            doc.close()
            logger.info(f"Extracted {len(sections)} sections for {arxiv_id}")

            # 3. Chunk sections
            chunks = self.chunker.chunk_sections(paper_title, sections)
            logger.info(f"Produced {len(chunks)} token-bounded chunks for {arxiv_id}")

            # 4. Transactional Ledger Check & Write (Layer 2 Idempotency)
            is_new = check_and_record_ledger(conn, self.consumer_group, envelope.event_id)
            if is_new:
                chunk_count = save_sections_and_chunks(conn, paper_id, sections, chunks)
                conn.commit()
                logger.info(f"Successfully saved {chunk_count} chunks to DB for {arxiv_id}")
            else:
                conn.rollback()
                logger.warning(
                    f"Duplicate event {envelope.event_id} for {arxiv_id} — skipped DB write, proceeding to re-emit downstream (§4)"
                )
                chunk_count = len(chunks)

            # 5. Publish papers.chunked event
            payload = {
                "paper_db_id": paper_id,
                "arxiv_id": arxiv_id,
                "version": version,
                "chunk_count": chunk_count,
            }
            self.producer.publish(
                topic=TOPIC_PAPERS_CHUNKED,
                event_type=TOPIC_PAPERS_CHUNKED,
                paper_id=arxiv_id,
                version=version,
                payload=payload,
                traceparent=envelope.traceparent,
            )

        except Exception as e:
            conn.rollback()
            raise e
        finally:
            conn.close()

    def _parse_doc_sections(self, doc) -> list:
        import numpy as np
        import re

        sections = []
        current_section = {"title": "Abstract", "level": 1, "text": "", "page": 1, "page_breaks": []}

        font_sizes = []
        for page in doc:
            blocks = page.get_text("dict", flags=PDF_TEXT_FLAGS)["blocks"]
            for b in blocks:
                if b.get("type") == 0:
                    for line in b.get("lines", []):
                        for span in line.get("spans", []):
                            if span.get("text", "").strip():
                                font_sizes.append(span.get("size", 10.0))

        base_font_size = float(np.median(font_sizes)) if font_sizes else 10.0
        heading_threshold = base_font_size * 1.15

        for page_idx, page in enumerate(doc):
            blocks = page.get_text("dict", flags=PDF_TEXT_FLAGS)["blocks"]
            for b in blocks:
                if b.get("type") != 0:
                    continue

                for line in b.get("lines", []):
                    # Spans carry their own edge spaces; collapse every whitespace run to one space.
                    line_text = " ".join(" ".join(span.get("text", "") for span in line.get("spans", [])).split())
                    if not line_text:
                        continue

                    max_span_size = max([span.get("size", 0.0) for span in line.get("spans", [])], default=0.0)
                    is_bold = any("bold" in span.get("font", "").lower() for span in line.get("spans", []))
                    is_numbered_heading = bool(re.match(r"^(\d+\.?\d*|[IVXLCDM]+\.)\s+[A-Z]", line_text))

                    is_heading = (
                        max_span_size >= heading_threshold
                        or is_numbered_heading
                        or (is_bold and len(line_text) < 60)
                    ) and len(line_text) < 100

                    if is_heading:
                        norm_title = re.sub(r"^(\d+\.?\d*|[IVXLCDM]+\.)\s*", "", line_text).strip()
                        if current_section["text"].strip():
                            sections.append(current_section)
                        current_section = {
                            "title": norm_title or line_text,
                            "level": 1 if max_span_size >= base_font_size * 1.3 else 2,
                            "text": "",
                            "page": page_idx + 1,
                            "page_breaks": [],  # (offset into text, 1-based page) where each page's text starts (ING-11)
                        }
                    else:
                        pb = current_section["page_breaks"]
                        if not pb or pb[-1][1] != page_idx + 1:
                            pb.append((len(current_section["text"]), page_idx + 1))
                        current_section["text"] += line_text + " "

        if current_section["text"].strip():
            sections.append(current_section)

        return sections


def main():
    worker = ProcessorWorker()
    consumer = ServiceKafkaConsumer(
        group_id=worker.consumer_group,
        topics=[TOPIC_PAPERS_DOWNLOADED],
        max_poll_records=1,
    )
    logger.info("Processor worker started. Listening for papers.downloaded...")
    consumer.start(worker.process_message)


if __name__ == "__main__":
    main()

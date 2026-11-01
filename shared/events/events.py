"""
Shared Event Envelope and Models for ArxivLens (Python).
Must match shared/events/events.go exactly.
"""

from datetime import datetime, timezone
from typing import Dict, Any, List, Optional
from pydantic import BaseModel, Field


# Standard topic names matching §2
TOPIC_PAPERS_DISCOVERED = "papers.discovered"
TOPIC_PAPERS_DISCOVERED_DLQ = "papers.discovered.dlq"
TOPIC_PAPERS_DOWNLOADED = "papers.downloaded"
TOPIC_PAPERS_DOWNLOADED_DLQ = "papers.downloaded.dlq"
TOPIC_PAPERS_CHUNKED = "papers.chunked"
TOPIC_PAPERS_CHUNKED_DLQ = "papers.chunked.dlq"


class EventEnvelope(BaseModel):
    event_id: str
    event_type: str
    occurred_at: str
    paper_id: str
    version: int = 1
    attempt: int = 0
    traceparent: Optional[str] = None
    payload: Dict[str, Any] = Field(default_factory=dict)


class DLQPayload(BaseModel):
    original_topic: str
    partition: int
    offset: int
    original_key: str
    original_value: Dict[str, Any]
    error_type: str
    error_message: str
    stack_trace: str
    attempts: int
    consumer_group: str
    failed_at: str
    traceparent: Optional[str] = None


class PaperDiscoveredPayload(BaseModel):
    arxiv_id: str
    version: int = 1
    title: str
    abstract: str
    authors: List[str]
    categories: List[str]
    published_at: str
    source_url: str


class PaperDownloadedPayload(BaseModel):
    arxiv_id: str
    version: int = 1
    pdf_storage_key: str
    sha256: str
    bytes: int
    storage_bucket: str = "arxiv-papers"


class PaperChunkedPayload(BaseModel):
    paper_db_id: int
    arxiv_id: str
    version: int = 1
    chunk_count: int

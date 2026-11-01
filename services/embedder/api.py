#!/usr/bin/env python3
"""
ArxivLens — Embedder Internal HTTP API
Provides low-latency embedding and cross-encoder reranking over HTTP for the Go Query Service.
Endpoints:
  POST /internal/embed   {"texts": [...], "is_query": bool} -> {"vectors": [[...]]}
  POST /internal/rerank  {"query": "...", "passages": [...]} -> {"scores": [...]}
  GET  /healthz          -> {"status": "ok"}
"""

import logging
from typing import List, Optional
import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

import torch
from transformers import AutoTokenizer, AutoModelForSequenceClassification
from scripts.m0_skeleton import BGEEmbedder
from services.common.config import config

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("embedder-api")

app = FastAPI(title="ArxivLens Embedder & Reranker API", version="1.0.0")

# Initialize model singletons on startup
embedder: Optional[BGEEmbedder] = None
reranker_tokenizer = None
reranker_model = None


@app.on_event("startup")
def load_models():
    global embedder, reranker_tokenizer, reranker_model
    logger.info(f"Loading embedding model {config.embedding_model} on device...")
    embedder = BGEEmbedder(model_name=config.embedding_model)
    logger.info(f"Model loaded successfully on {embedder.device}")

    try:
        logger.info("Loading Cross-Encoder Reranker BAAI/bge-reranker-base...")
        reranker_tokenizer = AutoTokenizer.from_pretrained("BAAI/bge-reranker-base")
        reranker_model = AutoModelForSequenceClassification.from_pretrained("BAAI/bge-reranker-base")
        reranker_model.eval()
        logger.info("Cross-Encoder Reranker loaded successfully!")
    except Exception as e:
        logger.error(f"Warning: Failed to load cross-encoder ({e}), falling back to bi-encoder")


class EmbedRequest(BaseModel):
    texts: List[str]
    is_query: bool = False


class EmbedResponse(BaseModel):
    vectors: List[List[float]]
    dim: int


class RerankRequest(BaseModel):
    query: str
    passages: List[str]


class RerankResponse(BaseModel):
    scores: List[float]


@app.get("/healthz")
def healthz():
    return {"status": "ok", "device": embedder.device if embedder else "uninitialized", "reranker": reranker_model is not None}


@app.post("/internal/embed", response_model=EmbedResponse)
def embed_texts_endpoint(req: EmbedRequest):
    if not req.texts:
        return EmbedResponse(vectors=[], dim=768)

    if embedder is None:
        raise HTTPException(status_code=503, detail="Embedder model not ready")

    try:
        vectors = embedder.embed_texts(req.texts, is_query=req.is_query)
        vec_list = [v.tolist() for v in vectors]
        return EmbedResponse(vectors=vec_list, dim=vectors.shape[1])
    except Exception as e:
        logger.error(f"Error in embed endpoint: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/internal/rerank", response_model=RerankResponse)
def rerank_endpoint(req: RerankRequest):
    if not req.passages:
        return RerankResponse(scores=[])

    try:
        if reranker_model is not None and reranker_tokenizer is not None:
            pairs = [[req.query, p] for p in req.passages]
            with torch.no_grad():
                inputs = reranker_tokenizer(pairs, padding=True, truncation=True, return_tensors="pt", max_length=512)
                scores = reranker_model(**inputs, return_dict=True).logits.view(-1, ).float()
                probs = torch.sigmoid(scores).tolist()
                if isinstance(probs, float):
                    probs = [probs]
                return RerankResponse(scores=[float(s) for s in probs])
        elif embedder is not None:
            q_vec = embedder.embed_texts([req.query], is_query=True)[0]
            p_vecs = embedder.embed_texts(req.passages, is_query=False)
            scores = [float(np.dot(q_vec, pv)) for pv in p_vecs]
            return RerankResponse(scores=scores)
        else:
            raise HTTPException(status_code=503, detail="Reranker models not ready")
    except Exception as e:
        logger.error(f"Error in rerank endpoint: {e}")
        raise HTTPException(status_code=500, detail=str(e))


import ulid
from services.processor.worker import ProcessorWorker
from services.embedder.worker import EmbedderWorker
from shared.events.events import EventEnvelope, TOPIC_PAPERS_DOWNLOADED, TOPIC_PAPERS_CHUNKED

class IngestProcessRequest(BaseModel):
    arxiv_id: str
    version: int = 1
    pdf_storage_key: str

class IngestProcessResponse(BaseModel):
    status: str
    arxiv_id: str
    sections: int
    chunks: int
    embeddings: int

@app.post("/internal/ingest", response_model=IngestProcessResponse)
def ingest_paper_endpoint(req: IngestProcessRequest):
    try:
        # Step 1: Typography chunking
        processor = ProcessorWorker()
        download_envelope = EventEnvelope(
            event_id=str(ulid.new()),
            event_type=TOPIC_PAPERS_DOWNLOADED,
            occurred_at="2026-08-24T00:00:00Z",
            paper_id=req.arxiv_id,
            version=req.version,
            attempt=0,
            payload={
                "arxiv_id": req.arxiv_id,
                "version": req.version,
                "pdf_storage_key": req.pdf_storage_key,
            },
        )
        processor.process_message(download_envelope, msg=None)

        # Step 2: GPU Embedding
        import psycopg
        conn = psycopg.connect(config.postgres_url)
        with conn.cursor() as cur:
            cur.execute("SELECT id FROM papers WHERE arxiv_id = %s ORDER BY version DESC LIMIT 1;", (req.arxiv_id,))
            paper_id_row = cur.fetchone()
            p_id = paper_id_row[0] if paper_id_row else None
        conn.close()

        embedder_worker = EmbedderWorker()
        chunk_envelope = EventEnvelope(
            event_id=str(ulid.new()),
            event_type=TOPIC_PAPERS_CHUNKED,
            occurred_at="2026-08-24T00:00:00Z",
            paper_id=req.arxiv_id,
            version=req.version,
            attempt=0,
            payload={
                "arxiv_id": req.arxiv_id,
                "version": req.version,
                "paper_db_id": p_id,
                "chunk_count": 0,
            },
        )
        embedder_worker.process_message(chunk_envelope, msg=None)

        # Query stats
        import psycopg
        conn = psycopg.connect(config.postgres_url)
        with conn.cursor() as cur:
            cur.execute("SELECT id FROM papers WHERE arxiv_id = %s ORDER BY version DESC LIMIT 1;", (req.arxiv_id,))
            paper_id_row = cur.fetchone()
            p_id = paper_id_row[0] if paper_id_row else 0
            cur.execute("SELECT count(*) FROM paper_sections WHERE paper_id = %s;", (p_id,))
            sec_count = cur.fetchone()[0]
            cur.execute("SELECT count(*) FROM paper_chunks WHERE paper_id = %s;", (p_id,))
            chk_count = cur.fetchone()[0]
            cur.execute("SELECT count(*) FROM chunk_embeddings ce JOIN paper_chunks pc ON ce.chunk_id = pc.id WHERE pc.paper_id = %s;", (p_id,))
            emb_count = cur.fetchone()[0]
        conn.close()

        return IngestProcessResponse(
            status="success",
            arxiv_id=req.arxiv_id,
            sections=sec_count,
            chunks=chk_count,
            embeddings=emb_count,
        )
    except Exception as e:
        logger.error(f"Error ingesting paper {req.arxiv_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8081)

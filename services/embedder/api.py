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
import os
import time
from typing import List, Optional, Sequence, Tuple
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

# Cross-encoder input limit. Lowering it changes scores and ranking, so it needs an ADR (AI-9).
RERANK_MAX_LENGTH = 512
# Pairs are scored in length-sorted batches of this size to cut padding; scores go back in input order.
RERANK_BATCH_SIZE = 8


def _torch_threads() -> int:
    """CPU threads for torch: EMBEDDER_TORCH_THREADS if set to a positive int, else the cores this process may use."""
    raw = os.getenv("EMBEDDER_TORCH_THREADS", "").strip()
    if raw:
        try:
            n = int(raw)
            if n > 0:
                return n
        except ValueError:
            pass
        logger.warning(f"Ignoring invalid EMBEDDER_TORCH_THREADS={raw!r}")
    try:
        return max(1, len(os.sched_getaffinity(0)))
    except AttributeError:  # not available on macOS
        return max(1, os.cpu_count() or 1)


def score_pairs(tokenizer, model, query: str, passages: Sequence[str],
                batch_size: int = RERANK_BATCH_SIZE) -> Tuple[List[float], dict]:
    """Cross-encoder sigmoid scores for (query, passage) pairs, returned in input order.

    Same tokenization (truncation, max_length) and model call as a single padded batch, but pairs are
    sorted by token length and scored in batches so short pairs aren't padded to the longest one.
    Returns (scores, stats); stats holds counts and timings only, never text.
    """
    t0 = time.perf_counter()
    pairs = [[query, p] for p in passages]
    lengths = [len(ids) for ids in
               tokenizer(pairs, truncation=True, max_length=RERANK_MAX_LENGTH)["input_ids"]]
    order = sorted(range(len(pairs)), key=lambda i: lengths[i])  # stable: ties keep input order
    batches = []
    for start in range(0, len(order), batch_size):
        idx = order[start:start + batch_size]
        inputs = tokenizer([pairs[i] for i in idx], padding=True, truncation=True,
                           return_tensors="pt", max_length=RERANK_MAX_LENGTH)
        # Same device as the model (#43: MPS/CUDA when the embedder has one). BatchEncoding has .to().
        device = getattr(model, "device", None)
        if device is not None and hasattr(inputs, "to"):
            inputs = inputs.to(device)
        batches.append((idx, inputs))
    t1 = time.perf_counter()

    scores = [0.0] * len(pairs)
    padded_tokens = 0
    with torch.inference_mode():
        for idx, inputs in batches:
            logits = model(**inputs, return_dict=True).logits.view(-1, ).float()
            probs = torch.sigmoid(logits).tolist()
            for i, prob in zip(idx, probs):
                scores[i] = float(prob)  # back to input order
            padded_tokens += int(inputs["input_ids"].shape[0]) * int(inputs["input_ids"].shape[1])
    t2 = time.perf_counter()

    stats = {
        "passages": len(pairs),
        "batches": len(batches),
        "max_len": max(lengths) if lengths else 0,
        "tokens": sum(lengths),
        "padded_tokens": padded_tokens,
        "single_batch_padded_tokens": len(pairs) * (max(lengths) if lengths else 0),
        "threads": torch.get_num_threads(),
        "tokenize_ms": round((t1 - t0) * 1000, 1),
        "forward_ms": round((t2 - t1) * 1000, 1),
        "total_ms": round((t2 - t0) * 1000, 1),
    }
    return scores, stats


def _format_stats(stats: dict) -> str:
    return " ".join(f"{k}={v}" for k, v in stats.items())


@app.on_event("startup")
def load_models():
    global embedder, reranker_tokenizer, reranker_model
    threads_before = torch.get_num_threads()
    torch.set_num_threads(_torch_threads())
    logger.info(f"torch threads: {threads_before} -> {torch.get_num_threads()}")
    logger.info(f"Loading embedding model {config.embedding_model} on device...")
    embedder = BGEEmbedder(model_name=config.embedding_model)
    logger.info(f"Model loaded successfully on {embedder.device}")

    try:
        logger.info("Loading Cross-Encoder Reranker BAAI/bge-reranker-base...")
        reranker_tokenizer = AutoTokenizer.from_pretrained("BAAI/bge-reranker-base")
        reranker_model = AutoModelForSequenceClassification.from_pretrained("BAAI/bge-reranker-base")
        # Same device as the embedder (MPS/CUDA/CPU). Left on CPU it scored 20 pairs in ~2.3 s on Apple
        # silicon, most of the /v1/ask latency budget. fp32 everywhere, so scores do not change.
        reranker_model.to(embedder.device)
        reranker_model.eval()
        logger.info(f"Cross-Encoder Reranker loaded successfully on {embedder.device}!")
    except Exception as e:
        logger.error(f"Warning: Failed to load cross-encoder ({e}), falling back to bi-encoder")
        return

    try:
        # Warm-up so the first real request doesn't pay one-time kernel/allocator setup costs.
        _, stats = score_pairs(reranker_tokenizer, reranker_model, "warm-up query",
                               ["warm-up passage " * n for n in (8, 64, 256)])
        logger.info(f"rerank warm-up {_format_stats(stats)}")
    except Exception as e:
        logger.warning(f"rerank warm-up failed ({e}); continuing")


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
            scores, stats = score_pairs(reranker_tokenizer, reranker_model, req.query, req.passages)
            logger.info(f"rerank {_format_stats(stats)}")
            return RerankResponse(scores=scores)
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

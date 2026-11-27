"""NLI citation verification (services/embedder/verify.py and /internal/verify). No infra.

The real-model test runs only when the NLI model is already in the local Hugging Face cache, so CI's unit job
does not download it; the stack and eval workflows prefetch it and exercise it end to end.
"""
from types import SimpleNamespace

import pytest
import torch

from services.embedder import verify as nli


def test_strip_chunk_prefix():
    assert nli.strip_chunk_prefix("Adam: A Method — Introduction\n\nBody text.") == "Body text."
    assert nli.strip_chunk_prefix("No prefix here.\n\nSecond paragraph.") == "No prefix here.\n\nSecond paragraph."
    assert nli.strip_chunk_prefix("Title — Sec\n\n   ") == "   "  # never falls back to the title
    assert nli.strip_chunk_prefix("plain") == "plain"


def test_premise_windows():
    body = "T — S\n\nFirst sentence is long enough. Second sentence is long enough! Third sentence is long enough? x. Fourth sentence is long enough."
    assert nli.premise_windows(body, 2) == [
        "First sentence is long enough. Second sentence is long enough!",
        "Second sentence is long enough! Third sentence is long enough?",
        "Third sentence is long enough? Fourth sentence is long enough.",
    ]
    # Fewer sentences than k: the whole (whitespace-normalised, prefix-free) body is one window.
    assert nli.premise_windows("T — S\n\nOnly   one\nsentence here.", 2) == ["Only one sentence here."]
    assert nli.premise_windows("T — S\n\n", 2) == []


class _Tok:
    """Records premise/claim pairs; encodes each pair as its index into a shared list."""

    def __init__(self):
        self.seen = []

    def __call__(self, premises, claims, **kw):
        assert kw["truncation"] == "only_first" and kw["max_length"] == nli.NLI_MAX_LENGTH
        ids = []
        for p, c in zip(premises, claims):
            self.seen.append((p, c))
            ids.append([len(self.seen) - 1])
        return {"input_ids": torch.tensor(ids)}


class _Model:
    """P(entailment) = 0.9 when the claim's first word occurs in the premise window, else 0.1."""

    config = SimpleNamespace(id2label={0: "contradiction", 1: "entailment", 2: "neutral"})

    def __init__(self, tok):
        self.tok = tok

    def __call__(self, input_ids):
        rows = []
        for (i,) in input_ids.tolist():
            premise, claim = self.tok.seen[i]
            p = 0.9 if claim.split()[0].lower() in premise.lower() else 0.1
            rows.append([(1 - p) / 2, p, (1 - p) / 2])
        return SimpleNamespace(logits=torch.tensor(rows).log())  # softmax(log q) == q for a distribution q


def test_verify_pairs_takes_max_over_windows_and_keeps_order():
    tok = _Tok()
    model = _Model(tok)
    idx = nli.entailment_index(model)
    passage = ("T — S\n\nWarmup is used in early training steps. Nothing relevant appears in this one. "
               "Gradient clipping keeps updates stable overall.")
    pairs = [("gradient clipping helps.", passage), ("dropout is used.", passage), ("warmup matters.", passage), ("x.", "T — S\n\n")]
    scores, stats = nli.verify_pairs(tok, model, pairs, idx, batch_size=2)
    assert scores[0] == pytest.approx(0.9, abs=1e-6)  # entailed in one window: max over windows
    assert scores[1] == pytest.approx(0.1, abs=1e-6)  # no window entails it
    assert scores[2] == pytest.approx(0.9, abs=1e-6)
    assert scores[3] == 0.0  # empty passage: no windows, score 0
    assert stats["pairs"] == 4 and stats["windows"] == 6 and stats["batches"] == 3
    assert "Warmup" not in str(stats)  # stats never carry text


def test_entailment_index_requires_label():
    with pytest.raises(ValueError):
        nli.entailment_index(SimpleNamespace(config=SimpleNamespace(id2label={0: "a", 1: "b"})))


def test_verify_endpoint_fails_closed_without_model(monkeypatch):
    from fastapi import HTTPException
    from services.embedder import api

    monkeypatch.setattr(api, "nli_model", None)
    assert api.verify_endpoint(api.VerifyRequest(pairs=[])).scores == []
    with pytest.raises(HTTPException) as e:
        api.verify_endpoint(api.VerifyRequest(pairs=[api.VerifyPair(claim="c", passage="p")]))
    assert e.value.status_code == 503


def _cached_nli():
    from transformers import AutoModelForSequenceClassification, AutoTokenizer
    try:
        tok = AutoTokenizer.from_pretrained(nli.DEFAULT_NLI_MODEL, local_files_only=True)
        model = AutoModelForSequenceClassification.from_pretrained(nli.DEFAULT_NLI_MODEL, local_files_only=True).eval()
    except Exception:
        pytest.skip(f"{nli.DEFAULT_NLI_MODEL} not in the local Hugging Face cache")
    return tok, model


# A chunk as the chunker stores it, and claims of each kind. Cosine similarity (the old check) marked all four
# non-entailed kinds below as supported.
CHUNK = ("Adam: A Method for Stochastic Optimization — Introduction\n\n"
         "We propose Adam, a method for efficient stochastic optimization that only requires first-order gradients "
         "with little memory requirement. The method computes individual adaptive learning rates for different "
         "parameters from estimates of first and second moments of the gradients. Our method is designed to combine "
         "the advantages of two recently popular methods: AdaGrad, which works well with sparse gradients, and "
         "RMSProp, which works well in on-line and non-stationary settings.")
OTHER = ("RoBERTa — Introduction\n\nWe find that BERT was significantly undertrained. We train the model longer, "
         "with bigger batches, over more data, and remove the next sentence prediction objective.")


def test_real_nli_separates_support_from_topicality():
    tok, model = _cached_nli()
    idx = nli.entailment_index(model)
    true_claim = "Adam computes individual adaptive learning rates from estimates of first and second moments of the gradients."
    pairs = [
        (true_claim, CHUNK),
        ("Adam uses a single fixed learning rate for all parameters.", CHUNK),           # contradiction
        ('The paper "Adam: A Method for Stochastic Optimization" addresses this in the Introduction section.', CHUNK),  # old template
        ("The weather in Paris is usually mild in spring.", CHUNK),                       # unrelated
        (true_claim, OTHER),                                                              # wrong paper
    ]
    scores, _ = nli.verify_pairs(tok, model, pairs, idx)
    assert scores[0] >= 0.9, scores
    assert all(s < 0.2 for s in scores[1:]), scores


def test_startup_handler_is_load_models_and_loads_verifier(monkeypatch):
    """The one startup hook is load_models, and it loads the verifier even when the reranker fails to load."""
    from services.embedder import api

    assert api.app.router.on_startup == [api.load_models]
    loaded = []
    monkeypatch.setattr(api, "BGEEmbedder", lambda model_name: SimpleNamespace(device="cpu"))
    monkeypatch.setattr(api.torch, "set_num_threads", lambda n: None)
    monkeypatch.setattr(api, "_load_verifier", lambda device: loaded.append(device))

    def boom(*a, **k):
        raise OSError("reranker missing")

    monkeypatch.setattr(api.AutoTokenizer, "from_pretrained", boom)
    for name in ("embedder", "reranker_tokenizer", "reranker_model"):
        monkeypatch.setattr(api, name, None)
    api.load_models()
    assert loaded == ["cpu"]


# --- /internal/ingest reuses its workers and the startup-loaded model (AI-12). Fakes only, no infra. ---

class _FakeCursor:
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def execute(self, sql, params=None):
        pass

    def fetchone(self):
        return (7,)  # paper id and every count


class _FakeConn:
    def cursor(self):
        return _FakeCursor()

    def close(self):
        pass


@pytest.fixture
def ingest(monkeypatch):
    """Fresh worker slots, fake DB, and counting fake workers. Returns a namespace the test can tune."""
    import threading
    import time

    import psycopg
    from services.embedder import api

    monkeypatch.setattr(api, "_processor_worker", None)
    monkeypatch.setattr(api, "_embedder_worker", None)
    monkeypatch.setattr(api, "embedder", None)
    monkeypatch.setattr(psycopg, "connect", lambda *a, **k: _FakeConn())

    st = SimpleNamespace(proc_inits=0, emb_inits=[], proc_calls=0, emb_calls=0, log=[],
                         proc_init_sleep=0.0, call_sleep=0.0, proc_init_failures=0, emb_init_failures=0)
    guard = threading.Lock()

    def record(kind):
        with guard:
            st.log.append((kind, threading.get_ident()))

    class FakeProcessor:
        def __init__(self):
            time.sleep(st.proc_init_sleep)
            with guard:
                st.proc_inits += 1
                if st.proc_init_failures:
                    st.proc_init_failures -= 1
                    raise RuntimeError("minio down")

        def process_message(self, envelope, msg):
            record("enter")
            time.sleep(st.call_sleep)
            st.proc_calls += 1
            record("exit")

    class FakeEmbedderWorker:
        def __init__(self, **kwargs):
            with guard:
                st.emb_inits.append(kwargs)
                if st.emb_init_failures:
                    st.emb_init_failures -= 1
                    raise RuntimeError("model load failed")

        def process_message(self, envelope, msg):
            record("enter")
            time.sleep(st.call_sleep)
            st.emb_calls += 1
            record("exit")

    monkeypatch.setattr(api, "ProcessorWorker", FakeProcessor)
    monkeypatch.setattr(api, "EmbedderWorker", FakeEmbedderWorker)
    st.api = api
    st.req = api.IngestProcessRequest(arxiv_id="2608.20331", version=1, pdf_storage_key="pdfs/2608.20331v1.pdf")
    return st


def test_ingest_builds_each_worker_once(ingest):
    for _ in range(2):
        resp = ingest.api.ingest_paper_endpoint(ingest.req)
        assert resp.status == "success" and resp.chunks == 7 and resp.embeddings == 7
    assert ingest.proc_inits == 1 and len(ingest.emb_inits) == 1
    assert ingest.proc_calls == 2 and ingest.emb_calls == 2


def test_ingest_embedder_worker_gets_startup_model(ingest, monkeypatch):
    sentinel = object()
    monkeypatch.setattr(ingest.api, "embedder", sentinel)
    ingest.api.ingest_paper_endpoint(ingest.req)
    assert len(ingest.emb_inits) == 1 and ingest.emb_inits[0].get("embedder") is sentinel


def test_ingest_embedder_worker_loads_own_model_without_startup_model(ingest):
    ingest.api.ingest_paper_endpoint(ingest.req)  # fixture sets api.embedder = None
    assert len(ingest.emb_inits) == 1 and ingest.emb_inits[0].get("embedder") is None


def test_embedder_worker_uses_injected_model(monkeypatch):
    from services.common.config import config
    from services.embedder import worker

    def boom(**kw):
        raise AssertionError("BGEEmbedder must not be built when a model is injected")

    monkeypatch.setattr(worker, "BGEEmbedder", boom)
    x = object()
    assert worker.EmbedderWorker(embedder=x).embedder is x

    built = []
    monkeypatch.setattr(worker, "BGEEmbedder", lambda **kw: built.append(kw) or SimpleNamespace(kw=kw))
    w = worker.EmbedderWorker()
    assert built == [{"model_name": config.embedding_model}] and w.embedder.kw == built[0]


def test_ingest_concurrent_first_requests_build_once_and_do_not_overlap(ingest):
    import threading

    ingest.proc_init_sleep = 0.2  # widen the race on first construction
    ingest.call_sleep = 0.05      # widen the race between pipelines
    results, errors = [], []

    def call():
        try:
            results.append(ingest.api.ingest_paper_endpoint(ingest.req).status)
        except Exception as e:  # pragma: no cover - surfaced by the asserts below
            errors.append(e)

    threads = [threading.Thread(target=call) for _ in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=10)
    assert not errors and results == ["success", "success"]
    # Every process_message exits before the next one enters, in the same thread.
    log = ingest.log
    no_overlap = len(log) == 8 and all(log[i][0] == "enter" and log[i + 1] == ("exit", log[i][1]) for i in range(0, 8, 2))
    # One assert so a failure reports both properties: built once, and no interleaved pipelines.
    assert {"proc_inits": ingest.proc_inits, "emb_inits": len(ingest.emb_inits), "no_overlap": no_overlap} == \
        {"proc_inits": 1, "emb_inits": 1, "no_overlap": True}, log


def test_ingest_failed_worker_construction_is_not_cached(ingest):
    from fastapi import HTTPException

    ingest.proc_init_failures = 1
    with pytest.raises(HTTPException) as e:
        ingest.api.ingest_paper_endpoint(ingest.req)
    assert e.value.status_code == 500
    assert ingest.api._processor_worker is None
    assert ingest.api.ingest_paper_endpoint(ingest.req).status == "success"
    assert ingest.proc_inits == 2 and ingest.proc_calls == 1


def _call_during_ingest(ingest, call):
    """Start an ingest, wait until its first process_message has entered (lock held), then run call() in its own thread
    with a bounded join, so an endpoint that takes _ingest_lock fails the test instead of hanging it. The ingest thread
    is joined in a finally, so it never outlives the test. Returns (ingest result, call result); the call result holds
    the endpoint response and the lock state and exit count at the moment it returned."""
    import threading
    import time

    api = ingest.api
    ingest.call_sleep = 0.5  # the ingest holds the lock for >= 1 s (two process_message calls)
    out, res = {}, {}

    def run_ingest():
        try:
            out["resp"] = api.ingest_paper_endpoint(ingest.req)
        except Exception as e:  # pragma: no cover - surfaced by the asserts below
            out["err"] = e

    def run_call():
        try:
            res["resp"] = call()
        except Exception as e:  # surfaced by the callers' asserts
            res["err"] = e
        res["locked"] = api._ingest_lock.locked()
        res["exits"] = sum(kind == "exit" for kind, _ in ingest.log)

    t = threading.Thread(target=run_ingest, daemon=True)
    t.start()
    try:
        deadline = time.monotonic() + 5
        while not any(kind == "enter" for kind, _ in ingest.log) and time.monotonic() < deadline:
            time.sleep(0.005)
        assert ingest.log and ingest.log[0][0] == "enter", "ingest never entered process_message"
        c = threading.Thread(target=run_call, daemon=True)
        c.start()
        c.join(timeout=5)
    finally:
        t.join(timeout=10)  # longer than the ingest's total sleep (2 x 0.5 s)
        out["alive"] = t.is_alive()
    return out, res


def _assert_served_during_ingest(out, res):
    """The ingest succeeded, and the endpoint returned while the ingest still held the lock, before any exit."""
    assert not out["alive"] and "resp" in out and "err" not in out and out["resp"].status == "success", out
    assert "resp" in res, f"endpoint call did not return: {res!r}"
    assert {"locked_at_return": res["locked"], "exits_at_return": res["exits"]} == \
        {"locked_at_return": True, "exits_at_return": 0}


def test_embed_is_not_serialized_behind_an_in_flight_ingest(ingest, monkeypatch):
    """Pins that /internal/embed does not take _ingest_lock. /v1/search and /v1/ask call it for every query, and an ingest
    holds the lock for 30-80 s, so a query must be served from the shared startup model while an ingest is in progress.
    Also pins that the ingest's EmbedderWorker got that same model object (AI-12)."""
    import numpy as np

    api = ingest.api
    calls = []

    class FakeModel:
        def embed_texts(self, texts, is_query):
            calls.append((list(texts), is_query, api._ingest_lock.locked()))
            return np.zeros((len(texts), 768), dtype=np.float32)

    model = FakeModel()
    monkeypatch.setattr(api, "embedder", model)
    out, emb = _call_during_ingest(ingest, lambda: api.embed_texts_endpoint(api.EmbedRequest(texts=["q"], is_query=True)))
    assert not out["alive"] and "err" not in out and out["resp"].status == "success"
    assert "resp" in emb, "embed call did not return"
    assert emb["resp"].dim == 768 and len(emb["resp"].vectors) == 1 and len(emb["resp"].vectors[0]) == 768
    # Returned while the ingest still held the lock and before its first process_message exited.
    at_return = {"locked_at_return": emb["locked"], "exits_at_return": emb["exits"]}
    assert at_return == {"locked_at_return": True, "exits_at_return": 0}
    # Same model object: the ingest's EmbedderWorker was built with it, and the embed endpoint called it.
    assert len(ingest.emb_inits) == 1 and ingest.emb_inits[0].get("embedder") is model
    assert calls == [(["q"], True, True)]


class _NoEmbedder:
    def embed_texts(self, texts, is_query):
        raise AssertionError("bi-encoder fallback must not run when the cross-encoder is loaded")


def test_rerank_cross_encoder_is_not_serialized_behind_an_in_flight_ingest(ingest, monkeypatch):
    """Pins that /internal/rerank's cross-encoder path does not take _ingest_lock (every /v1/ask calls it), and that it is
    the path taken when the cross-encoder is loaded, even with an embedder present."""
    api = ingest.api
    tok, model, calls = object(), object(), []

    def fake_score_pairs(tokenizer, reranker, query, passages):
        calls.append((tokenizer, reranker, query, list(passages), api._ingest_lock.locked()))
        return [0.5] * len(passages), {}

    monkeypatch.setattr(api, "reranker_tokenizer", tok)
    monkeypatch.setattr(api, "reranker_model", model)
    monkeypatch.setattr(api, "embedder", _NoEmbedder())
    monkeypatch.setattr(api, "score_pairs", fake_score_pairs)
    out, res = _call_during_ingest(ingest, lambda: api.rerank_endpoint(api.RerankRequest(query="q", passages=["a", "b"])))
    _assert_served_during_ingest(out, res)
    assert res["resp"].scores == [0.5, 0.5]
    assert len(calls) == 1 and calls[0][0] is tok and calls[0][1] is model
    assert calls[0][2:] == ("q", ["a", "b"], True)


def test_rerank_bi_encoder_fallback_is_not_serialized_behind_an_in_flight_ingest(ingest, monkeypatch):
    """Pins that /internal/rerank's bi-encoder fallback (no cross-encoder loaded) does not take _ingest_lock."""
    import math

    import numpy as np

    api = ingest.api
    calls = []

    class FakeModel:
        def embed_texts(self, texts, is_query):
            calls.append((list(texts), is_query, api._ingest_lock.locked()))
            return np.full((len(texts), 768), 0.1, dtype=np.float32)

    def no_score_pairs(*a, **k):
        raise AssertionError("cross-encoder must not run when reranker_model is None")

    monkeypatch.setattr(api, "reranker_model", None)
    monkeypatch.setattr(api, "reranker_tokenizer", object())
    monkeypatch.setattr(api, "embedder", FakeModel())
    monkeypatch.setattr(api, "score_pairs", no_score_pairs)
    out, res = _call_during_ingest(ingest, lambda: api.rerank_endpoint(api.RerankRequest(query="q", passages=["a", "b"])))
    _assert_served_during_ingest(out, res)
    scores = res["resp"].scores
    assert len(scores) == 2 and all(isinstance(s, float) and math.isfinite(s) for s in scores), scores
    assert calls == [(["q"], True, True), (["a", "b"], False, True)]


def test_verify_is_not_serialized_behind_an_in_flight_ingest(ingest, monkeypatch):
    """Pins that /internal/verify does not take _ingest_lock (every /v1/ask calls it since NLI verification)."""
    api = ingest.api
    tok, model, idx, calls = object(), object(), object(), []

    def fake_verify_pairs(tokenizer, nli_model, pairs, entail_idx):
        calls.append((tokenizer, nli_model, list(pairs), entail_idx, api._ingest_lock.locked()))
        return [0.9], {}

    monkeypatch.setattr(api, "nli_tokenizer", tok)
    monkeypatch.setattr(api, "nli_model", model)
    monkeypatch.setattr(api, "nli_entail_idx", idx)
    monkeypatch.setattr(api.nli, "verify_pairs", fake_verify_pairs)
    req = api.VerifyRequest(pairs=[api.VerifyPair(claim="c", passage="p")])
    out, res = _call_during_ingest(ingest, lambda: api.verify_endpoint(req))
    _assert_served_during_ingest(out, res)
    assert res["resp"].scores == [0.9] and res["resp"].model == api.NLI_MODEL
    assert len(calls) == 1 and calls[0][0] is tok and calls[0][1] is model and calls[0][3] is idx
    assert calls[0][2] == [("c", "p")] and calls[0][4] is True


def test_ingest_failed_embedder_build_keeps_cached_processor(ingest):
    """Pins that an EmbedderWorker build failure after the ProcessorWorker is built leaves the processor cached and the
    embedder slot empty: the request is a 500, the next one retries only the embedder and succeeds, and the processor is
    never rebuilt (no extra MinIO/tokenizer setup per failed ingest)."""
    from fastapi import HTTPException

    api = ingest.api
    ingest.emb_init_failures = 1
    with pytest.raises(HTTPException) as e:
        api.ingest_paper_endpoint(ingest.req)
    assert e.value.status_code == 500
    proc = api._processor_worker
    assert proc is not None and ingest.proc_inits == 1
    assert api._embedder_worker is None
    assert api.ingest_paper_endpoint(ingest.req).status == "success"
    assert ingest.proc_inits == 1 and api._processor_worker is proc
    assert len(ingest.emb_inits) == 2 and ingest.emb_calls == 1


# --- The embedder worker saves each vector with the content hash it was computed from (AI-13). Fakes only, no infra. ---

class _HashConn:
    def __init__(self):
        self.commits = 0
        self.rollbacks = 0
        self.closed = False

    def commit(self):
        self.commits += 1

    def rollback(self):
        self.rollbacks += 1

    def close(self):
        self.closed = True


class _VecEmbedder:
    """Vector j of a call is [len(text_j), j]: deterministic, and different per chunk."""

    def __init__(self):
        self.calls = []

    def embed_texts(self, texts, is_query):
        import numpy as np

        self.calls.append((list(texts), is_query))
        return np.array([[float(len(t)), float(j)] for j, t in enumerate(texts)], dtype=np.float32)


def _run_embed_worker(monkeypatch, caplog, chunks, written):
    import logging

    from services.embedder import worker

    conn, saved, statuses = _HashConn(), [], []
    monkeypatch.setattr(worker, "get_db_connection", lambda: conn)
    monkeypatch.setattr(worker, "get_chunks_for_embedding", lambda c, pid, model: [dict(ch) for ch in chunks])

    def fake_save(c, items):
        assert c is conn
        saved.append(list(items))
        return written

    monkeypatch.setattr(worker, "save_chunk_embeddings", fake_save)
    monkeypatch.setattr(worker, "update_paper_status", lambda c, pid, status: statuses.append((c, pid, status, conn.commits)))
    w = worker.EmbedderWorker(embedder=_VecEmbedder())
    w.batch_size = 2  # three chunks to embed -> two batches
    env = SimpleNamespace(paper_id="2608.00001", version=1, payload={"paper_db_id": 42})  # the fields process_message reads
    with caplog.at_level(logging.INFO, logger="embedder"):
        w.process_message(env, None)
    return w, conn, saved, statuses


_HASH_CHUNKS = [
    {"chunk_id": 11, "chunk_index": 0, "content": "T — S\n\nalpha", "content_hash": "h-11", "existing_model": None},
    {"chunk_id": 12, "chunk_index": 1, "content": "T — S\n\nbeta beta", "content_hash": "h-12", "existing_model": "bge"},
    {"chunk_id": 13, "chunk_index": 2, "content": "T — S\n\ngamma gamma gamma", "content_hash": "h-13", "existing_model": None},
    {"chunk_id": 14, "chunk_index": 3, "content": "T — S\n\nd", "content_hash": "h-14", "existing_model": None},
]


def _saved_logs(caplog):
    return [r.getMessage() for r in caplog.records if r.getMessage().startswith("Saved ")]


def test_embedder_worker_passes_each_chunks_content_hash(monkeypatch, caplog):
    """AC1: every saved item is (chunk_id, model, vector, that chunk's content_hash), for exactly the chunks it embedded."""
    w, conn, saved, statuses = _run_embed_worker(monkeypatch, caplog, _HASH_CHUNKS, written=3)
    assert len(saved) == 1
    items = saved[0]
    assert all(len(it) == 4 for it in items), items
    to_embed = [c for c in _HASH_CHUNKS if c["existing_model"] is None]
    assert [(it[0], it[1], it[3]) for it in items] == [(c["chunk_id"], w.model_name, c["content_hash"]) for c in to_embed]
    # Each vector is the one computed from that chunk's own text (batches of 2: positions 0,1 then 0).
    assert [it[2] for it in items] == [[float(len(_HASH_CHUNKS[0]["content"])), 0.0],
                                       [float(len(_HASH_CHUNKS[2]["content"])), 1.0],
                                       [float(len(_HASH_CHUNKS[3]["content"])), 0.0]]
    assert w.embedder.calls == [([_HASH_CHUNKS[0]["content"], _HASH_CHUNKS[2]["content"]], False),
                                ([_HASH_CHUNKS[3]["content"]], False)]
    assert _saved_logs(caplog) == ["Saved 3 embeddings for 2608.00001"]
    assert statuses == [(conn, 42, "embedded", 0)] and conn.commits == 1 and conn.rollbacks == 0 and conn.closed


def test_embedder_worker_logs_written_and_skipped_and_still_marks_embedded(monkeypatch, caplog):
    """AC2 + AC3: the log reports the returned count and the skipped count; skips don't fail the message."""
    _, conn, saved, statuses = _run_embed_worker(monkeypatch, caplog, _HASH_CHUNKS, written=1)
    assert len(saved) == 1 and len(saved[0]) == 3
    assert _saved_logs(caplog) == ["Saved 1 embeddings for 2608.00001 (2 skipped: chunk text changed or removed since read)"]
    assert not any("Saved 3 " in r.getMessage() or "saved 3 " in r.getMessage() for r in caplog.records)
    # Status set before the single commit, in the same transaction; no rollback.
    assert statuses == [(conn, 42, "embedded", 0)]
    assert conn.commits == 1 and conn.rollbacks == 0 and conn.closed

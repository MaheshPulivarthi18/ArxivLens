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
                         proc_init_sleep=0.0, call_sleep=0.0, proc_init_failures=0)
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

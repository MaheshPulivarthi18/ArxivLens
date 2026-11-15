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

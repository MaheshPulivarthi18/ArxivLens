import pytest
import numpy as np
import torch

from scripts.m0_skeleton import (
    PaperParser,
    TokenChunker,
    BGEEmbedder,
    CitationValidator,
    TARGET_TOKENS,
    OVERLAP_TOKENS,
)


def test_token_chunker_section_prefixing():
    chunker = TokenChunker(target_tokens=100, overlap_tokens=20, min_tokens=10)
    paper_title = "FlashAttention-2: Faster Attention with Better Parallelism"
    sections = [
        {"title": "Introduction", "level": 1, "text": "This is a long introductory text explaining transformer attention mechanisms and memory access patterns in detail over several sentences.", "page": 1},
        {"title": "References", "level": 1, "text": "Smith et al. Attention is all you need 2017.", "page": 8},
    ]

    chunks = chunker.chunk_sections(paper_title, sections)

    assert len(chunks) >= 1
    # Check that References section was dropped
    assert all(c["section_title"] != "References" for c in chunks)
    # Check prefixing format
    assert chunks[0]["content"].startswith(f"{paper_title} — Introduction\n\n")
    # Check content hash
    assert len(chunks[0]["content_hash"]) == 64


def test_embedder_dimensions_and_norm():
    embedder = BGEEmbedder()
    texts = ["Transformer architecture and self-attention", "Convolutional neural network"]
    vectors = embedder.embed_texts(texts)

    assert vectors.shape == (2, 768)
    # Assert unit norm (L2 = 1.0)
    for v in vectors:
        norm = np.linalg.norm(v)
        assert abs(norm - 1.0) < 1e-4


def test_citation_validator_marker_and_support():
    embedder = BGEEmbedder()
    validator = CitationValidator(embedder, threshold=0.45)

    doc_text = "FlashAttention tiled matrix multiplication to fit GPU SRAM and reduce memory bandwidth."
    doc_emb = embedder.embed_texts([doc_text])[0]

    retrieved_chunks = [
        {
            "arxiv_id": "2307.08691",
            "paper_title": "FlashAttention-2",
            "chunk_index": 0,
            "section_title": "Methodology",
            "content": doc_text,
            "embedding": doc_emb,
        }
    ]

    # Test valid and supported citation
    answer_supported = "The method tiles matrix multiplication to optimize GPU SRAM usage [1]."
    res_supported = validator.validate_and_verify(answer_supported, retrieved_chunks)
    assert res_supported["citations_total"] == 1
    assert len(res_supported["invalid_markers"]) == 0
    assert len(res_supported["citations"]) == 1
    assert res_supported["citations"][0]["supported"] is True

    # Test invalid marker out of range [2] (only 1 retrieved chunk)
    answer_invalid = "The method uses quantum circuits [2]."
    res_invalid = validator.validate_and_verify(answer_invalid, retrieved_chunks)
    assert 2 in res_invalid["invalid_markers"]

    # Test unsupported claim
    answer_unsupported = "The method is specifically designed for baking sourdough bread [1]."
    res_unsupported = validator.validate_and_verify(answer_unsupported, retrieved_chunks)
    assert res_unsupported["citations"][0]["supported"] is False


# --- AI-9: cross-encoder rerank in length-sorted batches -------------------------------------------------------
import warnings
from types import SimpleNamespace


class _FakeRerankTokenizer:
    """Word-level stand-in for the cross-encoder tokenizer: [CLS] query [SEP] passage [SEP], right-padded with 0."""

    def __call__(self, pairs, padding=False, truncation=False, return_tensors=None, max_length=None):
        rows = []
        for q, p in pairs:
            ids = [1] + [3 + len(w) for w in q.split()] + [2] + [3 + (sum(map(ord, w)) % 97) for w in p.split()]
            if truncation and max_length is not None:
                ids = ids[:max_length - 1]
            rows.append(ids + [2])
        if not padding:
            return {"input_ids": rows, "attention_mask": [[1] * len(r) for r in rows]}
        width = max(len(r) for r in rows)
        ids = [r + [0] * (width - len(r)) for r in rows]
        mask = [[1] * len(r) + [0] * (width - len(r)) for r in rows]
        assert return_tensors == "pt"
        return {"input_ids": torch.tensor(ids), "attention_mask": torch.tensor(mask)}


class _FakeRerankModel:
    """Logit = masked mean token id / 10 - 5: depends only on a pair's own tokens, never on padding or batch."""

    def __init__(self):
        self.batch_shapes = []

    def __call__(self, input_ids, attention_mask, return_dict=True):
        self.batch_shapes.append(tuple(input_ids.shape))
        ids, mask = input_ids.tolist(), attention_mask.tolist()
        logits = [[sum(i * m for i, m in zip(r, mr)) / sum(mr) / 10.0 - 5.0] for r, mr in zip(ids, mask)]
        return SimpleNamespace(logits=torch.tensor(logits))


def _legacy_rerank_scores(tokenizer, model, query, passages):
    """The pre-AI-9 rerank_endpoint scoring: one padded batch in input order (reference for parity)."""
    pairs = [[query, p] for p in passages]
    with torch.no_grad():
        inputs = tokenizer(pairs, padding=True, truncation=True, return_tensors="pt", max_length=512)
        logits = model(**inputs, return_dict=True).logits.view(-1, ).float()
        return [float(s) for s in torch.sigmoid(logits).tolist()]


def _mixed_length_passages():
    # Lengths deliberately out of order (long, short, mid, ...) and one over max_length, so sorting permutes them.
    lengths = [300, 3, 120, 7, 45, 600, 1, 80, 15, 250, 9, 33, 512, 2, 60, 18, 140, 5, 90, 27]
    return [" ".join(f"tok{j}x{k}" for k in range(n)) for j, n in enumerate(lengths)]


def test_rerank_score_pairs_keeps_input_order_after_length_sorting():
    from services.embedder import api

    tok, query, passages = _FakeRerankTokenizer(), "flash attention memory tiling", _mixed_length_passages()
    expected = _legacy_rerank_scores(tok, _FakeRerankModel(), query, passages)
    assert len(set(expected)) == len(expected)  # distinct, so any misplaced score is caught

    model = _FakeRerankModel()
    scores, stats = api.score_pairs(tok, model, query, passages, batch_size=4)

    assert scores == pytest.approx(expected, abs=1e-6)
    assert len(model.batch_shapes) == stats["batches"] == 5
    assert stats["passages"] == 20 and stats["max_len"] == 512
    assert stats["padded_tokens"] < stats["single_batch_padded_tokens"]  # length sorting cut the padding
    assert all(isinstance(v, (int, float)) for v in stats.values())  # the log line carries counts/timings, no text


def test_rerank_endpoint_returns_scores_in_input_order(monkeypatch):
    from services.embedder import api

    tok, query, passages = _FakeRerankTokenizer(), "dense passage retrieval", _mixed_length_passages()
    expected = _legacy_rerank_scores(tok, _FakeRerankModel(), query, passages)
    monkeypatch.setattr(api, "reranker_tokenizer", tok)
    monkeypatch.setattr(api, "reranker_model", _FakeRerankModel())

    resp = api.rerank_endpoint(api.RerankRequest(query=query, passages=passages))

    assert resp.scores == pytest.approx(expected, abs=1e-6)
    assert api.rerank_endpoint(api.RerankRequest(query=query, passages=[])).scores == []


def test_rerank_torch_threads_env(monkeypatch):
    from services.embedder import api

    monkeypatch.setenv("EMBEDDER_TORCH_THREADS", "3")
    assert api._torch_threads() == 3
    for bad in ("0", "-2", "many"):
        monkeypatch.setenv("EMBEDDER_TORCH_THREADS", bad)
        assert api._torch_threads() >= 1
    monkeypatch.delenv("EMBEDDER_TORCH_THREADS")
    assert api._torch_threads() >= 1


def test_rerank_torch_threads_fallback_without_sched_getaffinity(monkeypatch):
    from services.embedder import api

    monkeypatch.delenv("EMBEDDER_TORCH_THREADS", raising=False)
    monkeypatch.delattr(api.os, "sched_getaffinity", raising=False)  # e.g. macOS
    monkeypatch.setattr(api.os, "cpu_count", lambda: 6)
    assert api._torch_threads() == 6
    monkeypatch.setattr(api.os, "cpu_count", lambda: None)
    assert api._torch_threads() == 1


@pytest.mark.parametrize("batch_size,n_batches", [(1, 20), (3, 7), (4, 5), (20, 1), (32, 1)])
def test_rerank_score_pairs_batch_sizes(batch_size, n_batches):
    """Exact multiples, a remainder, batch == pool and batch > pool all give the single-batch scores in input order."""
    from services.embedder import api

    tok, query, passages = _FakeRerankTokenizer(), "low rank adaptation", _mixed_length_passages()
    expected = _legacy_rerank_scores(tok, _FakeRerankModel(), query, passages)
    model = _FakeRerankModel()

    scores, stats = api.score_pairs(tok, model, query, passages, batch_size=batch_size)

    assert scores == pytest.approx(expected, abs=1e-6)
    assert stats["batches"] == len(model.batch_shapes) == n_batches
    assert sum(shape[0] for shape in model.batch_shapes) == 20
    if n_batches == 1:
        assert stats["padded_tokens"] == stats["single_batch_padded_tokens"]


def test_rerank_single_passage_returns_one_score(monkeypatch):
    """The old code special-cased a scalar `probs` for one passage; the new path must still return a 1-element list."""
    from services.embedder import api

    tok, query, passages = _FakeRerankTokenizer(), "rotary position embeddings", ["rotary embeddings rotate keys"]
    expected = _legacy_rerank_scores(tok, _FakeRerankModel(), query, passages)

    scores, stats = api.score_pairs(tok, _FakeRerankModel(), query, passages)
    assert isinstance(scores, list) and len(scores) == 1 and scores == pytest.approx(expected, abs=1e-6)
    assert stats["passages"] == stats["batches"] == 1

    monkeypatch.setattr(api, "reranker_tokenizer", tok)
    monkeypatch.setattr(api, "reranker_model", _FakeRerankModel())
    resp = api.rerank_endpoint(api.RerankRequest(query=query, passages=passages))
    assert resp.scores == pytest.approx(expected, abs=1e-6)


def _patch_load_models(monkeypatch, api, reranker_load_error=None):
    """Fake BGE and cross-encoder loaders for load_models; globals and torch threads are restored after the test."""
    for name in ("embedder", "reranker_tokenizer", "reranker_model"):
        monkeypatch.setattr(api, name, None)
    monkeypatch.setattr(api.torch, "set_num_threads", lambda n: None)
    monkeypatch.setattr(api, "BGEEmbedder", lambda model_name: SimpleNamespace(device="cpu"))

    def load_tokenizer(*_a, **_k):
        if reranker_load_error is not None:
            raise reranker_load_error
        return _FakeRerankTokenizer()

    model = _FakeRerankModel()
    model.eval = lambda: model
    monkeypatch.setattr(api.AutoTokenizer, "from_pretrained", load_tokenizer)
    monkeypatch.setattr(api.AutoModelForSequenceClassification, "from_pretrained", lambda *a, **k: model)


def test_rerank_load_models_runs_warm_up(monkeypatch, caplog):
    from services.embedder import api

    _patch_load_models(monkeypatch, api)
    calls = []
    real_score_pairs = api.score_pairs
    monkeypatch.setattr(api, "score_pairs", lambda *a, **k: calls.append(len(a[3])) or real_score_pairs(*a, **k))

    with caplog.at_level("INFO", logger="embedder-api"):
        api.load_models()

    assert api.reranker_model is not None and api.reranker_tokenizer is not None
    assert calls == [3]
    assert any(r.getMessage().startswith("rerank warm-up passages=3 ") for r in caplog.records)


def test_rerank_load_models_continues_when_warm_up_fails(monkeypatch, caplog):
    from services.embedder import api

    _patch_load_models(monkeypatch, api)

    def boom(*_a, **_k):
        raise RuntimeError("warm-up exploded")

    monkeypatch.setattr(api, "score_pairs", boom)

    with caplog.at_level("INFO", logger="embedder-api"):
        api.load_models()  # must not raise: start-up continues

    assert api.embedder is not None and api.reranker_model is not None and api.reranker_tokenizer is not None
    assert any("rerank warm-up failed (warm-up exploded); continuing" in r.getMessage() for r in caplog.records)


def test_rerank_load_models_returns_early_when_cross_encoder_fails(monkeypatch, caplog):
    from services.embedder import api

    _patch_load_models(monkeypatch, api, reranker_load_error=OSError("no reranker"))
    calls = []
    monkeypatch.setattr(api, "score_pairs", lambda *a, **k: calls.append(a))

    with caplog.at_level("INFO", logger="embedder-api"):
        api.load_models()

    assert api.embedder is not None  # the bi-encoder fallback stays available
    assert api.reranker_model is None and api.reranker_tokenizer is None
    assert calls == []  # no warm-up without a cross-encoder
    assert any("Failed to load cross-encoder (no reranker)" in r.getMessage() for r in caplog.records)


_RERANK_PARITY_SENTENCES = [
    "FlashAttention tiles the attention computation so blocks fit in on-chip SRAM and cut HBM traffic.",
    "Dense passage retrieval trains a dual encoder so questions and passages share one embedding space.",
    "Low-rank adaptation freezes the pretrained weights and learns a small rank decomposition per layer.",
    "Rotary position embeddings rotate query and key vectors by an angle proportional to the position.",
    "HNSW builds a layered proximity graph and searches it greedily from the top layer downwards.",
    "Chain-of-thought prompting adds worked intermediate reasoning steps to the few-shot exemplars.",
    "Direct preference optimization fits the policy to preference pairs without a separate reward model.",
    "Residual connections let very deep convolutional networks train by learning residual functions.",
    "Scaling laws relate language model loss to parameters, data and compute as smooth power laws.",
    "Proximal policy optimization clips the probability ratio to keep each policy update small.",
    "The vision transformer splits an image into patches and feeds their embeddings to a transformer.",
    "ColBERT keeps one vector per token and scores documents with a late-interaction MaxSim operator.",
    "BERT pretrains a bidirectional encoder with masked language modelling and next sentence prediction.",
    "Retrieval-augmented generation conditions a seq2seq generator on passages fetched from an index.",
    "Chinchilla shows compute-optimal training scales parameters and tokens in roughly equal proportion.",
    "The checklist reward model scores generated medical report explanations item by item for factuality.",
    "Weather-aware demand models collect travel behaviour data actively where the predictor is uncertain.",
    "T5 casts every language task as text-to-text and studies transfer learning choices systematically.",
    "BEIR evaluates zero-shot retrieval across eighteen heterogeneous datasets and task types.",
    "Instruction tuning with human feedback aligns a language model with what users ask it to do.",
]


_RERANKER = "BAAI/bge-reranker-base"


def _reranker_in_local_hf_cache() -> bool:
    """True when bge-reranker-base is in the local Hugging Face cache (checked without any network call)."""
    from huggingface_hub import try_to_load_from_cache

    return isinstance(try_to_load_from_cache(_RERANKER, "config.json"), str)


def test_rerank_real_cross_encoder_scores_match_single_batch():
    """AC2 parity on the real BAAI/bge-reranker-base: 3 queries x 20 passages, old vs new path within 1e-4.

    Loads from the local HF cache only (never downloads). Skips only when the model isn't cached (ci.yml's Python
    job); stack.yml pre-fetches it, so the test runs there. Any other load error fails the test.
    """
    from transformers import AutoTokenizer, AutoModelForSequenceClassification
    from services.embedder import api

    if not _reranker_in_local_hf_cache():
        pytest.skip("bge-reranker-base not in local HF cache")
    tok = AutoTokenizer.from_pretrained(_RERANKER, local_files_only=True)
    model = AutoModelForSequenceClassification.from_pretrained(_RERANKER, local_files_only=True).eval()

    # 1..45 sentences; four passages (30, 40, 45, 35 sentences) exceed 512 tokens, so truncation is exercised.
    reps = [1, 12, 2, 30, 4, 1, 8, 40, 3, 1, 6, 45, 2, 10, 1, 35, 5, 3, 14, 7]
    passages = [" ".join([s] * n) for s, n in zip(_RERANK_PARITY_SENTENCES, reps)]
    queries = ["how does flash attention reduce memory traffic",
               "dense retrieval with dual encoders for open-domain QA",
               "preference optimization without a reward model"]
    worst = 0.0
    for qi, q in enumerate(queries):
        full_lengths = [len(ids) for ids in tok([[q, p] for p in passages], truncation=False)["input_ids"]]
        truncated = sum(n > api.RERANK_MAX_LENGTH for n in full_lengths)
        assert truncated >= 3, f"only {truncated} of 20 passages exceed {api.RERANK_MAX_LENGTH} tokens"
        old = _legacy_rerank_scores(tok, model, q, passages)
        new, stats = api.score_pairs(tok, model, q, passages)
        diff = max(abs(a - b) for a, b in zip(old, new))
        worst = max(worst, diff)
        assert sorted(range(20), key=lambda i: -old[i]) == sorted(range(20), key=lambda i: -new[i]), q
        # warnings show in `pytest -q` output (CI log), so the parity numbers are visible without -s
        warnings.warn(f"AI-9 parity query={qi} max_abs_diff={diff:.2e} truncated={truncated}/20 "
                      f"{api._format_stats(stats)}")
    assert worst <= 1e-4

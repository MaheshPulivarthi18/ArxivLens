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


def test_rerank_real_cross_encoder_scores_match_single_batch():
    """AC2 parity on the real BAAI/bge-reranker-base: 3 queries x 20 passages, old vs new path within 1e-4.

    Skips when the model can't be loaded (e.g. no Hugging Face access); CI downloads it.
    """
    from transformers import AutoTokenizer, AutoModelForSequenceClassification
    from services.embedder import api

    try:
        tok = AutoTokenizer.from_pretrained("BAAI/bge-reranker-base")
        model = AutoModelForSequenceClassification.from_pretrained("BAAI/bge-reranker-base").eval()
    except Exception as e:  # noqa: BLE001 - any load failure means "no model here"
        pytest.skip(f"bge-reranker-base not loadable: {e}")

    reps = [1, 12, 2, 30, 4, 1, 8, 20, 3, 1, 6, 16, 2, 10, 1, 25, 5, 3, 14, 7]  # 1..30 sentences, > 512 tokens at the top
    passages = [" ".join([s] * n) for s, n in zip(_RERANK_PARITY_SENTENCES, reps)]
    queries = ["how does flash attention reduce memory traffic",
               "dense retrieval with dual encoders for open-domain QA",
               "preference optimization without a reward model"]
    worst = 0.0
    for q in queries:
        old = _legacy_rerank_scores(tok, model, q, passages)
        new, stats = api.score_pairs(tok, model, q, passages)
        worst = max(worst, max(abs(a - b) for a, b in zip(old, new)))
        assert sorted(range(20), key=lambda i: -old[i]) == sorted(range(20), key=lambda i: -new[i]), q
        # warnings show in `pytest -q` output (CI log), so the parity numbers are visible without -s
        warnings.warn(f"AI-9 parity query={queries.index(q)} max_abs_diff={max(abs(a - b) for a, b in zip(old, new)):.2e} "
                      f"{api._format_stats(stats)}")
    assert worst <= 1e-4

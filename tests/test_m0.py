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

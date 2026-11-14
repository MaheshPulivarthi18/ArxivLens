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


# --- Text fidelity: parsed and chunked text must be the paper's own words (regression for flags=11 + decode) ---

WORDS = "FlashAttention-2 reaches 73% of the theoretical maximum FLOPs/s on A100 GPUs".split()


def _gap_spaced_pdf(words, lines=40):
    """A page laid out the way LaTeX does it: each word is placed on its own, with no space glyph between words.
    Extracting it with TEXT_INHIBIT_SPACES runs every word in a line together."""
    import pymupdf

    doc = pymupdf.open()
    page = doc.new_page()
    y = 60
    for _ in range(lines):
        x = 72
        for w in words:
            page.insert_text((x, y), w, fontname="helv", fontsize=10)
            x += pymupdf.get_text_length(w, fontname="helv", fontsize=10) + 3.5  # visual gap, no space glyph
        y += 13
    return doc


def test_parser_keeps_spaces_between_gap_spaced_words():
    from services.processor.worker import ProcessorWorker

    doc = _gap_spaced_pdf(WORDS)
    try:
        sections = ProcessorWorker._parse_doc_sections(None, doc)
    finally:
        doc.close()
    text = " ".join(s["text"] for s in sections)
    assert " ".join(WORDS) in text, text[:200]
    assert "  " not in text.strip()


def test_paper_parser_keeps_spaces_between_gap_spaced_words(tmp_path):
    doc = _gap_spaced_pdf(WORDS)
    path = tmp_path / "gap.pdf"
    doc.save(path)
    doc.close()
    text = " ".join(s["text"] for s in PaperParser().parse_pdf(str(path), "t"))
    assert " ".join(WORDS) in text, text[:200]


def test_chunk_text_is_verbatim_source_text():
    chunker = TokenChunker(target_tokens=60, overlap_tokens=10, min_tokens=10)
    body = " ".join(
        f"Sentence {i}: FlashAttention-2 achieves state-of-the-art GPU utilization (73%) on NVIDIA A100s."
        for i in range(30)
    )
    title = "FlashAttention-2: Faster Attention"
    chunks = chunker.chunk_sections(title, [{"title": "Method", "level": 1, "text": body, "page": 1}])
    assert len(chunks) > 3
    prefix = f"{title} — Method\n\n"
    for c in chunks:
        assert c["content"].startswith(prefix)
        text = c["content"][len(prefix):]
        assert text in body, text[:120]  # verbatim: case, punctuation and spacing kept
        assert "##" not in text
        assert body.find(text) == 0 or body[body.find(text) - 1] == " ", "chunk starts mid-word"
        end = body.find(text) + len(text)
        assert end == len(body) or body[end] == " ", "chunk ends mid-word"
        assert c["token_count"] <= chunker.target_tokens
    assert "State-of-the-art".lower() in chunks[0]["content"].lower() and "A100s" in chunks[0]["content"]


def test_slice_on_words_bounds():
    from scripts.m0_skeleton import MAX_WORD_SNAP_CHARS, _slice_on_words

    text = "alpha beta gamma"
    assert _slice_on_words(text, 7, 9) == "beta"  # inside "beta" -> whole word
    assert _slice_on_words(text, 0, len(text)) == text
    long_word = "x" * (MAX_WORD_SNAP_CHARS * 3)
    s = _slice_on_words(long_word, 50, 60)
    assert len(s) == 10  # no boundary within reach: token edges kept

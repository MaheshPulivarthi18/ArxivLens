"""ING-11: page breaks recorded per section, chunk output unchanged (no infra; downloads BGE once).

The fixture PDF is generated here with PyMuPDF (base-14 `helv`, nothing committed). Each page's body uses its own
vocabulary of single-wordpiece words, so the first word of any text tells which page it sits on, independently of the
implementation. AI-11 appends its tests to this file and reuses the module-level helpers below.
"""
import hashlib
import json
import sys

import pymupdf
import pytest

from scripts.m0_skeleton import MODEL_NAME, TokenChunker
from services.processor.worker import ProcessorWorker

FIXTURE_TITLE = "Synthetic Pages Fixture"
PAGE_VOCAB = {
    1: "cat dog horse cow bird fish lion tiger bear wolf sheep".split(),
    2: "red blue green yellow black white brown pink purple orange gray silver gold".split(),
    3: "one two three four five six seven eight nine".split(),
}
WORD_PAGE = {w: p for p, words in PAGE_VOCAB.items() for w in words}
WORDS_PER_LINE = 10
HEADING_PT, BODY_PT, LEADING = 16, 10, 13


def _body_lines(page_no: int, n_lines: int, offset: int = 0) -> list:
    vocab = PAGE_VOCAB[page_no]
    return [
        " ".join(vocab[(offset + i * WORDS_PER_LINE + j) % len(vocab)] for j in range(WORDS_PER_LINE))
        for i in range(n_lines)
    ]


def build_fixture_doc():
    """Title + `1 Introduction` on page 1; the Introduction body runs over pages 1-3; `2 Method` starts mid-page 3."""
    doc = pymupdf.open()
    layout = [
        [(FIXTURE_TITLE, HEADING_PT), ("1 Introduction", HEADING_PT)] + [(t, BODY_PT) for t in _body_lines(1, 40)],
        [(t, BODY_PT) for t in _body_lines(2, 45)],
        [(t, BODY_PT) for t in _body_lines(3, 20)]
        + [("2 Method", HEADING_PT)]
        + [(t, BODY_PT) for t in _body_lines(3, 10, offset=4)],
    ]
    for lines in layout:
        page = doc.new_page()  # A4, 595 x 842 pt
        y = 60
        for text, size in lines:
            page.insert_text((72, y), text, fontname="helv", fontsize=size)
            y += LEADING if size == BODY_PT else 2 * LEADING
    return doc


def first_word_page(text: str):
    """Oracle: the page of the first fixture word in `text` (None if it has none)."""
    for word in text.split():
        if word in WORD_PAGE:
            return WORD_PAGE[word]
    return None


def parse_fixture_sections():
    doc = build_fixture_doc()
    try:
        return ProcessorWorker._parse_doc_sections(None, doc)  # ING-8's call contract: no instance state
    finally:
        doc.close()


def _sha(s: str) -> str:
    return hashlib.sha256(s.encode("utf-8")).hexdigest()


def snapshot(sections: list, chunks: list) -> dict:
    return {
        "sections": [[s["title"], s["level"], s["page"], _sha(s["text"])] for s in sections],
        "chunks": [[c["chunk_index"], c["token_count"], c["content_hash"], _sha(c["content"])] for c in chunks],
    }


# Captured from unchanged production code (origin/main 2b71772) by this test's first CI run (PR #33, job 128531883258).
EXPECTED_SNAPSHOT = {
    "sections": [
        ["Introduction", 1, 1, "227bcbef6f8d15f72844a640b25b9e2361e7a66b9ea6bad575c12bab42321594"],
        ["Method", 1, 3, "8486fd03f7cf49792dea428aaecda90adda5a006eec30a405585f140222af85d"],
    ],
    "chunks": [
        [0, 496, "251c278fd8c2b31e00bf9aa325a8e89d9b4afb5834586de24968c50fde4c148c",
         "251c278fd8c2b31e00bf9aa325a8e89d9b4afb5834586de24968c50fde4c148c"],
        [1, 496, "887aa4ea0b55e20f95309df70682b5d719ae09724ca2ba6f510213a520b95038",
         "887aa4ea0b55e20f95309df70682b5d719ae09724ca2ba6f510213a520b95038"],
        [2, 223, "117aad44689c7e7e194c9180e8e5038a6927aef911ecd63142f103550c54144e",
         "117aad44689c7e7e194c9180e8e5038a6927aef911ecd63142f103550c54144e"],
        [3, 105, "603d110ff9ca035b88e6d18b6d83f634ff69fabe69a97702926f4321ffae7983",
         "603d110ff9ca035b88e6d18b6d83f634ff69fabe69a97702926f4321ffae7983"],
    ],
}


@pytest.fixture(scope="module")
def chunker():
    return TokenChunker(model_name=MODEL_NAME)


def test_fixture_vocabulary_is_single_wordpiece(chunker):
    bad = {w: chunker.tokenizer.tokenize(w) for w in WORD_PAGE if chunker.tokenizer.tokenize(w) != [w]}
    assert not bad, f"fixture words must be single BGE wordpieces, or the page oracle breaks: {bad}"


def test_chunk_output_identical_to_pre_change(chunker):
    sections = parse_fixture_sections()
    got = snapshot(sections, chunker.chunk_sections(FIXTURE_TITLE, sections))
    if got != EXPECTED_SNAPSHOT:
        # Annotation line, so the literal can be read from the job log or the check-run annotations.
        sys.__stdout__.write(f"::warning title=ING-11 snapshot::{json.dumps(got)}\n")
        sys.__stdout__.flush()
    assert got == EXPECTED_SNAPSHOT, f"got snapshot:\n{json.dumps(got, indent=1)}"
    assert [s["title"] for s in sections] == ["Introduction", "Method"]
    assert [s["page"] for s in sections] == [1, 3]

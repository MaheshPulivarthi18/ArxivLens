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


def test_page_breaks_invariants():
    sections = parse_fixture_sections()
    assert sections, "fixture produced no sections"
    for s in sections:
        pb = s.get("page_breaks")
        assert pb, f"{s['title']}: missing or empty page_breaks"
        assert pb[0][0] == 0, f"{s['title']}: first offset {pb[0][0]}, want 0"
        offsets, pages = [o for o, _ in pb], [p for _, p in pb]
        assert offsets == sorted(set(offsets)), f"{s['title']}: offsets not strictly ascending {pb}"
        assert pages == sorted(set(pages)), f"{s['title']}: pages not strictly ascending {pb}"
        assert all(o < len(s["text"]) for o in offsets), f"{s['title']}: offset out of range {pb}"
        assert pages[0] >= s["page"], f"{s['title']}: text starts before its heading page {pb}"
        # Implementation-independent oracle: the text after each break starts with that page's vocabulary.
        for o, p in pb:
            assert first_word_page(s["text"][o:]) == p, f"{s['title']}: break {(o, p)} not at page {p}'s text"
    assert any(len(s["page_breaks"]) >= 2 for s in sections), [s["page_breaks"] for s in sections]
    intro = sections[0]
    assert [p for _, p in intro["page_breaks"]] == [1, 2, 3]


class FakeCursor:
    """Records statements; `RETURNING id` yields ids 100, 101, ... in order."""

    def __init__(self):
        self.calls, self._next = [], 100

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def execute(self, sql, params=()):
        self.calls.append((" ".join(sql.split()), params))

    def fetchone(self):
        self._next += 1
        return (self._next - 1,)


class FakeConn:
    def __init__(self):
        self.cur, self.commits = FakeCursor(), 0

    def cursor(self):
        return self.cur

    def commit(self):
        self.commits += 1


def _chunk(i, **extra):
    return {"chunk_index": i, "section_title": "Intro", "content": f"c{i}", "content_hash": f"h{i}", "token_count": 5, **extra}


@pytest.mark.parametrize(
    "chunk, want_sql, want_params, want_page_number",
    [
        (_chunk(0, page_start=4, page_number=3), "INSERT INTO paper_chunk_pages", (101, 4), 3),
        (_chunk(0, page_start=1), "INSERT INTO paper_chunk_pages", (101, 1), 1),
        (_chunk(0, page_number=2), "DELETE FROM paper_chunk_pages WHERE chunk_id = %s", (101,), 2),
        (_chunk(0, page_start=None), "DELETE FROM paper_chunk_pages WHERE chunk_id = %s", (101,), 1),
    ],
    ids=["page_start-set", "page_start-1-no-page_number", "missing", "none"],
)
def test_save_chunks_page_rows(chunk, want_sql, want_params, want_page_number):
    from services.common.db import save_sections_and_chunks

    conn = FakeConn()
    n = save_sections_and_chunks(conn, 7, [{"title": "Intro", "level": 1}], [chunk])
    assert n == 1 and conn.commits == 0, "must stay inside the caller's transaction (L2)"
    calls = conn.cur.calls
    sec_sql, chunk_call = calls[0][0], calls[1]  # section id 100, chunk id 101
    assert sec_sql.startswith("INSERT INTO paper_sections")
    assert chunk_call[0].startswith("INSERT INTO paper_chunks") and chunk_call[0].endswith("RETURNING id;")
    assert chunk_call[1] == (7, 100, 0, "c0", "h0", want_page_number, 5), "page_number arg must stay chunk.get('page_number', 1)"
    page_calls = [c for c in calls if "paper_chunk_pages" in c[0]]
    assert len(page_calls) == 1, page_calls
    assert page_calls[0][0].startswith(want_sql) and page_calls[0][1] == want_params
    if want_sql.startswith("INSERT"):
        assert page_calls[0][0].endswith("ON CONFLICT (chunk_id) DO UPDATE SET page_start = EXCLUDED.page_start;")
    assert calls[-1][0].startswith("UPDATE papers SET status = 'chunked'")


def test_save_chunks_page_rows_use_each_returned_id():
    from services.common.db import save_sections_and_chunks

    conn = FakeConn()
    save_sections_and_chunks(conn, 7, [{"title": "Intro", "level": 1}], [_chunk(0, page_start=2), _chunk(1), _chunk(2, page_start=5)])
    page_calls = [(c[0].split()[0], c[1]) for c in conn.cur.calls if "paper_chunk_pages" in c[0]]
    assert page_calls == [("INSERT", (101, 2)), ("DELETE", (102,)), ("INSERT", (103, 5))]
    assert conn.commits == 0

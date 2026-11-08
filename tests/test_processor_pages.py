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


# --- AI-11: TokenChunker records each chunk's start page (`page_start`) -------------------------------------------


def build_fixture_doc_ac5():
    """Body text before any heading on page 1 (the `Abstract` bucket), then `1 Introduction` as page 1's last line;
    the Introduction body starts on page 2 and runs into page 3."""
    doc = pymupdf.open()
    layout = [
        [(t, BODY_PT) for t in _body_lines(1, 30)] + [("1 Introduction", HEADING_PT)],
        [(t, BODY_PT) for t in _body_lines(2, 40)],
        [(t, BODY_PT) for t in _body_lines(3, 20)],
    ]
    for lines in layout:
        page = doc.new_page()
        y = 60
        for text, size in lines:
            page.insert_text((72, y), text, fontname="helv", fontsize=size)
            y += LEADING if size == BODY_PT else 2 * LEADING
    return doc


def _parse(doc):
    try:
        return ProcessorWorker._parse_doc_sections(None, doc)
    finally:
        doc.close()


def _assert_chunks_on_oracle_page(chunks):
    assert chunks
    for c in chunks:
        assert "page_start" in c, f"chunk {c['chunk_index']}: no page_start"
        assert c["page_start"] >= 1, c["page_start"]  # 0 would hit paper_chunk_pages' CHECK and fail the transaction
        body = c["content"].split("\n\n", 1)[1]
        assert c["page_start"] == first_word_page(body), f"chunk {c['chunk_index']}: {c['page_start']} vs {body[:40]!r}"


def test_chunk_page_start_matches_first_word_page(chunker):
    sections = parse_fixture_sections()
    chunks = chunker.chunk_sections(FIXTURE_TITLE, sections)
    _assert_chunks_on_oracle_page(chunks)
    sec_page = {s["title"]: s["page"] for s in sections}
    assert any(c["page_start"] > sec_page[c["section_title"]] for c in chunks), [c["page_start"] for c in chunks]


def test_chunk_page_start_abstract_bucket_and_heading_at_page_end(chunker):
    sections = _parse(build_fixture_doc_ac5())
    assert [(s["title"], s["page"]) for s in sections] == [("Abstract", 1), ("Introduction", 1)]
    assert sections[0]["page_breaks"] == [(0, 1)]
    assert sections[1]["page_breaks"][0] == (0, 2)
    chunks = chunker.chunk_sections(FIXTURE_TITLE, sections)
    _assert_chunks_on_oracle_page(chunks)
    assert {c["section_title"] for c in chunks} == {"Abstract", "Introduction"}
    intro = [c for c in chunks if c["section_title"] == "Introduction"]
    assert intro[0]["page_start"] == 2 and intro[0]["page_number"] == 1


# Hand-made sections (no PDF). Words come from the single-wordpiece fixture vocabulary in a seeded random order, so every
# chunk body occurs once in the text and its first character is found by `str.find`, independently of offset mappings.
HAND_WORDS = sorted(WORD_PAGE)
LEAD = "\n \t "


def _hand_text(n=150, seed=11):
    import random

    rng = random.Random(seed)
    return " ".join(rng.choice(HAND_WORDS) for _ in range(n))


def _word_char(text, i):
    """Char offset of word `i` in a single-space-joined `text`."""
    return len(" ".join(text.split(" ")[:i])) + (1 if i else 0)


@pytest.fixture(scope="module")
def small_chunker(chunker):
    import copy

    c = copy.copy(chunker)  # same tokenizer, small windows (several per section, 50% overlap)
    c.target_tokens, c.overlap_tokens, c.min_tokens = 60, 20, 5
    return c


def _hand_chunks(c, raw, **sec):
    return c.chunk_sections("T", [{"title": "S", "text": raw, "page": 1, **sec}])


def _body(chunk):
    return chunk["content"].split("\n\n", 1)[1]


def _chunk_char(raw, chunk):
    body = _body(chunk)
    assert raw.count(body) == 1, f"chunk body not unique in text: {body[:40]!r}"
    return raw.find(body)


def _oracle(raw, breaks, chunk):
    pos = _chunk_char(raw, chunk)
    pages = [p for o, p in breaks if o <= pos]
    return pages[-1] if pages else None


def _no_page(chunks):
    return [{k: v for k, v in c.items() if k != "page_start"} for c in chunks]


def _hand_case(name, c):
    text = _hand_text()
    base = _hand_chunks(c, text)
    if name == "a-known-offsets":
        return text, [(0, 1), (_word_char(text, 50), 2), (_word_char(text, 97), 3)]
    if name == "b-straddle":
        return text, [(0, 1), (_word_char(text, 30), 2)]
    if name == "c-exact-break":
        return text, [(0, 1), (_chunk_char(text, base[1]), 2), (_chunk_char(text, base[2]), 3)]
    if name == "c-break-inside-first-word":  # first char before the break, rest of the first token after it
        return text, [(0, 1), (_chunk_char(text, base[1]) + 1, 2)]
    if name == "d-leading-ws":
        return LEAD + text, [(0, 1), (len(LEAD) + _chunk_char(text, base[1]), 2)]
    if name == "first-break-after-start":
        return text, [(_word_char(text, 30), 4)]
    raise ValueError(name)


@pytest.mark.parametrize("name", [
    "a-known-offsets", "b-straddle", "c-exact-break", "c-break-inside-first-word", "d-leading-ws", "first-break-after-start",
])
def test_page_start_hand_made_sections(small_chunker, name):
    raw, breaks = _hand_case(name, small_chunker)
    got = _hand_chunks(small_chunker, raw, page_breaks=breaks)
    assert len(got) >= 4
    assert _no_page(got) == _hand_chunks(small_chunker, raw), "(g) chunks minus page_start must equal the no-breaks output"
    for ch in got:
        want = _oracle(raw, breaks, ch)
        assert ch.get("page_start") == want and (want is not None or "page_start" not in ch), (ch["chunk_index"], want)
    pages = [ch.get("page_start") for ch in got]
    if name == "a-known-offsets":
        assert set(pages) == {1, 2, 3}, pages
    elif name == "b-straddle":
        b = breaks[1][0]
        straddle = [ch for ch in got if _chunk_char(raw, ch) < b < _chunk_char(raw, ch) + len(_body(ch))]
        assert straddle and all(ch["page_start"] == 1 for ch in straddle), pages
    elif name in ("c-exact-break", "d-leading-ws"):
        assert _chunk_char(raw, got[1]) == breaks[1][0] and pages[:2] == [1, 2], pages
    elif name == "c-break-inside-first-word":
        assert _chunk_char(raw, got[1]) == breaks[1][0] - 1 and pages[:3] == [1, 1, 2], pages
    else:
        assert None in pages and 4 in pages, pages


class _TokProxy:
    """Delegates to the real tokenizer except for `is_fast` and `__call__`."""

    def __init__(self, tok, is_fast, call):
        self._tok, self.is_fast, self._call = tok, is_fast, call

    def __getattr__(self, name):
        return getattr(self._tok, name)

    def __call__(self, *args, **kwargs):
        return self._call(self._tok, *args, **kwargs)


def _call_must_not_happen(tok, *args, **kwargs):
    raise AssertionError("a slow tokenizer has no offset mapping; it must not be called with return_offsets_mapping")


def _call_ids_differ(tok, *args, **kwargs):
    enc = tok(*args, **kwargs)
    ids = list(enc["input_ids"])
    return {"input_ids": ids[:-1] + [ids[-1] + 1], "offset_mapping": enc["offset_mapping"]}


@pytest.mark.parametrize(
    "variant, sec",
    [
        ("real", {}),
        ("real", {"page_breaks": []}),
        ("slow", {"page_breaks": [(0, 1), (100, 2)]}),
        ("ids-differ", {"page_breaks": [(0, 1), (100, 2)]}),
    ],
    ids=["no-key", "empty-list", "is_fast-false", "call-ids-differ"],
)
def test_page_start_absent_when_unknown(small_chunker, variant, sec):
    import copy

    c = copy.copy(small_chunker)
    if variant == "slow":
        c.tokenizer = _TokProxy(small_chunker.tokenizer, False, _call_must_not_happen)
    elif variant == "ids-differ":
        c.tokenizer = _TokProxy(small_chunker.tokenizer, True, _call_ids_differ)
    text = _hand_text()
    got = _hand_chunks(c, text, **sec)
    assert len(got) >= 4
    assert not [ch for ch in got if "page_start" in ch]
    assert got == _hand_chunks(small_chunker, text), "(f)/(g) chunks must equal the no-breaks output"

// Citation status rule (ADR-0003, ADR-0014; design docs/design/FE-3.md §1), shared by the card chip, the footer
// summary and the Markdown export. Plain ESM, no JSX. Conservative on purpose: a reserved or unknown status, or a
// `status`/`supported` disagreement, can never read as Supported. No output of this module says "verified".

const pct = (score) => `${((Number(score) || 0) * 100).toFixed(1)}%`;

function kindOf(cit, refusal) {
  const { status, supported, score } = cit;
  if (status === 'supported' && supported !== false) return 'supported';
  if (status === 'unverified') return 'unchecked';
  if (!status && refusal && !score) return 'unchecked';
  if (!status && supported === true) return 'supported';
  return 'unsupported';
}

// → { kind: 'supported' | 'unsupported' | 'unchecked', text, tooltip }
export function citationState(cit, { refusal = false } = {}) {
  const kind = kindOf(cit, refusal);
  const score = pct(cit.score);
  if (kind === 'supported') {
    return {
      kind,
      text: `Supported · ${score}`,
      tooltip: `Supported: a check found that this passage backs the sentence citing it (support score ${score}).`,
    };
  }
  if (kind === 'unsupported') {
    return {
      kind,
      text: `Unsupported · ${score}`,
      tooltip: refusal
        ? `Unsupported: a draft statement citing this passage was checked and not supported (best support score ${score}).`
        : `Unsupported: this passage was checked and doesn't back the sentence citing it (support score ${score}). Read it before relying on that sentence.`,
    };
  }
  return {
    kind,
    text: 'Not checked',
    tooltip: 'Not checked: no check ran for this passage, because the checker was unavailable or no sentence cited it.',
  };
}

// Markdown research note for an answer (answer mode only; a refusal has no answer to export).
export function markdownNote(answer, citations) {
  let md = `# ArxivLens Research Note\n\n${answer}\n\n## References\n`;
  (citations || []).forEach((c) => {
    const pdf = c.pdf_url || `https://arxiv.org/pdf/${c.arxiv_id}.pdf`;
    const section = c.section ? ` (§ ${c.section})` : '';
    md += `- [${c.marker}] **${c.title}**${section} - [arXiv:${c.arxiv_id}](${pdf}) • ${citationState(c).text}\n`;
  });
  return md;
}

// Claim verdict (design FE-4 §1). Claims carry no score; same conservative order as citationState: only
// `supported` reads Supported, `unverified` reads Not checked, anything else (reserved, unknown, missing) Unsupported.
export function claimState(claim) {
  const status = claim?.status;
  if (status === 'supported') return { kind: 'supported', text: 'Supported' };
  if (status === 'unverified') return { kind: 'unchecked', text: 'Not checked' };
  return { kind: 'unsupported', text: 'Unsupported' };
}

// True when the response carries a usable claims list; otherwise the view falls back to the prose (FE-3).
export const hasClaims = (claims) =>
  Array.isArray(claims) && claims.length > 0 && claims.every((c) => typeof c?.text === 'string');

const markersOf = (claim) => (Array.isArray(claim?.markers) ? claim.markers : []);

// Positional mapping (rag.go: claim i's citations are citations[o_i : o_i + len(markers_i)]).
// → [{ claim, index, entries: [{ marker, citation, pos }] }], or null when the response breaks the invariant,
// so a malformed response can never pin a claim to the wrong passage.
export function claimCitations(claims, citations) {
  const cits = citations || [];
  const rows = [];
  let o = 0;
  for (const [index, claim] of claims.entries()) {
    const entries = markersOf(claim).map((marker, j) => ({ marker, citation: cits[o + j] ?? null, pos: o + j }));
    if (entries.some((e) => e.citation?.marker !== e.marker)) return null;
    rows.push({ claim, index, entries });
    o += entries.length;
  }
  return o === cits.length ? rows : null;
}

// Rows for the claims list: positional when valid, else each marker maps to the first citation with its number
// (citation null, pos undefined when there is none).
export function claimRows(claims, citations) {
  const exact = claimCitations(claims, citations);
  if (exact) return exact;
  const cits = citations || [];
  return claims.map((claim, index) => ({
    claim,
    index,
    entries: markersOf(claim).map((marker) => {
      const pos = cits.findIndex((c) => c.marker === marker);
      return pos < 0 ? { marker, citation: null, pos: undefined } : { marker, citation: cits[pos], pos };
    }),
  }));
}

// One card per distinct marker, in first-seen order (a written answer can cite one passage from several claims).
export function groupByMarker(citations) {
  const groups = new Map();
  (citations || []).forEach((citation, pos) => {
    if (!groups.has(citation.marker)) groups.set(citation.marker, { marker: citation.marker, entries: [] });
    groups.get(citation.marker).entries.push({ citation, pos });
  });
  return [...groups.values()];
}

// --- FE-4b: quote location, provenance line, PDF page (design FE-4 §1, PM condition 5) ---

// Go RE2 `\s` is ASCII, so the two regex steps of normQuote (rag.go) use this class, never JS `\s`.
const RE2_SPACE = '[\\t\\n\\f\\r ]';
const INLINE_REF = new RegExp(`${RE2_SPACE}*\\[\\d+(?:${RE2_SPACE}*[,\u2013-]${RE2_SPACE}*\\d+)*\\]`, 'g');
// strings.Fields splits on unicode.IsSpace: ASCII spaces, \v, U+0085, U+00A0 and the Z categories (not U+FEFF).
const GO_SPACE = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/;
const PUNCT = '.,;:!?';

// normQuote with an offset map: → { text, at } where at[i] is the raw index of text[i].
function normWithMap(raw) {
  const kept = [];
  let last = 0;
  for (const m of raw.matchAll(INLINE_REF)) {
    for (let i = last; i < m.index; i += 1) kept.push(i);
    last = m.index + m[0].length;
  }
  for (let i = last; i < raw.length; i += 1) kept.push(i);
  // strings.Join(strings.Fields(s), " "): trim, collapse each whitespace run to one space at its first raw index.
  const fields = [];
  let gap = null;
  for (const i of kept) {
    if (GO_SPACE.test(raw[i])) {
      if (gap === null) gap = i;
    } else {
      if (gap !== null && fields.length) fields.push({ ch: ' ', i: gap });
      gap = null;
      fields.push({ ch: raw[i], i });
    }
  }
  // spaceBeforePunct: after Fields the only whitespace left is a single ' ', so drop each one before .,;:!?
  const out = fields.filter((f, k) => !(f.ch === ' ' && PUNCT.includes(fields[k + 1]?.ch)));
  return { text: out.map((f) => f.ch).join(''), at: out.map((f) => f.i) };
}

export const normQuote = (s) => normWithMap(String(s ?? '')).text;

// → { start, end } in raw `content` whose text normalises to the quote (case-sensitive), or null.
export function locateQuote(content, quote) {
  if (typeof content !== 'string' || typeof quote !== 'string' || !quote.trim()) return null;
  const direct = content.indexOf(quote);
  if (direct >= 0) return { start: direct, end: direct + quote.length };
  const q = normQuote(quote);
  if (!q) return null;
  const { text, at } = normWithMap(content);
  const k = text.indexOf(q);
  if (k < 0) return null;
  return { start: at[k], end: at[k + q.length - 1] + 1 };
}

export const validPage = (page) => Number.isInteger(page) && page >= 1;

// PDF link: pdf_url (already versioned) or the arXiv fallback, plus #page=N only for a valid page.
export function pdfHref(cit) {
  const base = cit.pdf_url || `https://arxiv.org/pdf/${cit.arxiv_id}.pdf`;
  return validPage(cit.page) ? `${base.split('#')[0]}#page=${cit.page}` : base;
}

// "arXiv:2401.01234v2"; no "v" part when paper_version is 0 or absent.
export function arxivLabel(cit) {
  const v = cit.paper_version;
  return `arXiv:${cit.arxiv_id}${Number.isInteger(v) && v > 0 ? `v${v}` : ''}`;
}

// "arXiv:<id>v<N> · § <section> · p. <N>"; section omitted when empty, page only when valid.
export function metaLine(cit) {
  return [arxivLabel(cit), cit.section && `§ ${cit.section}`, validPage(cit.page) && `p. ${cit.page}`]
    .filter(Boolean)
    .join(' · ');
}

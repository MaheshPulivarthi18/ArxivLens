// The one BibTeX builder (design FE-4 › B-UI addendum §2). Plain ESM, no JSX. It writes only fields the API returns:
// no author (the API has none), no primaryClass, and a year only when the arXiv ID itself gives it.
import { citationState } from './citations';

const baseId = (id) => String(id ?? '').trim().replace(/v\d+$/, '');

// First-version year from the arXiv ID: "2401.01234" → 2024, "cs/0601001" → 2006 (YY ≥ 91 → 19YY); else null.
export function arxivYear(id) {
  const s = baseId(id);
  const m = s.match(/^(\d{2})(\d{2})\.\d{4,5}$/) || s.match(/^[a-z-]+(?:\.[A-Za-z-]+)?\/(\d{2})(\d{2})\d{3}$/);
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return null;
  const yy = Number(m[1]);
  return yy >= 91 ? 1900 + yy : 2000 + yy;
}

// "arxiv" + the ID without its version and punctuation: stable on every call. Empty ID → null.
export function bibKey(id) {
  const s = baseId(id).replace(/[^A-Za-z0-9]/g, '');
  return s ? `arxiv${s}` : null;
}

const ESCAPES = { '\\': '\\textbackslash{}', '{': '\\{', '}': '\\}', '&': '\\&', '%': '\\%', $: '\\$', '#': '\\#', _: '\\_' };
export const bibEscape = (s) => String(s).replace(/[\\{}&%$#_]/g, (c) => ESCAPES[c]);

// One @misc entry (arXiv's own export form), or null when the citation has no usable ID.
export function bibEntry(cit) {
  const key = bibKey(cit?.arxiv_id);
  if (!key) return null;
  const id = String(cit.arxiv_id).trim();
  const year = arxivYear(id);
  const title = String(cit.title ?? '').trim();
  const fields = [
    title && `  title = {${bibEscape(title)}}`,
    `  eprint = {${id}}`,
    '  archivePrefix = {arXiv}',
    year && `  year = {${year}}`,
    `  url = {https://arxiv.org/abs/${id}}`,
  ].filter(Boolean);
  return `@misc{${key},\n${fields.join(',\n')}\n}`;
}

export const BIB_HEADER =
  '% ArxivLens export. Author fields are not included: ArxivLens does not return authors yet; see each url.';

// The citations the answer shows (a written answer shows only supported ones, ADR-0019; quoted or absent kind → all),
// one entry per arxiv_id in first-seen order. → { text, count }
export function bibFile(citations, { answerKind } = {}) {
  const shown = (citations || []).filter(
    (c) => answerKind !== 'synthesized' || citationState(c).kind === 'supported',
  );
  const seen = new Set();
  const entries = [];
  for (const c of shown) {
    const key = bibKey(c.arxiv_id);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    entries.push(bibEntry(c));
  }
  return { text: `${BIB_HEADER}\n\n${entries.join('\n\n')}\n`, count: entries.length };
}

const pad = (n) => String(n).padStart(2, '0');

// "arxivlens-20261128-142530.bib", local time.
export function bibFilename(d = new Date()) {
  const date = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}`;
  return `arxivlens-${date}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}.bib`;
}

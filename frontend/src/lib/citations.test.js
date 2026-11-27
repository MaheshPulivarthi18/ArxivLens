import { describe, it, expect } from 'vitest';
import {
  citationState, markdownNote, claimState, hasClaims, claimCitations, claimRows, groupByMarker,
  locateQuote, normQuote, pdfHref, arxivLabel, metaLine,
} from './citations';
import {
  supportedCitation,
  unsupportedCitation,
  unverifiedCitation,
  contradictorySupported,
  partialCitation,
  contradictedCitation,
  legacyRefusalZero,
  quotedClaims,
  synthesizedSkips,
  brokenPositional,
  malformedClaims,
} from '../test/fixtures';

const SUPPORTED_WORD = /\bSupported\b/;
const VERIFIED = /verified/i;

// Design FE-3 §1, rows 1–5, in both modes where it matters.
const ROWS = [
  ['row 1: status supported', supportedCitation, false, 'supported', 'Supported · 87.3%'],
  ['row 1: status supported (refusal mode)', supportedCitation, true, 'supported', 'Supported · 87.3%'],
  ['row 2: status unverified', unverifiedCitation, false, 'unchecked', 'Not checked'],
  ['row 2: status unverified (refusal mode)', unverifiedCitation, true, 'unchecked', 'Not checked'],
  ['row 3: no status, score 0, refusal mode', legacyRefusalZero, true, 'unchecked', 'Not checked'],
  ['row 4: no status, supported: true', { ...supportedCitation, status: undefined }, false, 'supported', 'Supported · 87.3%'],
  ['row 5: status unsupported', unsupportedCitation, false, 'unsupported', 'Unsupported · 12.0%'],
  ['row 5: status supported but supported: false', contradictorySupported, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: reserved partially_supported', partialCitation, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: reserved contradicted', contradictedCitation, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: unknown status "bogus"', { ...supportedCitation, status: 'bogus' }, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: unknown status "bogus" (refusal mode)', { ...supportedCitation, status: 'bogus' }, true, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: no status, supported: false (answer mode)', { ...unsupportedCitation, status: undefined }, false, 'unsupported', 'Unsupported · 12.0%'],
  ['row 5: no status, score 0 outside a refusal', legacyRefusalZero, false, 'unsupported', 'Unsupported · 0.0%'],
  ['refusal A: checked passage keeps its best score', { ...unsupportedCitation, score: 0.31 }, true, 'unsupported', 'Unsupported · 31.0%'],
];

describe('citationState', () => {
  it.each(ROWS)('%s', (_name, cit, refusal, kind, text) => {
    const state = citationState(cit, { refusal });
    expect(state.kind).toBe(kind);
    expect(state.text).toBe(text);
    expect(state.tooltip.length).toBeGreaterThan(0);
  });

  it.each(ROWS)('%s: only a supported kind says "Supported", nothing says "verified"', (_name, cit, refusal) => {
    const state = citationState(cit, { refusal });
    for (const out of [state.text, state.tooltip]) {
      expect(out).not.toMatch(VERIFIED);
      if (state.kind === 'supported') expect(out).toMatch(SUPPORTED_WORD);
      else expect(out).not.toMatch(SUPPORTED_WORD);
    }
  });

  it('a "Not checked" chip shows no percentage', () => {
    expect(citationState(unverifiedCitation).text).not.toMatch(/%/);
    expect(citationState(legacyRefusalZero, { refusal: true }).text).not.toMatch(/%/);
  });

  it('defaults to answer mode', () => {
    expect(citationState(legacyRefusalZero).kind).toBe('unsupported');
  });

  it('tooltips describe the meaning without naming a checking method', () => {
    for (const [, cit, refusal] of ROWS) {
      expect(citationState(cit, { refusal }).tooltip).not.toMatch(/entail|nli|cosine|embedding/i);
    }
  });
});

describe('markdownNote', () => {
  const md = markdownNote('An answer [1].', [supportedCitation, unsupportedCitation, unverifiedCitation]);

  it('uses a plain "## References" heading and never says verified', () => {
    expect(md).toContain('\n## References\n');
    expect(md).not.toMatch(VERIFIED);
  });

  it('ends each entry with its chip text', () => {
    const lines = md.trim().split('\n').filter((l) => l.startsWith('- ['));
    expect(lines).toHaveLength(3);
    expect(lines[0]).toMatch(/• Supported · 87\.3%$/);
    expect(lines[1]).toBe(
      '- [2] **Entailment Checks for Citations** (§ Results) - [arXiv:2402.05678](https://arxiv.org/pdf/2402.05678v1) • Unsupported · 12.0%',
    );
    expect(lines[2]).toMatch(/• Not checked$/);
  });

  it('heading is "# ArxivLens Research Note"; "(§ …)" only for a non-empty section', () => {
    expect(md.startsWith('# ArxivLens Research Note\n\n')).toBe(true);
    expect(md).not.toMatch(/Clinical/);
    const bare = markdownNote('A.', [{ ...supportedCitation, section: '' }]);
    expect(bare).toContain(`**${supportedCitation.title}** - [arXiv:`);
    expect(bare).not.toContain('§');
  });
});

// FE-4a (design docs/design/FE-4.md §1).
describe('claimState', () => {
  it.each([
    ['supported', 'supported', 'Supported'],
    ['unverified', 'unchecked', 'Not checked'],
    ['unsupported', 'unsupported', 'Unsupported'],
    ['partially_supported', 'unsupported', 'Unsupported'],
    ['contradicted', 'unsupported', 'Unsupported'],
    ['bogus', 'unsupported', 'Unsupported'],
    [undefined, 'unsupported', 'Unsupported'],
  ])('status %s → %s "%s"', (status, kind, text) => {
    const state = claimState({ text: 'A claim.', status, markers: [] });
    expect(state).toEqual({ kind, text });
    expect(state.text).not.toMatch(/verified|%/i);
    if (kind !== 'supported') expect(state.text).not.toMatch(SUPPORTED_WORD);
  });

  it('a missing claim reads Unsupported', () => {
    expect(claimState(undefined)).toEqual({ kind: 'unsupported', text: 'Unsupported' });
  });
});

describe('hasClaims', () => {
  it('accepts a non-empty list of claims with text', () => {
    expect(hasClaims(quotedClaims.claims)).toBe(true);
  });

  it.each([['absent', undefined], ['empty', []], ...malformedClaims.map((c, i) => [`malformed ${i + 1}`, c])])(
    '%s → false',
    (_name, claims) => {
      expect(hasClaims(claims)).toBe(false);
    },
  );
});

describe('claimCitations', () => {
  it('maps each claim marker to its positional citation entry', () => {
    const rows = claimCitations(synthesizedSkips.claims, synthesizedSkips.citations);
    expect(rows.map((r) => r.entries.map((e) => [e.marker, e.pos]))).toEqual([[[1, 0]], [[1, 1], [3, 2]]]);
    expect(rows[1].entries[0].citation).toBe(synthesizedSkips.citations[1]);
    expect(rows.map((r) => r.index)).toEqual([0, 1]);
  });

  it('an uncited claim has no entries', () => {
    const rows = claimCitations(quotedClaims.claims, quotedClaims.citations);
    expect(rows[3].entries).toEqual([]);
  });

  it('returns null when a marker disagrees with its positional citation', () => {
    expect(claimCitations(brokenPositional.claims, brokenPositional.citations)).toBeNull();
  });

  it('returns null when the citation count differs from the marker count', () => {
    expect(claimCitations(synthesizedSkips.claims, synthesizedSkips.citations.slice(0, 2))).toBeNull();
    expect(claimCitations(synthesizedSkips.claims, [...synthesizedSkips.citations, unsupportedCitation])).toBeNull();
  });
});

describe('claimRows', () => {
  it('uses the positional mapping when it is valid', () => {
    expect(claimRows(synthesizedSkips.claims, synthesizedSkips.citations)).toEqual(
      claimCitations(synthesizedSkips.claims, synthesizedSkips.citations),
    );
  });

  it('broken positional invariant: markers still map by number', () => {
    const rows = claimRows(brokenPositional.claims, brokenPositional.citations);
    expect(rows.map((r) => r.entries.map((e) => [e.marker, e.citation.marker, e.pos]))).toEqual([
      [[1, 1, 1]],
      [[1, 1, 1], [3, 3, 0]],
    ]);
  });

  it('a marker with no citation gets citation null and no position', () => {
    const rows = claimRows([{ text: 'x', status: 'supported', markers: [9] }], [supportedCitation]);
    expect(rows[0].entries).toEqual([{ marker: 9, citation: null, pos: undefined }]);
  });
});

describe('groupByMarker', () => {
  it('one group per distinct marker, in first-seen order, entries keep their positions', () => {
    const groups = groupByMarker(synthesizedSkips.citations);
    expect(groups.map((g) => [g.marker, g.entries.map((e) => e.pos)])).toEqual([[1, [0, 1]], [3, [2]]]);
    expect(groups[0].entries[1].citation).toBe(synthesizedSkips.citations[1]);
    expect(groupByMarker(brokenPositional.citations).map((g) => g.marker)).toEqual([3, 1]);
  });

  it('no citations → no groups', () => {
    expect(groupByMarker(undefined)).toEqual([]);
    expect(groupByMarker([])).toEqual([]);
  });
});

// B-UI AC1 (design FE-4 §1, PM condition 5): normQuote mirror, case-sensitive.
describe('locateQuote', () => {
  it.each([
    ['exact', 'We verify each claim here.', 'verify each claim', 'verify each claim'],
    ['\\n inside', 'hybrid\nsearch works', 'hybrid search works', 'hybrid\nsearch works'],
    ['double spaces', 'a  b   c', 'a b c', 'a  b   c'],
    ['inline [12]', 'shown [12] here.', 'shown here.', 'shown [12] here.'],
    ['inline [3, 4]', 'shown [3, 4] here', 'shown here', 'shown [3, 4] here'],
    ['inline [5–7]', 'shown [5–7] here', 'shown here', 'shown [5–7] here'],
    ['space before .', 'the claim . Next', 'the claim.', 'the claim .'],
    ['case mismatch', 'The Claim holds', 'the claim holds', null],
    ['empty quote', 'abc', '', null],
    ['whitespace quote', 'a \n b', ' \n ', null],
    ['absent', 'abc', 'xyz', null],
    // Something before the quote normalises away: the raw start is not the normalised index.
    ['ref + spaces before', 'As shown [12]  earlier, hybrid\nsearch works well.', 'hybrid search works', 'hybrid\nsearch works'],
    ['newlines + spaces before', 'Intro.\n\n\nWe  verify  each claim.', 'We verify each claim.', 'We  verify  each claim.'],
    // strings.Fields collapses Unicode spaces (unicode.IsSpace), e.g. NBSP and U+2009 from PDF text.
    ['NBSP between words', 'hybrid\u00a0search works', 'hybrid search works', 'hybrid\u00a0search works'],
    ['U+2009 between words', 'a\u2009b c', 'a b c', 'a\u2009b c'],
    // RE2 \s is ASCII: NBSP inside [3, 4] keeps the ref in the server's normalised text.
    ['NBSP [3,\u00a04] keeps the ref', 'shown in [3,\u00a04] that', 'shown in that', null],
  ])('%s', (_name, content, quote, raw) => {
    const r = locateQuote(content, quote);
    if (raw === null) return expect(r).toBeNull();
    expect(content.slice(r.start, r.end)).toBe(raw);
    expect(normQuote(raw)).toBe(normQuote(quote));
  });
});

it('locateQuote returns exact raw offsets after a normalised prefix', () => {
  expect(locateQuote('As shown [12]  earlier, hybrid\nsearch works well.', 'hybrid search works')).toEqual({ start: 24, end: 43 });
  expect(locateQuote('Intro.\n\n\nWe  verify  each claim.', 'We verify each claim.')).toEqual({ start: 9, end: 32 });
});

// B-UI AC2: provenance line and PDF page.
describe('pdfHref / arxivLabel / metaLine', () => {
  const base = { ...supportedCitation, page: undefined };
  it.each([
    [4, 'https://arxiv.org/pdf/2401.01234v2#page=4', 'arXiv:2401.01234v2 · § Methods · p. 4'],
    [0, 'https://arxiv.org/pdf/2401.01234v2', 'arXiv:2401.01234v2 · § Methods'],
    [null, 'https://arxiv.org/pdf/2401.01234v2', 'arXiv:2401.01234v2 · § Methods'],
    ['4', 'https://arxiv.org/pdf/2401.01234v2', 'arXiv:2401.01234v2 · § Methods'],
    [undefined, 'https://arxiv.org/pdf/2401.01234v2', 'arXiv:2401.01234v2 · § Methods'],
  ])('page %s', (page, href, meta) => {
    expect(pdfHref({ ...base, page })).toBe(href);
    expect(metaLine({ ...base, page })).toBe(meta);
  });

  it('replaces an existing fragment, falls back to the arXiv URL, and drops v0 and an empty section', () => {
    expect(pdfHref({ ...base, pdf_url: 'https://x.org/a.pdf#view=fit', page: 2 })).toBe('https://x.org/a.pdf#page=2');
    expect(pdfHref({ ...base, pdf_url: '', page: 3 })).toBe('https://arxiv.org/pdf/2401.01234.pdf#page=3');
    expect(arxivLabel({ ...base, paper_version: 0 })).toBe('arXiv:2401.01234');
    expect(arxivLabel({ ...base, paper_version: undefined })).toBe('arXiv:2401.01234');
    expect(metaLine({ ...base, section: '', paper_version: 0, page: 7 })).toBe('arXiv:2401.01234 · p. 7');
  });
});

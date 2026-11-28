import { afterEach, describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ReferencesStack from './ReferencesStack';
import { citationState } from '../lib/citations';
import { bibEntry } from '../lib/bibtex';
import {
  supportedCitation,
  unsupportedCitation,
  unverifiedCitation,
  refusalA,
  refusalB,
  refusalC,
  synthesizedSkips,
  quotedClaims,
  quotedAnswer,
} from '../test/fixtures';

const VERIFIED = /\bVerified\b/;

function renderStack(citations, onCopy = vi.fn(), refusalReason) {
  render(<ReferencesStack citations={citations} activeMarker={null} onCopy={onCopy} refusalReason={refusalReason} />);
  return onCopy;
}

const card = (marker) => screen.getByRole('article', { name: new RegExp(`Citation \\[${marker}\\]`) });

describe('ReferencesStack', () => {
  it('shows title, the exact meta line and a one-click link to pdf_url (at the page when known)', () => {
    renderStack([supportedCitation, unsupportedCitation]);
    for (const [cit, meta, href] of [
      [supportedCitation, 'arXiv:2401.01234v2 · § Methods · p. 4', `${supportedCitation.pdf_url}#page=4`],
      [unsupportedCitation, 'arXiv:2402.05678v1 · § Results', unsupportedCitation.pdf_url],
    ]) {
      const c = within(card(cit.marker));
      expect(c.getByText(cit.title)).toBeInTheDocument();
      expect(c.getByText(meta).textContent).toBe(meta);
      expect(c.getByRole('link', { name: /open pdf/i })).toHaveAttribute('href', href);
    }
    expect(document.body.textContent).not.toMatch(/Chunk #|General|Section:/);
  });

  it('falls back to the arXiv PDF URL when pdf_url is empty', () => {
    renderStack([{ ...supportedCitation, pdf_url: '' }]);
    expect(screen.getByRole('link', { name: /open pdf/i })).toHaveAttribute(
      'href',
      `https://arxiv.org/pdf/${supportedCitation.arxiv_id}.pdf#page=4`,
    );
  });

  it('answer mode: chips read "Supported · 87.3%" and "Unsupported · 12.0%", heading "References"', () => {
    renderStack([supportedCitation, unsupportedCitation]);
    expect(within(card(1)).getByText('Supported · 87.3%')).toBeInTheDocument();
    expect(within(card(2)).getByText('Unsupported · 12.0%')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /^References/ })).toHaveAttribute('id', 'references-heading');
    expect(screen.getByText('Click [n] in text to inspect')).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/verified/i);
    // the old cosine gauge is gone; the score appears once, in the chip
    expect(screen.queryByText(/Cosine/)).not.toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  });

  it('flags supported: false visibly and never calls it Verified', () => {
    renderStack([supportedCitation, unsupportedCitation]);
    const c = within(card(2));
    expect(c.getByText(/Unsupported · 12\.0%/)).toBeInTheDocument();
    expect(c.queryByText(VERIFIED)).not.toBeInTheDocument();
    // still listed, not hidden
    expect(c.getByText(unsupportedCitation.title)).toBeInTheDocument();
  });

  it('flags supported: false even without a status field (older responses)', () => {
    renderStack([{ ...unsupportedCitation, status: undefined }]);
    expect(screen.getByText(/Unsupported/)).toBeInTheDocument();
    expect(screen.queryByText(VERIFIED)).not.toBeInTheDocument();
  });

  it('shows an unchecked citation (verifier outage, score 0) as "Not checked", with no percentage', () => {
    renderStack([unverifiedCitation]);
    expect(within(card(3)).getByText('Not checked')).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/0\.0%|verified/i);
  });

  it('refusal A: heading "Retrieved passages (no answer was shown)", "Unsupported · 31.0%" and "Not checked" chips, no "0.0%"', () => {
    renderStack(refusalA.citations, vi.fn(), refusalA.refusal_reason);
    const heading = screen.getByRole('heading', { name: /Retrieved passages \(no answer was shown\)/ });
    expect(heading).toHaveAttribute('id', 'references-heading');
    expect(heading).toHaveAttribute('tabindex', '-1');
    expect(heading).toHaveTextContent('2 passages');
    expect(within(card(1)).getByText('Unsupported · 31.0%')).toBeInTheDocument();
    expect(within(card(2)).getByText('Not checked')).toBeInTheDocument();
    expect(screen.queryByText('Click [n] in text to inspect')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/0\.0%|verified/i);
    // every passage stays one click from its PDF
    expect(screen.getAllByRole('link', { name: /open pdf/i })).toHaveLength(2);
  });

  it('refusal B: "Unsupported · 22.0%" and "Not checked" chips under "2 passages"', () => {
    renderStack(refusalB.citations, vi.fn(), refusalB.refusal_reason);
    const heading = screen.getByRole('heading', { name: /Retrieved passages \(no answer was shown\)/ });
    expect(heading).toHaveTextContent('2 passages');
    expect(within(card(1)).getByText('Unsupported · 22.0%')).toBeInTheDocument();
    expect(within(card(2)).getByText('Not checked')).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/0\.0%|verified/i);
  });

  it('refusal mode with one passage: heading badge reads "1 passage"', () => {
    renderStack([refusalA.citations[0]], vi.fn(), refusalA.refusal_reason);
    const heading = screen.getByRole('heading', { name: /Retrieved passages \(no answer was shown\)/ });
    expect(heading).toHaveTextContent(/\b1 passage$/);
  });

  it('refusal C: every chip reads "Not checked"', () => {
    renderStack(refusalC.citations, vi.fn(), refusalC.refusal_reason);
    expect(screen.getByRole('heading', { name: /Retrieved passages \(no answer was shown\)/ })).toBeInTheDocument();
    expect(screen.getAllByText('Not checked')).toHaveLength(2);
    expect(document.body.textContent).not.toMatch(/0\.0%|verified|\bSupported\b/i);
  });

  it('refusal mode: a legacy citation without status and score 0 reads "Not checked"', () => {
    renderStack([{ ...unverifiedCitation, status: undefined }], vi.fn(), 'all_citations_unsupported');
    expect(screen.getByText('Not checked')).toBeInTheDocument();
  });

  it('focusing a status chip opens a tooltip with its meaning (keyboard reachable)', async () => {
    const user = userEvent.setup();
    renderStack([supportedCitation, unsupportedCitation]);
    const chip = within(card(1)).getByRole('button', { name: 'Supported · 87.3%' });
    expect(chip).toHaveAttribute('type', 'button');
    // Tab from the top of the page until the chip has focus (bounded; the heading is tabIndex -1).
    for (let i = 0; i < 5 && document.activeElement !== chip; i += 1) await user.tab();
    expect(chip).toHaveFocus();
    const tip = await screen.findByRole('tooltip');
    expect(tip).toHaveTextContent(
      /^Supported: a check found that this passage backs the sentence citing it \(support score 87\.3%\)\.$/,
    );
    expect(chip).toHaveAttribute('aria-describedby', tip.id);
    expect(tip.textContent).not.toMatch(/entails|NLI|cosine/i);
  });

  it('every chip is a button named by its text, with no title attribute (meaning not announced twice)', () => {
    renderStack([supportedCitation, unsupportedCitation, unverifiedCitation]);
    for (const [marker, name] of [[1, 'Supported · 87.3%'], [2, 'Unsupported · 12.0%'], [3, 'Not checked']]) {
      expect(within(card(marker)).getByRole('button', { name })).toHaveAttribute('type', 'button');
    }
    expect(document.querySelectorAll('[title]')).toHaveLength(0);
  });

  it('tooltip copy names no checking method, in answer and refusal mode', () => {
    const cits = [supportedCitation, unsupportedCitation, unverifiedCitation, ...refusalA.citations, ...refusalC.citations];
    for (const refusal of [false, true]) {
      for (const cit of cits) expect(citationState(cit, { refusal }).tooltip).not.toMatch(/entails|NLI|cosine/i);
    }
  });

  it('Copy BibTeX passes the shared bibEntry (@misc, no invented year or class) to onCopy', async () => {
    const user = userEvent.setup();
    const onCopy = renderStack([supportedCitation]);
    await user.click(screen.getByRole('button', { name: /copy bibtex for \[1\]/i }));
    expect(onCopy).toHaveBeenCalledTimes(1);
    const [bibtex] = onCopy.mock.calls[0];
    expect(bibtex).toBe(bibEntry(supportedCitation));
    expect(bibtex).toMatch(/^@misc\{arxiv240101234,/);
    expect(bibtex).not.toMatch(/2026|primaryClass/);
    expect(bibtex).toContain(`title = {${supportedCitation.title}}`);
    expect(bibtex).toContain(`eprint = {${supportedCitation.arxiv_id}}`);
  });

  it('shows an empty state when there are no citations', () => {
    renderStack([]);
    expect(screen.getByText(/No reference papers cited/)).toBeInTheDocument();
  });
});

// FE-4a (design docs/design/FE-4.md §3, first two bullets).
describe('ReferencesStack: grouping and active card (FE-4a)', () => {
  const renderWith = (citations, extra = {}) =>
    render(<ReferencesStack citations={citations} onCopy={vi.fn()} traceId="trace-x" {...extra} />);

  it('one card per distinct marker: no duplicate ref-n, heading counts cards, each check keeps its chip', () => {
    renderWith(synthesizedSkips.citations);
    expect(screen.getAllByRole('article')).toHaveLength(2);
    expect(document.querySelectorAll('[id="ref-1"]')).toHaveLength(1);
    expect(document.querySelectorAll('[id="ref-3"]')).toHaveLength(1);
    expect(document.getElementById('ref-2')).toBeNull();
    expect(within(screen.getByRole('heading', { name: /^References/ })).getByText('2 cited')).toBeInTheDocument();
    expect(within(card(1)).getByRole('button', { name: 'Supported · 87.3%' })).toBeInTheDocument();
    expect(within(card(1)).getByRole('button', { name: 'Supported · 80.1%' })).toBeInTheDocument();
    expect(within(card(3)).getByRole('button', { name: 'Supported · 91.2%' })).toBeInTheDocument();
    // every passage stays one click from its PDF
    expect(screen.getAllByRole('link', { name: /open pdf/i })).toHaveLength(2);
  });

  it('cards are programmatic focus targets, not Tab stops', () => {
    renderWith([supportedCitation, unsupportedCitation]);
    for (const m of [1, 2]) {
      expect(card(m)).toHaveAttribute('tabindex', '-1');
      expect(card(m)).toHaveAttribute('id', `ref-${m}`);
    }
  });

  it('nothing is active without an active marker', () => {
    renderWith([supportedCitation, unsupportedCitation]);
    expect(document.querySelectorAll('[data-active]')).toHaveLength(0);
  });

  it('the active marker marks only its own card', () => {
    renderWith(synthesizedSkips.citations, { active: { marker: 3, pos: 2 } });
    expect(card(3)).toHaveAttribute('data-active', 'true');
    expect(card(1)).not.toHaveAttribute('data-active');
    expect(document.querySelectorAll('[data-active]')).toHaveLength(1);
  });

  it('refusal mode ignores active', () => {
    renderWith(refusalA.citations, { active: { marker: 1, pos: 0 }, refusalReason: refusalA.refusal_reason });
    expect(document.querySelectorAll('[data-active]')).toHaveLength(0);
  });
});

// B-UI (design FE-4 §3 + B-UI addendum): quote in passage, provenance, Cited-by rows, Export.
describe('ReferencesStack: evidence and export (B-UI)', () => {
  const renderWith = (citations, extra = {}) =>
    render(<ReferencesStack citations={citations} onCopy={vi.fn()} traceId="t" {...extra} />);
  const marks = () => [...document.querySelectorAll('mark')].map((m) => m.textContent);
  const NOT_FOUND = /Quoted text \(not found in the passage shown above\)/;
  const NO_QUOTE = 'No exact quote for this citation: the check compared the claim with the whole passage.';

  it('AC1: highlights the raw span of a normalised quote, with an sr-only "Quote: "', () => {
    const cit = quotedClaims.citations[0];
    renderWith([cit]);
    expect(marks()).toEqual([`Quote: ${cit.content}`]);
    expect(document.querySelector('mark .sr-only')).toHaveTextContent('Quote:');
    expect(screen.queryByText(NOT_FOUND)).toBeNull();
    expect(screen.queryByText(NO_QUOTE)).toBeNull();
  });

  it('AC1: a quote that is not located gets the fallback block, no mark', () => {
    renderWith([{ ...supportedCitation, quote: 'Not in this passage.' }]);
    expect(marks()).toEqual([]);
    expect(screen.getByText(NOT_FOUND)).toHaveTextContent('“Not in this passage.”');
  });

  it('AC1: a supported citation without a quote gets the no-quote note', () => {
    renderWith([{ ...supportedCitation, quote: undefined }]);
    expect(marks()).toEqual([]);
    expect(screen.getByText(NO_QUOTE)).toBeInTheDocument();
  });

  it.each([
    ['unsupported', { ...unsupportedCitation, quote: 'Entailment scores' }],
    ['not checked', { ...unverifiedCitation, quote: 'An introductory passage.' }],
  ])('AC1: %s with a quote: no highlight and no note', (_n, cit) => {
    renderWith([cit]);
    expect(marks()).toEqual([]);
    expect(screen.queryByText(NOT_FOUND)).toBeNull();
    expect(screen.queryByText(NO_QUOTE)).toBeNull();
  });

  it('AC1: a long passage shows an excerpt holding the mark; "Show full passage" toggles aria-expanded', async () => {
    const user = userEvent.setup();
    const content = `Opening sentence of the chunk. ${'Filler words for length. '.repeat(30)}We verify each claim at the end.`;
    renderWith([{ ...supportedCitation, content }]);
    const excerpt = document.querySelector('[data-excerpt]');
    expect(excerpt.textContent.startsWith('…')).toBe(true);
    expect(excerpt.querySelector('mark')).toHaveTextContent('verify each claim');
    expect(card(1).textContent).not.toContain('Opening sentence');
    const toggle = screen.getByRole('button', { name: 'Show full passage' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await user.click(toggle);
    expect(screen.getByRole('button', { name: 'Show less' })).toHaveAttribute('aria-expanded', 'true');
    expect(card(1).textContent).toContain(content.slice(0, 60));
    expect(marks()).toEqual(['Quote: verify each claim']);
  });

  it.each([
    [4, '#page=4', 'Open PDF at p. 4 ↗'],
    [0, '', 'Open PDF ↗'],
    [null, '', 'Open PDF ↗'],
    ['4', '', 'Open PDF ↗'],
    [undefined, '', 'Open PDF ↗'],
  ])('AC2: page %s → link suffix "%s", text "%s"', (page, suffix, text) => {
    renderWith([{ ...supportedCitation, page }]);
    const link = screen.getByRole('link', { name: /open pdf/i });
    expect(link).toHaveAttribute('href', `${supportedCitation.pdf_url}${suffix}`);
    expect(link).toHaveTextContent(text);
    expect(card(1).textContent.includes('p. ')).toBe(suffix !== '');
  });

  it('AC2: paper_version 0 → "arXiv:<id>" with no "v0"', () => {
    renderWith([{ ...supportedCitation, paper_version: 0, page: undefined }]);
    expect(screen.getByText('arXiv:2401.01234 · § Methods')).toBeInTheDocument();
  });

  it('AC6: an older citation (no status, page, quote, paper_version) renders without new notes', () => {
    const { marker, arxiv_id, title, chunk_id, section, content, pdf_url, score, supported } = supportedCitation;
    const legacy = { marker, arxiv_id, title, chunk_id, section, content, pdf_url, score, supported };
    renderWith([legacy]);
    expect(screen.getByText('arXiv:2401.01234 · § Methods')).toBeInTheDocument();
    expect(screen.getByText('Supported · 87.3%')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /open pdf/i })).toHaveAttribute('href', legacy.pdf_url);
    expect(card(1).textContent).toContain(legacy.content);
    expect(marks()).toEqual([]);
    expect(screen.queryByText(NO_QUOTE)).toBeNull();
  });

  describe('Export', () => {
    let clicks;
    afterEach(() => vi.restoreAllMocks());
    const exportVia = async (key, citations, answerKind) => {
      URL.createObjectURL = vi.fn(() => 'blob:x');
      URL.revokeObjectURL = vi.fn();
      clicks = [];
      vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function click() {
        clicks.push(this.download);
      });
      const onNotify = vi.fn();
      const user = userEvent.setup();
      renderWith(citations, { answerKind, onNotify });
      const trigger = screen.getByRole('button', { name: 'Export' });
      for (let i = 0; i < 20 && document.activeElement !== trigger; i += 1) await user.tab();
      expect(trigger).toHaveFocus();
      await user.keyboard(key);
      const item = await screen.findByRole('menuitem', { name: 'BibTeX (.bib)' });
      await waitFor(() => expect(item).toHaveFocus());
      await user.keyboard('{Enter}');
      expect(clicks).toHaveLength(1);
      expect(clicks[0]).toMatch(/^arxivlens-\d{8}-\d{6}\.bib$/);
      const blob = URL.createObjectURL.mock.calls[0][0];
      const text = await new Promise((done) => {
        const reader = new FileReader();
        reader.onload = () => done(reader.result);
        reader.readAsText(blob);
      });
      return { text, onNotify };
    };

    it.each([['{Enter}'], [' ']])('AC5: keyboard %j opens Export, Enter downloads every shown reference', async (key) => {
      const { text, onNotify } = await exportVia(key, quotedAnswer.citations, 'quoted');
      expect(text).toContain('@misc{arxiv240101234,');
      expect(text).toContain('@misc{arxiv240205678,');
      expect(onNotify).toHaveBeenCalledWith('Exported 2 references (.bib)');
    });

    it('AC5: a written answer exports only supported citations', async () => {
      const { text } = await exportVia('{Enter}', [...synthesizedSkips.citations, unsupportedCitation], 'synthesized');
      expect(text.match(/@misc\{/g)).toHaveLength(2);
      expect(text).not.toContain('arxiv240205678');
    });

    it('AC5: refusal mode has no Export trigger', () => {
      renderWith(refusalA.citations, { refusalReason: refusalA.refusal_reason });
      expect(screen.queryByRole('button', { name: /export/i })).toBeNull();
    });
  });
});

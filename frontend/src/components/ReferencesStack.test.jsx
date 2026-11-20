import { describe, it, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ReferencesStack from './ReferencesStack';
import {
  supportedCitation,
  unsupportedCitation,
  unverifiedCitation,
  refusalA,
  refusalB,
  refusalC,
} from '../test/fixtures';

const VERIFIED = /\bVerified\b/;

function renderStack(citations, onCopy = vi.fn(), refusalReason) {
  render(<ReferencesStack citations={citations} activeMarker={null} onCopy={onCopy} refusalReason={refusalReason} />);
  return onCopy;
}

const card = (marker) => screen.getByRole('article', { name: new RegExp(`Citation \\[${marker}\\]`) });

describe('ReferencesStack', () => {
  it('shows title, section and a one-click link to pdf_url for each citation', () => {
    renderStack([supportedCitation, unsupportedCitation]);
    for (const cit of [supportedCitation, unsupportedCitation]) {
      const c = within(card(cit.marker));
      expect(c.getByText(cit.title)).toBeInTheDocument();
      expect(c.getByText(new RegExp(`Section: ${cit.section}`))).toBeInTheDocument();
      expect(c.getByRole('link', { name: /open pdf/i })).toHaveAttribute('href', cit.pdf_url);
    }
  });

  it('falls back to the arXiv PDF URL when pdf_url is empty', () => {
    renderStack([{ ...supportedCitation, pdf_url: '' }]);
    expect(screen.getByRole('link', { name: /open pdf/i })).toHaveAttribute(
      'href',
      `https://arxiv.org/pdf/${supportedCitation.arxiv_id}.pdf`,
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

  it('Copy BibTeX passes an @article{arxiv_... entry to onCopy', async () => {
    const user = userEvent.setup();
    const onCopy = renderStack([supportedCitation]);
    await user.click(screen.getByRole('button', { name: /copy bibtex for \[1\]/i }));
    expect(onCopy).toHaveBeenCalledTimes(1);
    const [bibtex] = onCopy.mock.calls[0];
    expect(bibtex).toMatch(/^@article\{arxiv_240101234,/);
    expect(bibtex).toContain(`title = {${supportedCitation.title}}`);
    expect(bibtex).toContain(`eprint = {${supportedCitation.arxiv_id}}`);
  });

  it('shows an empty state when there are no citations', () => {
    renderStack([]);
    expect(screen.getByText(/No reference papers cited/)).toBeInTheDocument();
  });
});

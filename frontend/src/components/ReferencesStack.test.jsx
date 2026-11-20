import { describe, it, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ReferencesStack from './ReferencesStack';
import {
  supportedCitation,
  unsupportedCitation,
  unverifiedCitation,
} from '../test/fixtures';

const VERIFIED = /\bVerified\b/;

function renderStack(citations, onCopy = vi.fn()) {
  render(<ReferencesStack citations={citations} activeMarker={null} onCopy={onCopy} />);
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

  it('labels a supported citation as Verified with its score', () => {
    renderStack([supportedCitation]);
    expect(within(card(1)).getByText(/87\.3% Verified/)).toBeInTheDocument();
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

  it('shows an unchecked citation (verifier outage, score 0) as Unverified, not "0.0% Verified"', () => {
    renderStack([unverifiedCitation]);
    expect(screen.getByText(/Unverified · 0\.0%/)).toBeInTheDocument();
    expect(screen.queryByText(VERIFIED)).not.toBeInTheDocument();
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

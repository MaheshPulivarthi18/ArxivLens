import { describe, it, expect, vi } from 'vitest';
import { render, screen, within, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import SynthesisView from './SynthesisView';
import {
  quotedAnswer,
  synthesizedAnswer,
  mixedAnswer,
  earlyRefusal,
  refusalA,
  refusalB,
  refusalC,
  supportedCitation,
  quotedClaims,
  synthesizedSkips,
  malformedClaims,
} from '../test/fixtures';

const COPY_A = /A draft answer was written, but none of its statements was supported by the passages it cited, so it isn't shown\. The passages that were found are listed as evidence\./;
const COPY_B = /Citation checking is unavailable right now, so the answer couldn't be checked and isn't shown\. The passages that were found are listed as evidence\. Try again in a moment\./;
const COPY_C = /The passages found don't answer this question, so no answer is shown\. They are listed as evidence\. Try rephrasing the question or indexing more papers\./;

function renderView(response, extra = {}) {
  const props = {
    answer: response.answer,
    traceId: response.trace_id,
    citations: response.citations,
    answerKind: response.answer_kind,
    refusalReason: response.refusal_reason,
    writing: false,
    onCitationClick: vi.fn(),
    onCopy: vi.fn(),
    ...extra,
  };
  render(<SynthesisView {...props} />);
  return props;
}

describe('SynthesisView', () => {
  it('renders the answer text with each [n] marker as a button', () => {
    renderView(quotedAnswer);
    expect(screen.getByText(/Hybrid search finds the passages/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '[1]' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '[2]' })).toBeInTheDocument();
  });

  it('clicking a marker reports its citation number', async () => {
    const user = userEvent.setup();
    const props = renderView(quotedAnswer);
    await user.click(screen.getByRole('button', { name: '[2]' }));
    expect(props.onCitationClick).toHaveBeenCalledWith(2);
  });

  it('labels a quoted answer as quoted from the papers', () => {
    renderView(quotedAnswer);
    expect(screen.getByText(/Quoted from the papers/)).toBeInTheDocument();
    expect(screen.queryByText(/No answer shown|Evidence only/)).not.toBeInTheDocument();
  });

  it('labels a synthesized answer as a checked written answer', () => {
    renderView(synthesizedAnswer);
    expect(screen.getByText(/Written answer/)).toBeInTheDocument();
  });

  it.each([
    ['A', refusalA, COPY_A],
    ['B', refusalB, COPY_B],
    ['C', refusalC, COPY_C],
  ])('refusal %s shows its copy in a "No answer shown" status panel, not an error', (_r, response, copy) => {
    const props = renderView(response);
    const panel = screen.getByRole('status', { name: /no answer shown/i });
    expect(panel).toHaveTextContent(copy);
    // the server's sentence is not rendered as if it were the answer
    expect(screen.queryByText(response.answer)).not.toBeInTheDocument();
    expect(screen.getByText('Evidence only — no answer was shown')).toBeInTheDocument();
    expect(screen.queryByText(/Quoted from the papers|Written answer/)).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^\[\d+\]$/ })).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/verified/i);
    expect(props.onCopy).not.toHaveBeenCalled();
  });

  it('an unknown refusal reason shows the server text in the panel', () => {
    renderView({ ...refusalA, refusal_reason: 'some_future_reason' });
    expect(screen.getByRole('status', { name: /no answer shown/i })).toHaveTextContent(refusalA.answer);
  });

  it('refusal mode hides Copy Synthesis and Export Note', () => {
    renderView(refusalA);
    expect(screen.queryByRole('button', { name: /copy synthesis/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /export note/i })).not.toBeInTheDocument();
  });

  it('refusal panel is in the tab order through its passages link', async () => {
    const user = userEvent.setup();
    renderView(refusalA);
    const panel = screen.getByRole('status', { name: /no answer shown/i });
    const link = within(panel).getByRole('link', { name: 'Read the 2 retrieved passages' });
    for (let i = 0; i < 10 && !panel.contains(document.activeElement); i++) await user.tab();
    expect(document.activeElement).toBe(link);
  });

  it('a refusal with one passage links "Read the 1 retrieved passage"', () => {
    renderView({ ...refusalA, citations: [refusalA.citations[0]] });
    const panel = screen.getByRole('status', { name: /no answer shown/i });
    expect(within(panel).getByRole('link', { name: 'Read the 1 retrieved passage' })).toBeInTheDocument();
  });

  it('a refusal with zero citations still renders the panel, without a link', () => {
    renderView({ ...refusalA, citations: [] });
    const panel = screen.getByRole('status', { name: /no answer shown/i });
    expect(panel).toHaveTextContent(COPY_A);
    expect(within(panel).queryByRole('link')).not.toBeInTheDocument();
  });

  it('no "PostgreSQL pgvector Verified" in answer or refusal mode', () => {
    renderView(quotedAnswer);
    expect(screen.queryByText(/pgvector Verified/)).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/verified/i);
    cleanup();
    renderView(refusalA);
    expect(screen.queryByText(/pgvector Verified/)).not.toBeInTheDocument();
  });

  it('answer-mode footer summarises how many citations are supported', () => {
    renderView(mixedAnswer);
    expect(screen.getByText('1 of 3 citations supported')).toBeInTheDocument();
  });

  it('answer-mode footer with no citations reads "0 of 0 citations supported"', () => {
    renderView(earlyRefusal);
    expect(screen.getByText('0 of 0 citations supported')).toBeInTheDocument();
  });

  it('Export Note writes "## References" and each entry ends with its chip text', async () => {
    const user = userEvent.setup();
    const props = renderView(mixedAnswer);
    await user.click(screen.getByRole('button', { name: /export note/i }));
    expect(props.onCopy).toHaveBeenCalledTimes(1);
    const [md] = props.onCopy.mock.calls[0];
    expect(md).toContain('\n## References\n');
    expect(md).not.toMatch(/Verified/);
    const entries = md.split('\n').filter((l) => l.startsWith('- ['));
    expect(entries[0]).toMatch(/• Supported · 87\.3%$/);
    expect(entries[1]).toMatch(/^- \[2\] .*• Unsupported · 12\.0%$/);
    expect(entries[2]).toMatch(/^- \[3\] .*• Not checked$/);
  });

  it('announces when the synthesized answer is still being written', () => {
    renderView(quotedAnswer, { writing: true });
    expect(screen.getByRole('status')).toHaveTextContent(/Writing a summary/);
  });
});

// FE-4a (design docs/design/FE-4.md §2, test plan rows AC1, AC2, AC5).
const claimsList = () => screen.getByRole('list', { name: 'Claims in this answer' });
const claimItems = () => within(claimsList()).getAllByRole('listitem');
const chip = (li, text) => within(li).getByText(text).parentElement;
const NOT_SUPPORTED = /\bSupported\b|verified/i;

describe('SynthesisView: claims (FE-4a)', () => {
  it('quoted claims: each claim shows its own chip text, in order', () => {
    renderView(quotedClaims, { claims: quotedClaims.claims });
    const items = claimItems();
    expect(items).toHaveLength(4);
    items.forEach((li, i) => expect(li).toHaveTextContent(quotedClaims.claims[i].text));
    const chips = [chip(items[0], 'Supported'), chip(items[1], 'Unsupported'), chip(items[2], 'Not checked')];
    expect(chips[0]).toHaveClass('bg-supported');
    expect(chips[1]).toHaveClass('bg-unsupported');
    expect(chips[2]).toHaveClass('bg-unchecked');
    for (const c of chips) expect(c.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
    expect(within(items[0]).getByRole('button', { name: '[1]' })).toBeInTheDocument();
    expect(within(items[1]).getByRole('button', { name: '[2]' })).toBeInTheDocument();
    expect(within(items[2]).getByRole('button', { name: '[3]' })).toBeInTheDocument();
    // unverified claims never read as supported or verified
    expect(items[2].textContent).not.toMatch(NOT_SUPPORTED);
    expect(items[3].textContent).not.toMatch(NOT_SUPPORTED);
    // an uncited claim: its chip, no buttons
    expect(chip(items[3], 'Not checked')).toHaveClass('bg-unchecked');
    expect(within(items[3]).queryAllByRole('button')).toHaveLength(0);
    // the prose paragraph is replaced, the footer still counts entries
    expect(screen.queryByText(quotedClaims.answer)).toBeNull();
    expect(screen.getByText('1 of 3 citations supported')).toBeInTheDocument();
  });

  it('written answer with skipped markers: [1], [1], [3], no [2], no hidden count', () => {
    renderView(synthesizedSkips, { claims: synthesizedSkips.claims });
    expect(within(claimsList()).getAllByRole('button').map((b) => b.textContent)).toEqual(['[1]', '[1]', '[3]']);
    expect(screen.queryByRole('button', { name: '[2]' })).toBeNull();
    expect(within(claimsList()).getAllByText('Supported')).toHaveLength(2);
    expect(document.body.textContent).not.toMatch(/hidden/i);
    expect(screen.getByText('3 of 3 citations supported')).toBeInTheDocument();
  });

  it('marker has aria-controls ref-<n> and reports (marker, pos) on click, Enter and Space', async () => {
    const user = userEvent.setup();
    const props = renderView(synthesizedSkips, { claims: synthesizedSkips.claims });
    const second = claimItems()[1];
    const m3 = within(second).getByRole('button', { name: '[3]' });
    expect(m3).toHaveAttribute('type', 'button');
    expect(m3).toHaveAttribute('aria-controls', 'ref-3');
    await user.click(m3);
    expect(props.onCitationClick).toHaveBeenLastCalledWith(3, 2);
    await user.keyboard('{Enter}');
    expect(props.onCitationClick).toHaveBeenLastCalledWith(3, 2);
    await user.keyboard(' ');
    expect(props.onCitationClick).toHaveBeenLastCalledWith(3, 2);
    expect(props.onCitationClick).toHaveBeenCalledTimes(3);
    // the repeated [1] reports its own entry, not the first one
    await user.click(within(second).getByRole('button', { name: '[1]' }));
    expect(props.onCitationClick).toHaveBeenLastCalledWith(1, 1);
    await user.click(within(claimItems()[0]).getByRole('button', { name: '[1]' }));
    expect(props.onCitationClick).toHaveBeenLastCalledWith(1, 0);
  });

  it('a claim marker with no citation renders as text, not a button', () => {
    renderView(
      { ...synthesizedAnswer, citations: [supportedCitation] },
      { claims: [{ text: 'Odd claim.', status: 'supported', markers: [1, 9] }] },
    );
    const [li] = claimItems();
    expect(within(li).getAllByRole('button').map((b) => b.textContent)).toEqual(['[1]']);
    expect(li).toHaveTextContent('[9]');
  });

  it.each([['absent', undefined], ['empty', []], ...malformedClaims.map((c, i) => [`malformed ${i + 1}`, c])])(
    'claims %s → prose with [n] buttons, no claims list',
    (_name, claims) => {
      renderView(quotedAnswer, { claims });
      expect(screen.queryByRole('list', { name: 'Claims in this answer' })).toBeNull();
      expect(screen.getByText(/Hybrid search finds the passages/)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: '[1]' })).toHaveAttribute('aria-controls', 'ref-1');
      expect(screen.getByRole('button', { name: '[2]' })).toHaveAttribute('aria-controls', 'ref-2');
    },
  );

  it('claims: [] renders the same DOM as no claims', () => {
    renderView(quotedAnswer);
    const without = document.body.innerHTML;
    cleanup();
    renderView(quotedAnswer, { claims: [] });
    expect(document.body.innerHTML).toBe(without);
  });

  it('fallback: a marker with no citation keeps its hover hint and controls nothing', () => {
    renderView({ ...quotedAnswer, citations: [supportedCitation] });
    const m2 = screen.getByRole('button', { name: '[2]' });
    expect(m2).toHaveAttribute('title', 'Click to inspect Evidence [2]');
    expect(m2).not.toHaveAttribute('aria-controls');
    expect(screen.getByRole('button', { name: '[1]' })).not.toHaveAttribute('title');
  });

  it('focusing a marker previews its passage in a tooltip', async () => {
    const user = userEvent.setup();
    renderView(synthesizedSkips, { claims: synthesizedSkips.claims });
    const m3 = within(claimItems()[1]).getByRole('button', { name: '[3]' });
    for (let i = 0; i < 10 && document.activeElement !== m3; i += 1) await user.tab();
    expect(m3).toHaveFocus();
    const tip = await screen.findByRole('tooltip');
    expect(tip).toHaveTextContent('[3] Claim-Level Checking in Practice · § Evaluation · Supported · 91.2%');
    expect(m3).toHaveAttribute('aria-describedby', tip.id);
  });
});

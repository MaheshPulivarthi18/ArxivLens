import { describe, it, expect, vi } from 'vitest';
import { render, screen, within, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import SynthesisView from './SynthesisView';
import { quotedAnswer, synthesizedAnswer, mixedAnswer, refusalA, refusalB, refusalC } from '../test/fixtures';

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

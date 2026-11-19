import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import SynthesisView from './SynthesisView';
import { quotedAnswer, synthesizedAnswer, refusalAnswer } from '../test/fixtures';

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
    expect(screen.queryByText(/No verified answer/)).not.toBeInTheDocument();
  });

  it('labels a synthesized answer as a checked written answer', () => {
    renderView(synthesizedAnswer);
    expect(screen.getByText(/Written answer/)).toBeInTheDocument();
  });

  it('renders a refusal as a normal state with its reason, not an error', () => {
    const props = renderView(refusalAnswer);
    expect(screen.getByText(refusalAnswer.answer)).toBeInTheDocument();
    expect(screen.getByText(/No verified answer/)).toBeInTheDocument();
    expect(screen.queryByText(/Quoted from the papers|Written answer/)).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(props.onCopy).not.toHaveBeenCalled();
  });

  it('announces when the synthesized answer is still being written', () => {
    renderView(quotedAnswer, { writing: true });
    expect(screen.getByRole('status')).toHaveTextContent(/Writing a summary/);
  });
});

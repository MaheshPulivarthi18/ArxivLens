import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Badge } from './badge';

// FE-0i: Badge is inline content, so it must render phrasing content (a <span>).
// A <div> inside FE-3b's status-chip <button> or a <p> is invalid HTML.
describe('Badge', () => {
  it('renders a span holding its children with the default variant classes', () => {
    render(<Badge>x</Badge>);
    const badge = screen.getByText('x');
    expect(badge.tagName).toBe('SPAN');
    expect(badge).toHaveClass('inline-flex', 'rounded-full', 'bg-primary');
  });

  it('merges a caller className, applies a named variant and forwards extra props', () => {
    render(
      <Badge variant="supported" className="ml-2" title="Supported claim" data-testid="chip">
        ok
      </Badge>
    );
    const badge = screen.getByTestId('chip');
    expect(badge.tagName).toBe('SPAN');
    expect(badge).toHaveTextContent('ok');
    expect(badge).toHaveAttribute('title', 'Supported claim');
    expect(badge).toHaveClass('ml-2', 'inline-flex', 'bg-supported', 'text-supported-foreground');
    expect(badge).not.toHaveClass('bg-primary');
  });

  // FE-0j: caller classes must win over conflicting defaults (ReferencesStack relies on
  // className="text-xs px-2 py-0" replacing the default px-2.5 py-0.5).
  it('lets a caller className override conflicting default padding', () => {
    render(<Badge className="px-2 py-0">x</Badge>);
    const badge = screen.getByText('x');
    expect(badge).toHaveClass('px-2', 'py-0');
    expect(badge).not.toHaveClass('px-2.5');
    expect(badge).not.toHaveClass('py-0.5');
  });

  it('is valid phrasing content inside a button (no div descendant)', () => {
    render(
      <button type="button">
        <Badge>x</Badge>
      </button>
    );
    const button = screen.getByRole('button', { name: 'x' });
    expect(button.querySelector('div')).toBeNull();
    expect(button.querySelector('span')).toHaveTextContent('x');
  });
});

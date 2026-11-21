import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Button } from './button';

// FE-0f AC1: the deprecated `clinical` variant draws with theme tokens, not a palette gradient.
describe('Button variant="clinical"', () => {
  it('uses the primary tokens and no palette colour or text-white', () => {
    render(<Button variant="clinical">Search</Button>);
    const cls = screen.getByRole('button', { name: 'Search' }).className;

    expect(cls).toMatch(/(^|\s)bg-primary(\s|$)/);
    expect(cls).toMatch(/(^|\s)text-primary-foreground(\s|$)/);
    expect(cls).toMatch(/(^|\s)hover:bg-primary\/90(\s|$)/);
    expect(cls).toMatch(/(^|\s)font-semibold(\s|$)/);
    for (const banned of ['emerald', 'teal', 'from-', 'to-', 'text-white']) {
      expect(cls).not.toContain(banned);
    }
  });

  it('keeps the base focus ring', () => {
    render(<Button variant="clinical">Go</Button>);
    const cls = screen.getByRole('button', { name: 'Go' }).className;
    expect(cls).toContain('focus-visible:ring-1');
    expect(cls).toContain('focus-visible:ring-ring');
  });

  it('matches the default variant colours, plus font-semibold', () => {
    render(
      <>
        <Button variant="clinical">A</Button>
        <Button>B</Button>
      </>,
    );
    const clinical = screen.getByRole('button', { name: 'A' }).className;
    const def = screen.getByRole('button', { name: 'B' }).className;
    const tokens = (cls) => cls.split(/\s+/).filter((c) => c && !c.startsWith('font-')).sort();
    expect(tokens(clinical)).toEqual(tokens(def));
  });
});

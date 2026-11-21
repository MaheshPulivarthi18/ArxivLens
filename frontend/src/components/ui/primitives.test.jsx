import { createRef } from 'react';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { toast } from 'sonner';
import { Tooltip, TooltipContent, TooltipTrigger } from './tooltip';
import { Popover, PopoverContent, PopoverTrigger } from './popover';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from './collapsible';
import * as ScrollAreaPrimitive from '@radix-ui/react-scroll-area';
import { ScrollArea, ScrollBar } from './scroll-area';
import { Separator } from './separator';
import { Toaster } from './sonner';

// Pins the FE-0b primitives' behaviour (QA's smoke facts, agents/reviews/FE-0b). jsdom computes no Tailwind CSS,
// so these assert class names and inline styles, not colours. Stubs live here, not in src/test/setup.js.
beforeAll(() => {
  if (typeof globalThis.ResizeObserver === 'undefined') {
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
  }
  if (typeof globalThis.DOMRect === 'undefined') {
    vi.stubGlobal('DOMRect', class {
      constructor(x = 0, y = 0, width = 0, height = 0) {
        Object.assign(this, { x, y, width, height, top: y, left: x, right: x + width, bottom: y + height });
      }
      static fromRect(r = {}) { return new this(r.x, r.y, r.width, r.height); }
    });
  }
});

afterAll(() => {
  vi.unstubAllGlobals();
});

// sonner keeps toasts in module state: dismiss them so one test's toasts don't replay into the next Toaster.
afterEach(() => {
  act(() => {
    toast.dismiss();
  });
});

const FLOATING = ['font-sans', 'bg-popover', 'text-popover-foreground'];

function expectPortalled(el, container) {
  expect(el).toBeInstanceOf(HTMLElement);
  expect(document.body.contains(el)).toBe(true);
  expect(container.contains(el)).toBe(false);
}

describe('Tooltip / Popover (AC1)', () => {
  it('TooltipContent renders in a portal with the popover tokens, forwards its ref and merges className', async () => {
    const ref = createRef();
    const { container } = render(
      <Tooltip open>
        <TooltipTrigger>Info</TooltipTrigger>
        <TooltipContent ref={ref} className="qa-tip">Tip text</TooltipContent>
      </Tooltip>,
    );
    await waitFor(() => expect(ref.current).toBeInstanceOf(HTMLElement));
    expectPortalled(ref.current, container);
    expect(ref.current).toHaveClass(...FLOATING, 'qa-tip');
    expect(ref.current).toHaveTextContent('Tip text');
  });

  it('PopoverContent renders in a portal with the popover tokens, forwards its ref and merges className', async () => {
    const ref = createRef();
    const { container } = render(
      <Popover defaultOpen>
        <PopoverTrigger>Open</PopoverTrigger>
        <PopoverContent ref={ref} className="qa-pop">Popover body</PopoverContent>
      </Popover>,
    );
    const body = await screen.findByText('Popover body');
    expect(ref.current).toBe(body);
    expectPortalled(body, container);
    expect(body).toHaveClass(...FLOATING, 'w-80', 'qa-pop');
  });
});

describe('Collapsible (AC2)', () => {
  it('defaultOpen shows the content with data-state="open"', () => {
    render(
      <Collapsible defaultOpen>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent>Hidden detail</CollapsibleContent>
      </Collapsible>,
    );
    expect(screen.getByText('Hidden detail')).toHaveAttribute('data-state', 'open');
  });

  it('closed keeps the content out of the document until the trigger is clicked', async () => {
    const user = userEvent.setup();
    render(
      <Collapsible>
        <CollapsibleTrigger>Details</CollapsibleTrigger>
        <CollapsibleContent>Hidden detail</CollapsibleContent>
      </Collapsible>,
    );
    expect(screen.queryByText('Hidden detail')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Details' }));
    expect(await screen.findByText('Hidden detail')).toHaveAttribute('data-state', 'open');
  });
});

describe('ScrollArea (AC3)', () => {
  it('viewport has an inset focus ring (no ring-offset), Root ref forwards, children render inside the viewport', () => {
    const ref = createRef();
    const { container } = render(
      <ScrollArea ref={ref} className="h-24">
        <p>Scrolled child</p>
      </ScrollArea>,
    );
    const viewport = container.querySelector('[data-radix-scroll-area-viewport]');
    expect(viewport).not.toBeNull();
    expect(viewport).toHaveClass('focus-visible:ring-inset', 'focus-visible:ring-ring');
    expect(viewport.className).not.toMatch(/ring-offset/);
    expect(ref.current).toBeInstanceOf(HTMLElement);
    expect(ref.current).toHaveClass('relative', 'overflow-hidden', 'h-24');
    expect(ref.current.contains(viewport)).toBe(true);
    expect(viewport).toContainElement(screen.getByText('Scrolled child'));
  });
});

describe('Separator (AC4)', () => {
  it('is decorative and horizontal by default', () => {
    const ref = createRef();
    const { container } = render(<Separator ref={ref} />);
    const sep = container.firstChild;
    expect(ref.current).toBe(sep);
    expect(sep).toHaveAttribute('role', 'none');
    expect(sep).toHaveClass('h-px', 'w-full');
  });

  it('decorative={false} exposes role="separator"', () => {
    render(<Separator decorative={false} />);
    expect(screen.getByRole('separator')).toHaveClass('h-px', 'w-full');
  });

  it('orientation="vertical" switches the size classes and sets aria-orientation when not decorative', () => {
    const { container } = render(<Separator orientation="vertical" />);
    expect(container.firstChild).toHaveClass('h-full', 'w-px');
    expect(container.firstChild).not.toHaveClass('h-px');

    render(<Separator orientation="vertical" decorative={false} />);
    const sep = screen.getByRole('separator');
    expect(sep).toHaveClass('h-full', 'w-px');
    expect(sep).toHaveAttribute('aria-orientation', 'vertical');
  });
});

describe('Toaster (AC5)', () => {
  // sonner renders the toasts asynchronously; its list (<ol data-sonner-toaster>, inside the ref'd <section>)
  // only exists once a toast is shown, and that list is where the `style` prop lands.
  async function showToast(title, description) {
    act(() => {
      toast(title, { description });
    });
    const desc = await screen.findByText(description);
    return { desc, item: desc.closest('[data-sonner-toast]') };
  }

  it('points sonner variables at the tokens and lets a caller override one of them', async () => {
    const ref = createRef();
    render(<Toaster ref={ref} style={{ '--normal-bg': 'red' }} />);
    const { item } = await showToast('t-vars', 'd-vars');
    expect(ref.current.tagName).toBe('SECTION');
    const list = ref.current.querySelector('[data-sonner-toaster]');
    expect(list).toContainElement(item);
    expect(list.style.getPropertyValue('--normal-bg')).toBe('red');
    expect(list.style.getPropertyValue('--normal-text')).toBe('hsl(var(--popover-foreground))');
    expect(list.style.getPropertyValue('--normal-border')).toBe('hsl(var(--border))');
    expect(list.style.getPropertyValue('--border-radius')).toBe('var(--radius)');
  });

  it('defaults --normal-bg to the popover token', async () => {
    const ref = createRef();
    render(<Toaster ref={ref} />);
    await showToast('t-default', 'd-default');
    const list = ref.current.querySelector('[data-sonner-toaster]');
    expect(list.style.getPropertyValue('--normal-bg')).toBe('hsl(var(--popover))');
    expect(list).toHaveClass('toaster');
  });

  it('gives the description the muted token class', async () => {
    render(<Toaster />);
    const { desc } = await showToast('t-muted', 'd-muted');
    expect(desc).toHaveAttribute('data-description');
    expect(desc).toHaveClass('!text-muted-foreground');
  });

  it('merges caller toastOptions.classNames with the muted description class (FE-0b F3)', async () => {
    render(<Toaster toastOptions={{ classNames: { title: 'x-title', description: 'x-desc' } }} />);
    const { desc, item } = await showToast('t-merge', 'd-merge');
    expect(item.querySelector('[data-title]')).toHaveClass('x-title');
    expect(desc).toHaveClass('!text-muted-foreground', 'x-desc');
  });
});

// FE-0g: pins the behaviours FE-0d's tests left open (QA FE-0d "Missing tests" M35–M39).
describe('ScrollBar orientation and className (FE-0g AC1)', () => {
  // type="always" mounts the scrollbar without overflow (the default "hover" type hides it in jsdom).
  function renderBar(barProps) {
    render(
      <ScrollAreaPrimitive.Root type="always">
        <ScrollAreaPrimitive.Viewport>
          <p>Bar content</p>
        </ScrollAreaPrimitive.Viewport>
        <ScrollBar data-testid="bar" {...barProps} />
      </ScrollAreaPrimitive.Root>,
    );
    return screen.getByTestId('bar');
  }

  it('horizontal gets the horizontal size/border classes plus the caller className', () => {
    const bar = renderBar({ orientation: 'horizontal', className: 'x-bar' });
    expect(bar).toHaveAttribute('data-orientation', 'horizontal');
    expect(bar).toHaveClass('h-2.5', 'flex-col', 'border-t', 'x-bar');
    expect(bar).not.toHaveClass('w-2.5');
    expect(bar).not.toHaveClass('border-l');
  });

  it('defaults to vertical with the vertical size/border classes', () => {
    const bar = renderBar({});
    expect(bar).toHaveAttribute('data-orientation', 'vertical');
    expect(bar).toHaveClass('h-full', 'w-2.5', 'border-l');
    expect(bar).not.toHaveClass('flex-col');
  });
});

describe('Toaster position (FE-0g AC2)', () => {
  it('places the toast list bottom-right', async () => {
    render(<Toaster />);
    act(() => {
      toast('t-position', { description: 'd-position' });
    });
    const desc = await screen.findByText('d-position');
    const list = desc.closest('[data-sonner-toaster]');
    expect(list).not.toBeNull();
    expect(list.tagName).toBe('OL');
    expect(list).toHaveAttribute('data-y-position', 'bottom');
    expect(list).toHaveAttribute('data-x-position', 'right');
  });
});

describe('Separator className (FE-0g AC3)', () => {
  it('merges the caller className with the base and size classes', () => {
    const { container } = render(<Separator className="x-sep" />);
    expect(container.firstChild).toHaveClass('x-sep', 'shrink-0', 'bg-border', 'h-px', 'w-full');
  });
});

describe('Tooltip default delay (FE-0g AC4)', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  // Synchronous on purpose: userEvent's internal awaits hang under fake timers (CI r1 timed out at 5000 ms).
  // fireEvent is act()-wrapped and awaits nothing; Radix's trigger opens on pointermove (not pointerenter).
  it('opens on hover after the 300 ms default delay, not before', () => {
    vi.useFakeTimers();
    const ref = createRef();
    render(
      <Tooltip>
        <TooltipTrigger>Hover me</TooltipTrigger>
        <TooltipContent ref={ref}>Delayed tip</TooltipContent>
      </Tooltip>,
    );
    fireEvent.pointerMove(screen.getByRole('button', { name: 'Hover me' }), { pointerType: 'mouse' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(ref.current).toBeNull();
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(100);
    });
    expect(ref.current).toBeInstanceOf(HTMLElement);
    expect(ref.current).toHaveTextContent('Delayed tip');
    expect(screen.getByRole('tooltip')).toHaveTextContent('Delayed tip');
  });
});

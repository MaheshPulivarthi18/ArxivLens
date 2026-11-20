import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import ThemeToggle from './ThemeToggle';

const KEY = 'arxivlens-theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';
const root = document.documentElement;

// matchMedia with a controllable OS scheme: os.change(dark) fires a `change` event like the browser does.
let os;
function stubMatchMedia(dark) {
  const listeners = new Set();
  os = {
    dark,
    listeners,
    change(next) {
      os.dark = next;
      act(() => {
        for (const fn of [...listeners]) fn({ matches: next });
      });
    },
  };
  window.matchMedia = vi.fn((query) => ({
    get matches() { return query === DARK_QUERY && os.dark; },
    addEventListener: (type, fn) => { if (type === 'change') listeners.add(fn); },
    removeEventListener: (type, fn) => { if (type === 'change') listeners.delete(fn); },
  }));
}

const trigger = (name) => screen.getByRole('button', { name });

beforeEach(() => {
  localStorage.clear();
  root.className = '';
  root.style.colorScheme = '';
});

afterEach(() => {
  delete window.matchMedia;
  localStorage.clear();
});

describe('ThemeToggle', () => {
  it('in System mode follows an OS change dark -> light without user action', () => {
    localStorage.setItem(KEY, 'system');
    stubMatchMedia(true);
    render(<ThemeToggle />);
    expect(trigger('Theme: System (dark)')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(true);

    os.change(false);
    expect(trigger('Theme: System (light)')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(false);
    expect(root.style.colorScheme).toBe('light');

    os.change(true);
    expect(trigger('Theme: System (dark)')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(true);
  });

  it('follows a theme change made in another tab', () => {
    localStorage.setItem(KEY, 'dark');
    stubMatchMedia(true);
    render(<ThemeToggle />);
    expect(trigger('Theme: Dark')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(true);

    localStorage.setItem(KEY, 'light');
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: KEY, newValue: 'light' }));
    });
    expect(trigger('Theme: Light')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(false);
  });

  it('ignores a storage event for another key', () => {
    localStorage.setItem(KEY, 'dark');
    stubMatchMedia(true);
    render(<ThemeToggle />);

    // The stored value changes underneath, but the event names another key, so nothing is re-read.
    localStorage.setItem(KEY, 'light');
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: 'some-other-key', newValue: 'light' }));
    });
    expect(trigger('Theme: Dark')).toBeInTheDocument();
    expect(root.classList.contains('dark')).toBe(true);
  });

  it('removes its OS listener on unmount', () => {
    localStorage.setItem(KEY, 'system');
    stubMatchMedia(true);
    const { unmount } = render(<ThemeToggle />);
    expect(os.listeners.size).toBe(1);
    unmount();
    expect(os.listeners.size).toBe(0);
  });
});

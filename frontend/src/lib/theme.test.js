import fs from 'node:fs';
import vm from 'node:vm';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_THEME, THEME_STORAGE_KEY, THEMES, applyTheme, getStoredTheme, resolveTheme, setStoredTheme, watchSystemTheme,
} from './theme';

// The pre-paint script in index.html duplicates theme.js (FE-0). These tests compare the two files, so a change to
// one without the other (e.g. FE-3 flipping DEFAULT_THEME) fails here. Never hard-code the default below.
const html = fs.readFileSync(new URL('../../index.html', import.meta.url), 'utf8');
const inlineScripts = [...html.matchAll(/<script(?![^>]*\bsrc=)([^>]*)>([\s\S]*?)<\/script>/g)]
  .filter((m) => !/type=["']module["']/.test(m[1]))
  .map((m) => m[2]);
const prePaint = inlineScripts.find((src) => src.includes('DEFAULT_THEME'));

const DARK_QUERY = '(prefers-color-scheme: dark)';
const root = document.documentElement;

// Controllable matchMedia: `os.dark` is the OS scheme, `os.change(dark)` fires a change event.
function stubMatchMedia() {
  const listeners = new Set();
  const os = {
    dark: true,
    listeners,
    change(dark) {
      os.dark = dark;
      for (const fn of [...listeners]) fn({ matches: dark });
    },
  };
  window.matchMedia = vi.fn((query) => ({
    get matches() { return query === DARK_QUERY && os.dark; },
    addEventListener: (type, fn) => { if (type === 'change') listeners.add(fn); },
    removeEventListener: (type, fn) => { if (type === 'change') listeners.delete(fn); },
  }));
  return os;
}

beforeEach(() => {
  localStorage.clear();
  root.className = '';
  root.style.colorScheme = '';
});

afterEach(() => {
  vi.restoreAllMocks();
  delete window.matchMedia;
  localStorage.clear();
});

describe('index.html pre-paint script vs theme.js (drift guard)', () => {
  it('has an inline classic pre-paint script', () => {
    expect(prePaint).toBeDefined();
  });

  it('uses the same DEFAULT_THEME as theme.js', () => {
    const literal = prePaint.match(/\bDEFAULT_THEME\s*=\s*(['"])([^'"]*)\1/);
    expect(literal).not.toBeNull();
    expect(literal[2]).toBe(DEFAULT_THEME);
  });

  it('reads the same storage key as theme.js', () => {
    const keys = [...prePaint.matchAll(/localStorage\.getItem\(\s*(['"])([^'"]*)\1\s*\)/g)].map((m) => m[2]);
    expect(keys.length).toBeGreaterThan(0);
    for (const key of keys) expect(key).toBe(THEME_STORAGE_KEY);
  });
});

describe('pre-paint script agrees with applyTheme(getStoredTheme())', () => {
  const STORED = { none: undefined, light: 'light', dark: 'dark', system: 'system', invalid: 'purple', empty: '', throws: 'THROW' };
  const rows = Object.entries(STORED).flatMap(([name, value]) => [true, false].map((osDark) => [name, value, osDark]));

  const snapshot = () => ({ dark: root.classList.contains('dark'), colorScheme: root.style.colorScheme });

  // Both implementations run from the same starting class so a missing "remove .dark" shows up too.
  function runBoth(value, osDark, startDark) {
    stubMatchMedia().dark = osDark;
    if (value === 'THROW') {
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('SecurityError'); });
    } else if (value !== undefined) {
      localStorage.setItem(THEME_STORAGE_KEY, value);
    }
    root.classList.toggle('dark', startDark);
    root.style.colorScheme = '';
    vm.runInNewContext(prePaint, { localStorage, window: { matchMedia: window.matchMedia }, document });
    const fromScript = snapshot();
    root.classList.toggle('dark', startDark);
    root.style.colorScheme = '';
    applyTheme(getStoredTheme());
    return { fromScript, fromModule: snapshot() };
  }

  it.each(rows)('stored %s (%j), OS dark=%s', (name, value, osDark) => {
    for (const startDark of [false, true]) {
      const { fromScript, fromModule } = runBoth(value, osDark, startDark);
      expect(fromScript).toEqual(fromModule);
      expect(fromScript.colorScheme).toBe(fromScript.dark ? 'dark' : 'light');
      vi.restoreAllMocks();
      localStorage.clear();
    }
  });

  it('covers all 14 combinations', () => {
    expect(rows).toHaveLength(14);
  });
});

describe('theme.js', () => {
  it('getStoredTheme returns each stored valid theme', () => {
    for (const theme of THEMES) {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
      expect(getStoredTheme()).toBe(theme);
    }
  });

  it.each([['missing', null], ['invalid', 'purple'], ['empty', ''], ['wrong case', 'DARK']])(
    'getStoredTheme returns DEFAULT_THEME when the value is %s',
    (_, value) => {
      if (value !== null) localStorage.setItem(THEME_STORAGE_KEY, value);
      expect(getStoredTheme()).toBe(DEFAULT_THEME);
    },
  );

  it('getStoredTheme returns DEFAULT_THEME when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('SecurityError'); });
    expect(getStoredTheme()).toBe(DEFAULT_THEME);
  });

  it('setStoredTheme persists a valid theme under the storage key', () => {
    setStoredTheme('light');
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('light');
  });

  it('setStoredTheme ignores an invalid value', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'system');
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    setStoredTheme('purple');
    expect(setItem).not.toHaveBeenCalled();
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('system');
  });

  it('setStoredTheme swallows a throwing setItem', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('QuotaExceededError'); });
    expect(() => setStoredTheme('dark')).not.toThrow();
  });

  it.each([
    ['light', true, 'light'], ['light', false, 'light'],
    ['dark', true, 'dark'], ['dark', false, 'dark'],
    ['system', true, 'dark'], ['system', false, 'light'],
  ])('resolveTheme(%s, prefersDark=%s) is %s', (pref, prefersDark, expected) => {
    expect(resolveTheme(pref, prefersDark)).toBe(expected);
  });

  it('watchSystemTheme calls back on change and its cleanup unsubscribes', () => {
    const os = stubMatchMedia();
    const onChange = vi.fn();
    const unsubscribe = watchSystemTheme(onChange);
    os.change(false);
    expect(onChange).toHaveBeenLastCalledWith(false);
    os.change(true);
    expect(onChange).toHaveBeenLastCalledWith(true);
    expect(onChange).toHaveBeenCalledTimes(2);
    unsubscribe();
    expect(os.listeners.size).toBe(0);
    os.change(false);
    expect(onChange).toHaveBeenCalledTimes(2);
  });

  it('watchSystemTheme falls back to addListener/removeListener', () => {
    const listeners = new Set();
    window.matchMedia = vi.fn(() => ({
      matches: false,
      addListener: (fn) => listeners.add(fn),
      removeListener: (fn) => listeners.delete(fn),
    }));
    const onChange = vi.fn();
    const unsubscribe = watchSystemTheme(onChange);
    expect(listeners.size).toBe(1);
    for (const fn of [...listeners]) fn({ matches: true });
    expect(onChange).toHaveBeenCalledWith(true);
    unsubscribe();
    expect(listeners.size).toBe(0);
  });
});

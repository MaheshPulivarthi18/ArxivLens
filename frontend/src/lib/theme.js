// Theme preference: "light" | "dark" | "system", stored in localStorage.
// Plain ESM with no imports so it runs in bare Node (tests stub window/document).
// The inline pre-paint script in index.html duplicates THEME_STORAGE_KEY and DEFAULT_THEME: keep them in sync.

export const THEME_STORAGE_KEY = "arxivlens-theme";
export const THEMES = ["light", "dark", "system"];
// First visit follows the OS (every screen is on the theme tokens since FE-0e and FE-3b).
// Same value as DEFAULT_THEME in the pre-paint script in index.html.
export const DEFAULT_THEME = "system";

const DARK_QUERY = "(prefers-color-scheme: dark)";

const isTheme = (value) => THEMES.includes(value);

export function getStoredTheme() {
  try {
    const value = globalThis.localStorage?.getItem(THEME_STORAGE_KEY);
    return isTheme(value) ? value : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

export function setStoredTheme(pref) {
  if (!isTheme(pref)) return;
  try {
    globalThis.localStorage?.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    // Storage unavailable (e.g. private mode): the choice lasts for this session only.
  }
}

export function systemPrefersDark() {
  const mm = globalThis.window?.matchMedia;
  return typeof mm === "function" ? Boolean(mm.call(globalThis.window, DARK_QUERY).matches) : false;
}

export function resolveTheme(pref, prefersDark) {
  if (pref === "light" || pref === "dark") return pref;
  return prefersDark ? "dark" : "light";
}

export function applyTheme(pref) {
  const resolved = resolveTheme(isTheme(pref) ? pref : DEFAULT_THEME, systemPrefersDark());
  const root = globalThis.document?.documentElement;
  if (root) {
    root.classList.toggle("dark", resolved === "dark");
    root.style.colorScheme = resolved;
  }
  return resolved;
}

export function watchSystemTheme(onChange) {
  const mm = globalThis.window?.matchMedia;
  if (typeof mm !== "function") return () => {};
  const query = mm.call(globalThis.window, DARK_QUERY);
  const listener = (event) => onChange(Boolean(event.matches));
  if (query.addEventListener) {
    query.addEventListener("change", listener);
    return () => query.removeEventListener("change", listener);
  }
  query.addListener?.(listener); // Safari < 14
  return () => query.removeListener?.(listener);
}

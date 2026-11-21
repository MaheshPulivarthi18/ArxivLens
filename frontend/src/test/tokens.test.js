// FE-0e: guard against hard-coded dark-palette colour classes coming back into
// screens that are on the theme tokens (src/index.css, tailwind.config.js).
// FE-3b adds the ask-flow files and widens the pattern (more utilities, every default palette, white/black).
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Paths relative to frontend/src/.
export const GUARDED_FILES = [
  'components/IngestModal.jsx',
  'components/InfrastructureModal.jsx',
  'components/PapersLibrary.jsx',
  'components/Toast.jsx',
  'components/SynthesisView.jsx',
  'components/ReferencesStack.jsx',
  'App.jsx',
  'components/HeroSearch.jsx',
]

const UTILITIES = 'text|bg|border|ring-offset|ring|outline|divide|decoration|fill|stroke|placeholder|caret|from|via|to'
const PALETTES =
  'slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose'

// A utility (any variant prefix, optional side such as border-t-) followed by a default palette shade, or by
// white/black. `shadow` only counts with a palette shade: a shadow-black/80 tint is not text or a surface.
// The lookbehind stops a token like `text-primary-foreground` or `bg-supported` from matching mid-word.
export const PALETTE_CLASS = new RegExp(
  `(?<![\\w-])(?:[a-z0-9-]+:)*(?:(?:${UTILITIES})-(?:[trblxyse]-)?(?:(?:${PALETTES})-\\d|(?:white|black)(?![\\w-]))` +
    `|shadow-(?:${PALETTES})-\\d)`,
)

// Resolve with node path. Vite rewrites a URL built from a template literal and
// import.meta.url into an import.meta.glob lookup, which breaks under Vitest.
const SRC_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (rel) => readFileSync(path.join(SRC_DIR, rel), 'utf8')

// Global copy so every hit on a line is reported, not just the first.
const PALETTE_CLASS_ALL = new RegExp(PALETTE_CLASS.source, 'g')

describe('palette-class guard', () => {
  it('pattern catches palette classes, with and without variants', () => {
    for (const cls of [
      'text-emerald-400', 'hover:bg-emerald-500/10', 'focus:border-red-500', 'dark:md:ring-slate-700', 'shadow-cyan-900',
      // FE-3b: the new utilities, a non-emerald palette, white/black, and a border side
      'focus-visible:ring-offset-slate-950', 'outline-emerald-500', 'divide-gray-800', 'decoration-sky-400',
      'fill-rose-500', 'stroke-indigo-400', 'placeholder:text-zinc-500', 'placeholder-stone-400', 'caret-violet-500',
      'bg-zinc-900', 'text-sky-400', 'from-fuchsia-600', 'text-white', 'border-white/30', 'border-t-white',
      'hover:bg-black/40', 'ring-offset-white', 'shadow-neutral-900/50',
    ]) {
      expect(cls).toMatch(PALETTE_CLASS)
    }
    for (const cls of [
      'text-primary', 'bg-destructive/10', 'border-border', 'text-supported-foreground', 'shadow-black/80',
      'border-primary-foreground/30', 'border-t-primary-foreground', 'ring-ring/30', 'ring-offset-card', 'bg-accent',
      'whitespace-pre-wrap', 'text-whitesmoke', 'shadow-white/10',
    ]) {
      expect(cls).not.toMatch(PALETTE_CLASS)
    }
  })

  it.each(GUARDED_FILES)('%s uses theme tokens, not palette classes', (rel) => {
    const lines = read(rel).split('\n')
    expect(lines.length).toBeGreaterThan(1)
    const hits = lines.flatMap((line, i) =>
      [...line.matchAll(PALETTE_CLASS_ALL)].map((m) => `${rel}:${i + 1} ${m[0]}`),
    )
    expect(hits).toEqual([])
  })
})

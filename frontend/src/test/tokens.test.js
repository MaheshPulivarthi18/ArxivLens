// FE-0e: guard against hard-coded dark-palette colour classes coming back into
// screens that are on the theme tokens (src/index.css, tailwind.config.js).
// FE-3b extends GUARDED_FILES with the ask-flow files.
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
]

export const PALETTE_CLASS =
  /\b(?:[a-z-]+:)*(text|bg|border|ring|from|via|to|shadow)-(slate|emerald|amber|cyan|teal|red|green)-\d/

// Resolve with node path. Vite rewrites a URL built from a template literal and
// import.meta.url into an import.meta.glob lookup, which breaks under Vitest.
const SRC_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (rel) => readFileSync(path.join(SRC_DIR, rel), 'utf8')

// Global copy so every hit on a line is reported, not just the first.
const PALETTE_CLASS_ALL = new RegExp(PALETTE_CLASS.source, 'g')

describe('palette-class guard', () => {
  it('pattern catches palette classes, with and without variants', () => {
    for (const cls of ['text-emerald-400', 'hover:bg-emerald-500/10', 'focus:border-red-500', 'dark:md:ring-slate-700', 'shadow-cyan-900']) {
      expect(cls).toMatch(PALETTE_CLASS)
    }
    for (const cls of ['text-primary', 'bg-destructive/10', 'border-border', 'text-supported-foreground', 'shadow-black/80']) {
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

// FE-0h: text inputs draw their outline with the form-control token
// `border-input` (>= 3:1, WCAG 1.4.11), never the decorative `border-border`
// hairline (index.css --input vs --border).
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Paths relative to frontend/src/.
export const CONTROL_FILES = ['components/IngestModal.jsx', 'components/PapersLibrary.jsx']

// Resolve with node path. Vite rewrites a URL built from a template literal and
// import.meta.url into an import.meta.glob lookup, which breaks under Vitest.
const SRC_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (rel) => readFileSync(path.join(SRC_DIR, rel), 'utf8')

// Whole-token matches: `border-border`, `border-border/80`, `focus:border-border`
// are hits; `border-border-x` or `xborder-border` are not.
export const BORDER_BORDER = /(?:^|\s)(?:[\w-]+:)*border-border(?:\/\d+)?(?=\s|$)/
export const BORDER_INPUT = /(?:^|\s)border-input(?=\s|$)/

// Every `<input .../>` element's static className string. JSX inputs are void
// elements, so the tag ends at the next `/>`.
export function inputClassNames(source) {
  const out = []
  let i = source.indexOf('<input')
  while (i !== -1) {
    const end = source.indexOf('/>', i)
    const tag = source.slice(i, end === -1 ? undefined : end)
    const m = tag.match(/className=(?:"([^"]*)"|\{\s*(["'`])([^"'`]*)\2\s*\})/)
    out.push(m ? (m[1] ?? m[3]) : null)
    i = source.indexOf('<input', i + 1)
  }
  return out
}

describe('form-control border guard', () => {
  it('patterns match whole tokens only', () => {
    for (const cls of ['border-border', 'a border-border b', 'border-border/80', 'focus:border-border']) {
      expect(cls).toMatch(BORDER_BORDER)
    }
    for (const cls of ['border-input', 'border-border-x', 'xborder-border', 'border-primary']) {
      expect(cls).not.toMatch(BORDER_BORDER)
    }
    expect('bg-card border border-input rounded-lg').toMatch(BORDER_INPUT)
    for (const cls of ['focus:border-input', 'border-input-x', 'border-border']) {
      expect(cls).not.toMatch(BORDER_INPUT)
    }
  })

  it('extracts the className of each <input', () => {
    const src = '<div className="x"><input type="text" className="a b" onChange={(e) => f(e)} /><input className={`c`} /></div>'
    expect(inputClassNames(src)).toEqual(['a b', 'c'])
  })

  it.each(CONTROL_FILES)('%s text inputs use border-input, not border-border', (rel) => {
    const classNames = inputClassNames(read(rel))
    expect(classNames.length, `${rel}: no <input found`).toBeGreaterThan(0)
    for (const cls of classNames) {
      expect(cls, `${rel}: <input without a static className`).not.toBeNull()
      expect(cls, `${rel}: <input className lacks border-input`).toMatch(BORDER_INPUT)
      expect(cls, `${rel}: <input className has border-border`).not.toMatch(BORDER_BORDER)
    }
  })
})

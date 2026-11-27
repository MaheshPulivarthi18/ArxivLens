// FE-0h/FE-0k: every form control under src/components/ draws its outline with
// the form-control token `border-input` (>= 3:1, WCAG 1.4.11), never the
// decorative `border-border` hairline (index.css --input vs --border). A
// borderless control may instead be outlined by its `focus-within:` wrapper,
// which then shows focus with the full-strength `ring-ring` (FE-0 design).
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Resolve with node path. Vite rewrites a URL built from a template literal and
// import.meta.url into an import.meta.glob lookup, which breaks under Vitest.
const SRC_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (rel) => readFileSync(path.join(SRC_DIR, rel), 'utf8')

// Whole-token matches: `border-border`, `border-border/80`, `focus:border-border`
// are hits; `border-border-x` or `xborder-border` are not.
export const BORDER_BORDER = /(?:^|\s)(?:[\w-]+:)*border-border(?:\/\d+)?(?=\s|$)/
export const BORDER_INPUT = /(?:^|\s)border-input(?=\s|$)/
// Any border utility (`border`, `border-2`, `border-x`, `focus:border-input`, ...).
export const ANY_BORDER = /(?:^|\s)(?:[\w-]+:)*border(?:-[\w./[\]#-]+)?(?=\s|$)/
// Full-strength focus ring on a wrapper: `focus-within:ring-ring`, not `/30`.
export const WITHIN_RING = /(?:^|\s)focus-within:ring-ring(?=\s|$)/

// Intrinsic (lowercase) form controls only: `<Input`, `<inputs` don't count.
const CONTROL_TAG = /<(?:input|textarea|select)(?=[\s/>])/
const CONTROL_TAGS = new Set(['input', 'textarea', 'select'])

export const hasControl = (source) => CONTROL_TAG.test(source)

// Index just past the `>` that ends the tag starting at `start` (the `<`).
// Skips `>` inside `{...}` (e.g. `=>`) and inside string literals.
function tagEnd(source, start) {
  let depth = 0
  let quote = null
  for (let i = start + 1; i < source.length; i++) {
    const c = source[i]
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = null
    } else if (c === '"' || c === "'" || c === '`') quote = c
    else if (c === '{') depth++
    else if (c === '}') depth--
    else if (c === '>' && depth === 0) return i + 1
  }
  return source.length
}

// The static className of an opening tag, or null (absent, or built at runtime).
function staticClassName(tag) {
  const m = tag.match(/\sclassName=(?:"([^"]*)"|\{\s*(?:"([^"]*)"|'([^']*)'|`([^`$]*)`)\s*\})/)
  return m ? (m[1] ?? m[2] ?? m[3] ?? m[4]) : null
}

// Every intrinsic control in a JSX source, in order: { tag, className, wrapper },
// where wrapper is the static className of the nearest enclosing element whose
// className contains `focus-within:` (or null). Fragments carry no className
// and are skipped.
export function controls(source) {
  const out = []
  const stack = [] // open elements: { name, className }
  const tagRe = /<(\/?)([A-Za-z][\w.:-]*)(?=[\s/>])/g
  let m
  while ((m = tagRe.exec(source)) !== null) {
    const [, closing, name] = m
    const end = tagEnd(source, m.index)
    const tag = source.slice(m.index, end)
    tagRe.lastIndex = end
    if (closing) {
      const at = stack.map((e) => e.name).lastIndexOf(name)
      if (at !== -1) stack.length = at
      continue
    }
    const className = staticClassName(tag)
    if (CONTROL_TAGS.has(name)) {
      const wrapper = [...stack].reverse().find((e) => e.className?.includes('focus-within:'))
      out.push({ tag: name, className, wrapper: wrapper ? wrapper.className : null })
    }
    if (!tag.endsWith('/>')) stack.push({ name, className })
  }
  return out
}

// Why a control breaks the outline rule, or null if it follows it.
export function violation({ className, wrapper }) {
  if (className === null) return 'no static className'
  if (BORDER_BORDER.test(className)) return 'className has border-border'
  if (BORDER_INPUT.test(className)) return null // rule (a)
  if (ANY_BORDER.test(className)) return 'className has a border that is not border-input'
  // rule (b): a borderless control outlined by its focus-within wrapper
  if (wrapper === null) return 'no border-input and no focus-within: outline wrapper'
  if (BORDER_BORDER.test(wrapper)) return 'wrapper has border-border'
  if (!BORDER_INPUT.test(wrapper)) return 'wrapper lacks border-input'
  if (!WITHIN_RING.test(wrapper)) return 'wrapper lacks full-strength focus-within:ring-ring'
  return null
}

// Every .jsx under src/components/ (recursively, tests excluded) that contains
// an intrinsic control. Paths relative to src/, with `/` separators.
function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((d) => {
    const full = path.join(dir, d.name)
    return d.isDirectory() ? walk(full) : [full]
  })
}
export const CONTROL_SOURCES = walk(path.join(SRC_DIR, 'components'))
  .filter((f) => f.endsWith('.jsx') && !f.endsWith('.test.jsx'))
  .map((f) => path.relative(SRC_DIR, f).split(path.sep).join('/'))
  .filter((rel) => hasControl(read(rel)))
  .sort()

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
    for (const cls of ['border', 'a border-2 b', 'border-x', 'focus:border-input', 'border-primary/50']) {
      expect(cls).toMatch(ANY_BORDER)
    }
    for (const cls of ['outline-none', 'rounded-lg', 'focus-within:ring-ring']) {
      expect(cls).not.toMatch(ANY_BORDER)
    }
    expect('a focus-within:ring-2 focus-within:ring-ring b').toMatch(WITHIN_RING)
    for (const cls of ['focus-within:ring-ring/30', 'ring-ring', 'focus-within:ring-ring-x']) {
      expect(cls).not.toMatch(WITHIN_RING)
    }
  })

  it('extracts the className of each <input', () => {
    const src = '<div className="x"><input type="text" className="a b" onChange={(e) => f(e)} /><input className={`c`} /></div>'
    expect(controls(src).map((c) => c.className)).toEqual(['a b', 'c'])
  })

  it('extracts textarea and select, with `>` inside braces', () => {
    const src = [
      '<form className="p">',
      '  <textarea className="t1" onChange={(e) => f(e)} />',
      '  <textarea onChange={(e) => g(e.target.value)} className="t2">{v}</textarea>',
      '  <select className={"s1"} onChange={(e) => h(e)}><option value="a">A</option></select>',
      '  <input className={cn("x")} />',
      '</form>',
    ].join('\n')
    expect(controls(src)).toEqual([
      { tag: 'textarea', className: 't1', wrapper: null },
      { tag: 'textarea', className: 't2', wrapper: null },
      { tag: 'select', className: 's1', wrapper: null },
      { tag: 'input', className: null, wrapper: null },
    ])
  })

  it('applies the outline rule', () => {
    const one = (src) => {
      const found = controls(src)
      expect(found).toHaveLength(1)
      return violation(found[0])
    }
    expect(one('<textarea className="border border-input" />')).toBeNull()
    expect(one('<textarea className="border border-border" />')).toMatch(/border-border/)
    expect(one('<select className="border border-border"><option>a</option></select>')).toMatch(/border-border/)
    expect(one('<input className="border border-primary" />')).toMatch(/not border-input/)
    expect(one('<input className="outline-none" />')).toMatch(/no focus-within/)
    expect(one('<input className={cn("border-input")} />')).toMatch(/static/)

    const wrapped = (cls) =>
      `<div className="${cls}"><span className="i" /><input className="outline-none" onChange={(e) => f(e)} /></div>`
    expect(one(wrapped('border border-input focus-within:ring-2 focus-within:ring-ring'))).toBeNull()
    expect(one(wrapped('border border-border/90 focus-within:ring-2 focus-within:ring-ring'))).toMatch(/wrapper has border-border/)
    expect(one(wrapped('border border-input focus-within:ring-2 focus-within:ring-ring/30'))).toMatch(/ring-ring/)
    expect(one(wrapped('border focus-within:ring-2 focus-within:ring-ring'))).toMatch(/lacks border-input/)
    // A closed sibling is not an enclosing wrapper.
    expect(one('<div><div className="border-input focus-within:ring-ring"></div><input className="outline-none" /></div>')).toMatch(
      /no focus-within/,
    )
  })

  it('discovers intrinsic lowercase controls only', () => {
    for (const src of ['<Input className="border-border" />', '<inputs />', '<Select>', 'a<b']) {
      expect(hasControl(src), src).toBe(false)
      expect(controls(src), src).toEqual([])
    }
    for (const src of ['<input/>', '<textarea\n  rows={3} />', '<select>']) {
      expect(hasControl(src), src).toBe(true)
    }
  })

  it('scans every component, including the known controls', () => {
    expect(CONTROL_SOURCES).toEqual(
      expect.arrayContaining(['components/HeroSearch.jsx', 'components/IngestModal.jsx', 'components/PapersLibrary.jsx']),
    )
  })

  it.each(CONTROL_SOURCES)('%s form controls are outlined with border-input', (rel) => {
    const found = controls(read(rel))
    expect(found.length, `${rel}: no control extracted`).toBeGreaterThan(0)
    for (const c of found) {
      expect(violation(c), `${rel}: <${c.tag} className=${JSON.stringify(c.className)}`).toBeNull()
    }
  })
})

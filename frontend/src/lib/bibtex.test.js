import { describe, it, expect } from 'vitest';
import { arxivYear, bibKey, bibEntry, bibFile, bibFilename, bibEscape } from './bibtex';
import { supportedCitation, unsupportedCitation, unverifiedCitation } from '../test/fixtures';

// B-UI AC4/AC5 (design FE-4 › B-UI addendum §2).
describe('bibtex', () => {
  it('year comes from the arXiv ID only', () => {
    expect(arxivYear('2401.01234')).toBe(2024);
    expect(arxivYear('2401.01234v3')).toBe(2024);
    expect(arxivYear('cs/0601001')).toBe(2006);
    expect(arxivYear('hep-th/9901001')).toBe(1999);
    for (const bad of ['', undefined, 'abc', '2413.01234', '12345']) expect(arxivYear(bad)).toBeNull();
  });

  it('keys are stable: "arxiv" + ID without version and punctuation', () => {
    expect(bibKey('2401.01234')).toBe('arxiv240101234');
    expect(bibKey('2401.01234v2')).toBe('arxiv240101234');
    expect(bibKey('cs/0601001')).toBe('arxivcs0601001');
    expect(bibKey('')).toBeNull();
    expect(bibEntry(supportedCitation)).toBe(bibEntry({ ...supportedCitation }));
  });

  it('writes an @misc entry with eprint, archivePrefix, year and url, and nothing invented', () => {
    expect(bibEntry(supportedCitation)).toBe(
      '@misc{arxiv240101234,\n  title = {Grounded Retrieval for Scientific QA},\n  eprint = {2401.01234},\n' +
        '  archivePrefix = {arXiv},\n  year = {2024},\n  url = {https://arxiv.org/abs/2401.01234}\n}',
    );
    const odd = bibEntry({ ...supportedCitation, arxiv_id: 'nope', title: '' });
    expect(odd).not.toMatch(/year|title|author|primaryClass|2026/);
    expect(bibEntry({ ...supportedCitation, arxiv_id: '' })).toBeNull();
  });

  it('escapes { } & % $ # _ and backslash in titles', () => {
    expect(bibEscape('A {b} & 5% $x #1 a_b \\c')).toBe('A \\{b\\} \\& 5\\% \\$x \\#1 a\\_b \\textbackslash{}c');
    expect(bibEntry({ ...supportedCitation, title: 'R&D_50%' })).toContain('title = {R\\&D\\_50\\%}');
  });

  it('one entry per paper; a written answer exports supported citations only', () => {
    const cits = [supportedCitation, { ...supportedCitation, marker: 2 }, unsupportedCitation, { ...supportedCitation, marker: 4 }];
    const quoted = bibFile(cits, { answerKind: 'quoted' });
    expect(quoted.count).toBe(2);
    expect(quoted.text.match(/@misc\{arxiv240101234,/g)).toHaveLength(1);
    expect(quoted.text).toContain('@misc{arxiv240205678,');
    expect(quoted.text).not.toMatch(/\bauthor\s*=|primaryClass/);
    expect(bibFile(cits).count).toBe(2);
    const written = bibFile([...cits, unverifiedCitation], { answerKind: 'synthesized' });
    expect(written.count).toBe(1);
    expect(written.text).not.toMatch(/arxiv240205678|arxiv240309999/);
  });

  it('filename is arxivlens-YYYYMMDD-HHMMSS.bib in local time', () => {
    expect(bibFilename(new Date(2026, 10, 8, 4, 5, 6))).toBe('arxivlens-20261108-040506.bib');
  });
});

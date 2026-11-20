import { describe, it, expect } from 'vitest';
import { citationState, markdownNote } from './citations';
import {
  supportedCitation,
  unsupportedCitation,
  unverifiedCitation,
  contradictorySupported,
  partialCitation,
  contradictedCitation,
  legacyRefusalZero,
} from '../test/fixtures';

const SUPPORTED_WORD = /\bSupported\b/;
const VERIFIED = /verified/i;

// Design FE-3 §1, rows 1–5, in both modes where it matters.
const ROWS = [
  ['row 1: status supported', supportedCitation, false, 'supported', 'Supported · 87.3%'],
  ['row 1: status supported (refusal mode)', supportedCitation, true, 'supported', 'Supported · 87.3%'],
  ['row 2: status unverified', unverifiedCitation, false, 'unchecked', 'Not checked'],
  ['row 2: status unverified (refusal mode)', unverifiedCitation, true, 'unchecked', 'Not checked'],
  ['row 3: no status, score 0, refusal mode', legacyRefusalZero, true, 'unchecked', 'Not checked'],
  ['row 4: no status, supported: true', { ...supportedCitation, status: undefined }, false, 'supported', 'Supported · 87.3%'],
  ['row 5: status unsupported', unsupportedCitation, false, 'unsupported', 'Unsupported · 12.0%'],
  ['row 5: status supported but supported: false', contradictorySupported, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: reserved partially_supported', partialCitation, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: reserved contradicted', contradictedCitation, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: unknown status "bogus"', { ...supportedCitation, status: 'bogus' }, false, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: unknown status "bogus" (refusal mode)', { ...supportedCitation, status: 'bogus' }, true, 'unsupported', 'Unsupported · 87.3%'],
  ['row 5: no status, supported: false (answer mode)', { ...unsupportedCitation, status: undefined }, false, 'unsupported', 'Unsupported · 12.0%'],
  ['row 5: no status, score 0 outside a refusal', legacyRefusalZero, false, 'unsupported', 'Unsupported · 0.0%'],
  ['refusal A: checked passage keeps its best score', { ...unsupportedCitation, score: 0.31 }, true, 'unsupported', 'Unsupported · 31.0%'],
];

describe('citationState', () => {
  it.each(ROWS)('%s', (_name, cit, refusal, kind, text) => {
    const state = citationState(cit, { refusal });
    expect(state.kind).toBe(kind);
    expect(state.text).toBe(text);
    expect(state.tooltip.length).toBeGreaterThan(0);
  });

  it.each(ROWS)('%s: only a supported kind says "Supported", nothing says "verified"', (_name, cit, refusal) => {
    const state = citationState(cit, { refusal });
    for (const out of [state.text, state.tooltip]) {
      expect(out).not.toMatch(VERIFIED);
      if (state.kind === 'supported') expect(out).toMatch(SUPPORTED_WORD);
      else expect(out).not.toMatch(SUPPORTED_WORD);
    }
  });

  it('a "Not checked" chip shows no percentage', () => {
    expect(citationState(unverifiedCitation).text).not.toMatch(/%/);
    expect(citationState(legacyRefusalZero, { refusal: true }).text).not.toMatch(/%/);
  });

  it('defaults to answer mode', () => {
    expect(citationState(legacyRefusalZero).kind).toBe('unsupported');
  });

  it('tooltips describe the meaning without naming a checking method', () => {
    for (const [, cit, refusal] of ROWS) {
      expect(citationState(cit, { refusal }).tooltip).not.toMatch(/entail|nli|cosine|embedding/i);
    }
  });
});

describe('markdownNote', () => {
  const md = markdownNote('An answer [1].', [supportedCitation, unsupportedCitation, unverifiedCitation]);

  it('uses a plain "## References" heading and never says verified', () => {
    expect(md).toContain('\n## References\n');
    expect(md).not.toMatch(VERIFIED);
  });

  it('ends each entry with its chip text', () => {
    const lines = md.trim().split('\n').filter((l) => l.startsWith('- ['));
    expect(lines).toHaveLength(3);
    expect(lines[0]).toMatch(/• Supported · 87\.3%$/);
    expect(lines[1]).toBe(
      '- [2] **Entailment Checks for Citations** (§ Results) - [arXiv:2402.05678](https://arxiv.org/pdf/2402.05678v1) • Unsupported · 12.0%',
    );
    expect(lines[2]).toMatch(/• Not checked$/);
  });
});

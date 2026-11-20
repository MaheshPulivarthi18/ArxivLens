// Citation status rule (ADR-0003, ADR-0014; design docs/design/FE-3.md §1), shared by the card chip, the footer
// summary and the Markdown export. Plain ESM, no JSX. Conservative on purpose: a reserved or unknown status, or a
// `status`/`supported` disagreement, can never read as Supported. No output of this module says "verified".

const pct = (score) => `${((Number(score) || 0) * 100).toFixed(1)}%`;

function kindOf(cit, refusal) {
  const { status, supported, score } = cit;
  if (status === 'supported' && supported !== false) return 'supported';
  if (status === 'unverified') return 'unchecked';
  if (!status && refusal && !score) return 'unchecked';
  if (!status && supported === true) return 'supported';
  return 'unsupported';
}

// → { kind: 'supported' | 'unsupported' | 'unchecked', text, tooltip }
export function citationState(cit, { refusal = false } = {}) {
  const kind = kindOf(cit, refusal);
  const score = pct(cit.score);
  if (kind === 'supported') {
    return {
      kind,
      text: `Supported · ${score}`,
      tooltip: `Supported: a check found that this passage backs the sentence citing it (support score ${score}).`,
    };
  }
  if (kind === 'unsupported') {
    return {
      kind,
      text: `Unsupported · ${score}`,
      tooltip: refusal
        ? `Unsupported: a draft statement citing this passage was checked and not supported (best support score ${score}).`
        : `Unsupported: this passage was checked and doesn't back the sentence citing it (support score ${score}). Read it before relying on that sentence.`,
    };
  }
  return {
    kind,
    text: 'Not checked',
    tooltip: 'Not checked: no check ran for this passage, because the checker was unavailable or no sentence cited it.',
  };
}

// Markdown research note for an answer (answer mode only; a refusal has no answer to export).
export function markdownNote(answer, citations) {
  let md = `# Clinical & Academic Research Note\n\n${answer}\n\n## References\n`;
  (citations || []).forEach((c) => {
    const pdf = c.pdf_url || `https://arxiv.org/pdf/${c.arxiv_id}.pdf`;
    md += `- [${c.marker}] **${c.title}** (§ ${c.section}) - [arXiv:${c.arxiv_id}](${pdf}) • ${citationState(c).text}\n`;
  });
  return md;
}

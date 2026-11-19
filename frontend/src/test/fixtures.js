// Fixture /v1/ask responses. Field names follow AskResponse and Citation in internal/query/rag.go.

export const supportedCitation = {
  marker: 1,
  arxiv_id: '2401.01234',
  title: 'Grounded Retrieval for Scientific QA',
  chunk_id: 101,
  section: 'Methods',
  content: 'We retrieve passages with hybrid search and verify each claim.',
  pdf_url: 'https://arxiv.org/pdf/2401.01234v2',
  score: 0.873,
  supported: true,
  paper_version: 2,
  page: 4,
  quote: 'verify each claim',
  status: 'supported',
};

export const unsupportedCitation = {
  marker: 2,
  arxiv_id: '2402.05678',
  title: 'Entailment Checks for Citations',
  chunk_id: 202,
  section: 'Results',
  content: 'Entailment scores fall below the support threshold.',
  pdf_url: 'https://arxiv.org/pdf/2402.05678v1',
  score: 0.12,
  supported: false,
  paper_version: 1,
  status: 'unsupported',
};

// Verifier outage (ADR-0014): no check ran, score 0.
export const unverifiedCitation = {
  marker: 3,
  arxiv_id: '2403.09999',
  title: 'Passage Without a Check',
  chunk_id: 303,
  section: 'Introduction',
  content: 'An introductory passage.',
  pdf_url: 'https://arxiv.org/pdf/2403.09999v1',
  score: 0,
  supported: false,
  paper_version: 1,
  status: 'unverified',
};

export const quotedAnswer = {
  answer: 'Hybrid search finds the passages [1] and an entailment check rejects weak ones [2].',
  citations: [supportedCitation, unsupportedCitation],
  trace_id: 'trace-0123456789abcdef',
  answer_kind: 'quoted',
  synthesis_available: true,
};

export const synthesizedAnswer = {
  answer: 'Each claim is verified against its cited passage [1].',
  citations: [supportedCitation],
  trace_id: 'trace-synth-0001',
  answer_kind: 'synthesized',
};

// ADR-0003 refusal: answer withheld, passages still returned as evidence.
export const refusalAnswer = {
  answer:
    "I couldn't verify any statement of a draft answer against the retrieved passages, so no answer is shown. The passages found are listed below.",
  citations: [unsupportedCitation, unverifiedCitation],
  trace_id: 'trace-refusal-0001',
  refusal_reason: 'all_citations_unsupported',
};

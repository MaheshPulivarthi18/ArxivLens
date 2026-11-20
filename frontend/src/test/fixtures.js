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

// Answer mode with all three states: supported, unsupported, and unchecked (a kept claim's other citation).
export const mixedAnswer = {
  answer: 'Hybrid search finds the passages [1] and an entailment check rejects weak ones [2] [3].',
  citations: [supportedCitation, unsupportedCitation, unverifiedCitation],
  trace_id: 'trace-mixed-0001',
  answer_kind: 'quoted',
};

// Early refusal (rag.go): nothing relevant was retrieved, no refusal_reason.
export const earlyRefusal = {
  answer: 'No indexed papers are relevant to this question.',
  citations: [],
  trace_id: 'trace-early-0001',
};

// ADR-0003 refusals: answer withheld, passages still returned as evidence.
// A: a check ran on the first passage (best score 0.31); the second was never checked.
export const refusalA = {
  answer:
    "I couldn't verify any statement of a draft answer against the retrieved passages, so no answer is shown. The passages found are listed below.",
  citations: [{ ...unsupportedCitation, marker: 1, score: 0.31 }, { ...unverifiedCitation, marker: 2 }],
  trace_id: 'trace-refusal-a',
  refusal_reason: 'all_citations_unsupported',
};

// B: the checker was unavailable for at least one passage.
export const refusalB = {
  answer: 'Verification is unavailable right now, so no answer is shown.',
  citations: [{ ...unsupportedCitation, marker: 1, score: 0.22 }, { ...unverifiedCitation, marker: 2 }],
  trace_id: 'trace-refusal-b',
  refusal_reason: 'verification_unavailable',
};

// C (LLM path only): every passage unchecked, score 0.
export const refusalC = {
  answer: 'The retrieved passages are insufficient to answer this question.',
  citations: [{ ...unverifiedCitation, marker: 1 }, { ...unverifiedCitation, marker: 2, chunk_id: 304 }],
  trace_id: 'trace-refusal-c',
  refusal_reason: 'insufficient_evidence',
};

// Citation variants for the status rule (design FE-3 §1).
export const contradictorySupported = { ...supportedCitation, status: 'supported', supported: false };
export const partialCitation = { ...supportedCitation, status: 'partially_supported', supported: false };
export const contradictedCitation = { ...supportedCitation, status: 'contradicted', supported: false };
export const legacyRefusalZero = { ...unverifiedCitation, status: undefined, score: 0, supported: false };

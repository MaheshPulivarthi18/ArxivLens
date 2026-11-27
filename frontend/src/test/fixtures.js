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

// FE-4a: responses with `claims` (rag.go Claim: text, status, markers; citations positional per claim).
// Quoted answer: one claim per status, plus an uncited sentence. Citation 1's content crosses a line break and holds
// an inline [12]; its quote is the server's normalised form (used by FE-4b).
export const quotedClaims = {
  answer:
    'We retrieve passages with hybrid search and check each claim [1]. Entailment scores fall below the support threshold [2]. An introductory passage [3]. Results vary across domains.',
  citations: [
    {
      ...supportedCitation,
      content: 'We retrieve passages with hybrid\nsearch [12] and check each claim.',
      quote: 'We retrieve passages with hybrid search and check each claim.',
    },
    unsupportedCitation,
    unverifiedCitation,
  ],
  claims: [
    { text: 'We retrieve passages with hybrid search and check each claim.', status: 'supported', markers: [1] },
    { text: 'Entailment scores fall below the support threshold.', status: 'unsupported', markers: [2] },
    { text: 'An introductory passage.', status: 'unverified', markers: [3] },
    { text: 'Results vary across domains.', status: 'unverified', markers: [] },
  ],
  trace_id: 'trace-claims-0001',
  answer_kind: 'quoted',
};

// Written answer (ADR-0019): passage 1 cited by both claims (repeat) and no [2] (skip); all supported, no quotes, page on [3] only.
const skipsM1 = { ...supportedCitation, quote: undefined, page: undefined };
const skipsM3 = {
  ...supportedCitation,
  marker: 3,
  arxiv_id: '2405.04321',
  title: 'Claim-Level Checking in Practice',
  chunk_id: 505,
  section: 'Evaluation',
  content: 'Each statement is compared with the passage it cites.',
  pdf_url: 'https://arxiv.org/pdf/2405.04321v1',
  paper_version: 1,
  page: 7,
  quote: undefined,
  score: 0.912,
};
export const synthesizedSkips = {
  answer: 'Retrieval grounds each answer [1]. Statements are compared with their passages [1] [3].',
  citations: [skipsM1, { ...skipsM1, score: 0.801 }, skipsM3],
  claims: [
    { text: 'Retrieval grounds each answer.', status: 'supported', markers: [1] },
    { text: 'Statements are compared with their passages.', status: 'supported', markers: [1, 3] },
  ],
  trace_id: 'trace-skips-0001',
  answer_kind: 'synthesized',
};

// Same claims, but citations out of positional order: markers must still map by number.
export const brokenPositional = {
  ...synthesizedSkips,
  citations: [skipsM3, skipsM1, { ...skipsM1, score: 0.801 }],
  trace_id: 'trace-broken-0001',
};

// `claims` values that must fall back to the FE-3 prose.
export const malformedClaims = [
  'not a list',
  [{ status: 'supported', markers: [1] }],
  [null],
  [{ text: 'Fine.', status: 'supported', markers: [1] }, { text: 7, status: 'supported', markers: [2] }],
];

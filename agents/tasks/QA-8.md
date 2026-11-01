# QA-8: LLM answer synthesis behind citation validation

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P0-next (top roadmap item) |
| **PRD ref** | QA-8; Technical PRD §7.2, §11; Product PRD risks "Template answers feel shallow", "Hallucinated citations" |
| **Depends on** | ADR-0001 (provider/budget) **Accepted**, ADR-0002 (where synthesis runs) **Accepted**, EVAL-1, AI-1 |
| **Status** | Blocked (needs ADR-0001, ADR-0002) |

## Goal
Replace the templated answer in `/v1/ask` with an LLM-written synthesis over the reranked passages, while keeping every
guarantee: numbered `[n]` citations, fabricated markers discarded, per-sentence support scoring, honest refusals.

## Scope (assuming ADR-0002 = synthesis in Go; adjust if the ADR decides otherwise)
- `internal/query/rag.go`: a `synthesizer` interface with two implementations: the current template (fallback) and an LLM client.
- Prompt: passages numbered `[1]..[K]` with title + section; instruct to cite only those numbers, to answer only from them,
  and to reply with an exact refusal sentence when they don't answer the question. Store the prompt as a Go constant with a version string returned in the response (e.g. `prompt_version`) — additive response field, no breaking change.
- Config via env: provider, model, API key, timeout, max tokens (per ADR-0001). No key configured → template path, same as today.
- On LLM timeout/error → template answer, and log it with `trace_id`.
- Out of scope: streaming (QA-9), regeneration on unsupported citations (ADR-0003).

## Acceptance criteria
- [ ] AC1 — With no API key set, behaviour and tests are identical to today.
- [ ] AC2 — Unit tests with a fake LLM: fabricated marker `[9]` with K=3 is dropped; LLM refusal text → refusal response; LLM error → template fallback.
- [ ] AC3 — `make eval` with the LLM enabled: supported citations ≥ 90%, correct refusal ≥ 95%, `/v1/ask` p95 < 3 s — and not worse than the EVAL-1 baseline on any metric. Paste before/after.
- [ ] AC4 — Cost per answer estimated from token counts and within ADR-0001 budget (paste numbers).
- [ ] AC5 — README Known limitations and Product PRD QA-8 status updated.

## Verification
```bash
make test-unit
make stack-up && LLM_API_KEY=... python scripts/eval_gate.py
```

## Notes

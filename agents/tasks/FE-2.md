# FE-2: "Export all citations" (.bib) for an answer

| | |
|---|---|
| **Owner** | frontend-engineer |
| **Priority** | P2 |
| **PRD ref** | UI-5 (per-citation BibTeX copy already exists in `ReferencesStack.jsx`; this completes the "export" half) |
| **Depends on** | FE-1 |
| **Status** | Ready |

## Goal
Priya (Product PRD persona) copies citations into a related-work draft. Let her take all of an answer's references at once.

## Scope
- A button in the references area: "Export .bib" → downloads `arxivlens-<timestamp>.bib` containing one entry per unique `arxiv_id` in the answer (dedupe papers cited multiple times).
- Reuse the existing per-citation BibTeX builder; move it to `src/lib/bibtex.js` so both paths share it.
- Out of scope: saved questions (rest of UI-5).

## Acceptance criteria
- [ ] AC1 — Unit tests for `bibtex.js`: dedupe by arxiv_id, escaping of `{ } &` in titles, stable keys.
- [ ] AC2 — Button is keyboard accessible and labelled; disabled when the answer is a refusal.
- [ ] AC3 — Lint, test, build pass.

## Verification
```bash
cd frontend && npm ci && npm run lint && npm test && npm run build
```

## Notes

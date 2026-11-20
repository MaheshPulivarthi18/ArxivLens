# ArxivLens frontend

React 19 + Vite 8 + Tailwind 3 UI for asking questions over indexed arXiv papers and inspecting the cited evidence.

## Commands

Run from `frontend/`:

| Command | What |
|---|---|
| `npm ci` | Install the exact dependencies from `package-lock.json` |
| `npm run dev` | Dev server on :5173; proxies `/v1` to the query API on :8080 |
| `npm run lint` | oxlint |
| `npm test` | Vitest component tests (jsdom, no network); test files are `src/**/*.test.jsx` |
| `npm run build` | Production build |

CI runs `npm ci`, `npm run lint`, `npm test` and `npm run build` on every PR.

## Tests

- Setup: `src/test/setup.js` (jest-dom matchers, cleanup after each test).
- Fixtures: `src/test/fixtures.js` holds `/v1/ask` responses shaped like `AskResponse` / `Citation` in `internal/query/rag.go`.
- Assert on visible text and roles, not class names, so restyles don't break tests.

# frontend/AGENTS.md — Frontend area rules

Owner: **frontend-engineer**. Read the root `AGENTS.md` first.

## Stack
React 19, Vite 8, Tailwind 3, Radix UI primitives (`src/components/ui/`), lucide-react icons. Plain JSX (no TypeScript yet).

## Commands (run from `frontend/`)
| Command | What |
|---|---|
| `npm ci` | Install exact deps |
| `npm run dev` | Dev server on :5173, proxies `/v1` → `:8080` (see `vite.config.js`) |
| `npm run lint` | oxlint |
| `npm run build` | Production build (must pass before a PR) |

## Map
- `App.jsx` — top-level state and routing between hero, synthesis view, library
- `HeroSearch.jsx` → `SynthesisView.jsx` + `ReferencesStack.jsx` — ask flow and citations (per-citation BibTeX copy already exists)
- `PapersLibrary.jsx`, `IngestModal.jsx`, `InfrastructureModal.jsx` — library, ingest, stats
- `components/ui/` — shared primitives; extend, don't fork

## Rules
- The backend API is a contract. Consume only fields documented in the root README API table and `internal/query/rag.go` (`AskResponse`, `Citation`). Need a new field → create a task for the backend or AI agent.
- Every citation must stay one click from its PDF (product principle "one click to the source").
- Always render refusals ("No indexed papers are relevant…") as a first-class state, not an error.
- Show `supported: false` citations visibly flagged, never silently hidden (until ADR-0003 decides otherwise).
- No new dependency without saying why in the PR.

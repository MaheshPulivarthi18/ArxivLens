# OPS-H1: Run processor + embedder workers in docker-compose; one-command full test

| | |
|---|---|
| **Owner** | backend-engineer |
| **Priority** | P0 (unblocks every agent's ability to verify end-to-end) |
| **PRD ref** | Technical PRD §11 "Containerize processor + embedder in compose"; README Known limitations |
| **Depends on** | — |
| **Status** | Ready |

## Goal
Today `make test-full` only works if someone starts three Python processes by hand. Agents can't verify pipeline or
query changes on their own. Put the workers in compose so the full stack starts with one command.

## Scope
- Change: `docker-compose.yml`, `services/processor/Dockerfile`, `services/embedder/Dockerfile`, `Makefile`, `README.md` (Quick start + Known limitations).
  Root `AGENTS.md` §5 still lists the old gotcha — note it in this task's Notes so pm-lead updates it (pm owns AGENTS.md).
- Add compose services `processor`, `embedder-worker`, `embedder-api` (port 8081) under a compose **profile** `workers`,
  so `docker compose up -d` keeps today's infra-only behaviour and `docker compose --profile workers up -d` starts everything.
- Inside the network, services use `kafka:9092`, `postgres:5432`, `minio:9000` via env vars (no code changes needed if config is env-driven; verify).
- Mount a named volume for the Hugging Face cache so models download once.
- Out of scope: GPU passthrough (document it as a note; CPU must work), Go services in compose, unifying deps (OPS-H2).

## Acceptance criteria
- [ ] AC1 — `docker compose up -d` behaves exactly as before (infra only).
- [ ] AC2 — `make stack-up` (new target) runs `docker compose --profile workers up -d --build` and all services become healthy; embedder API has a healthcheck.
- [ ] AC3 — With the stack up, `./bin/arxivlens crawl --query "cat:cs.LG" --limit 2` results in both papers reaching status `embedded` (paste `arxivlens status` output in the PR).
- [ ] AC4 — `make test-full` passes M1–M4 against the compose stack (paste summary). If a suite needs the Go query service, document how it's started.
- [ ] AC5 — README Quick start shows the one-command path; host-run path kept as "Apple-silicon MPS acceleration" option. Known-limitations bullet updated.

## Verification
```bash
make lint
make stack-up && docker compose ps
go build -o bin/arxivlens ./cmd/arxivlens && ./bin/arxivlens migrate
./bin/arxivlens crawl --query "cat:cs.LG" --limit 2
make test-full
```

## Notes

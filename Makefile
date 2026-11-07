# ArxivLens harness. Agents and CI use these targets; keep them deterministic and secret-free.
# Run `make help` for the list.

PY      ?= python3
PYTEST  ?= $(PY) -m pytest
TESTS   ?= tests/
export PYTHONPATH := $(CURDIR)

.DEFAULT_GOAL := help
.PHONY: help setup-py setup-fe build build-go build-fe lint lint-go lint-fe \
        test-unit test-go test-py test-fe test-full infra-up infra-down stack-up stack-down migrate eval eval-seed

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

# --- setup -------------------------------------------------------------------
setup-py: ## Install Python deps into the active environment
	$(PY) -m pip install -r requirements-m0.txt minio ulid-py fastapi uvicorn opentelemetry-sdk

setup-fe: ## Install frontend deps (exact lockfile)
	cd frontend && npm ci

# --- build -------------------------------------------------------------------
build: build-go build-fe ## Build Go CLI and frontend

build-go: ## Build bin/arxivlens
	go build -o bin/arxivlens ./cmd/arxivlens

build-fe: ## Production build of the frontend
	cd frontend && npm run build

# --- lint --------------------------------------------------------------------
lint: lint-go lint-fe ## All linters

lint-go: ## go vet + gofmt check
	go vet ./...
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint-fe: ## oxlint on the frontend
	cd frontend && npm run lint

# --- tests -------------------------------------------------------------------
test-unit: test-go test-py ## Fast tests, no infra (Go + M0 + eval data). Frontend tests: make test-fe

test-go: ## Go unit tests
	go test ./...

test-py: ## Python unit tests: M0 + eval data + worker heartbeat (M0 downloads BGE model on first run)
	$(PYTEST) -q tests/test_m0.py tests/test_evals_data.py tests/test_worker_heartbeat.py

test-fe: ## Frontend tests (available after FE-1)
	@cd frontend && if npm run | grep -q " test"; then npm test; else echo "no frontend tests yet (FE-1)"; fi

test-full: ## All suites M0-M6 (needs make stack-up, make migrate, arxivlens download + query on the host). TESTS=<paths/node IDs>, PYTEST_ARGS=<extra args>
	$(PYTEST) -q $(TESTS) $(PYTEST_ARGS)

# --- infra -------------------------------------------------------------------
infra-up: ## Start Postgres+pgvector, Kafka (+topics), MinIO
	docker compose up -d

infra-down: ## Stop infra (keeps volumes)
	docker compose down

# Seconds `make stack-up` waits for the three worker containers to become healthy.
STACK_TIMEOUT ?= 900
STACK_WORKERS := arxivlens-embedder-api arxivlens-processor arxivlens-embedder-worker

stack-up: ## Infra + processor, embedder worker, embedder API (:8081); waits until healthy
	docker compose --profile workers up -d --build
	@end=$$(( $$(date +%s) + $(STACK_TIMEOUT) )); for c in $(STACK_WORKERS); do \
	  until s=$$(docker inspect -f '{{.State.Health.Status}}' $$c 2>/dev/null); [ "$$s" = healthy ]; do \
	    if [ "$$s" = unhealthy ] || [ $$(date +%s) -ge $$end ]; then echo "stack-up: $$c is '$$s' (limit $(STACK_TIMEOUT)s)"; \
	      docker compose --profile workers ps -a; docker logs --tail 50 $$c 2>&1; \
	      docker inspect -f '{{json .State.Health}}' $$c; exit 1; fi; \
	    sleep 5; \
	  done; echo "stack-up: $$c healthy"; done
	docker compose --profile workers ps

stack-down: ## Stop infra + workers (keeps volumes)
	docker compose --profile workers down

migrate: build-go ## Apply DB schema
	./bin/arxivlens migrate

# --- quality -----------------------------------------------------------------
eval: ## Retrieval/answer eval; uses the gate from EVAL-1 when present
	@if [ -f scripts/eval_gate.py ]; then $(PY) scripts/eval_gate.py; else $(PY) scripts/evaluate_retrieval.py; fi

EVAL_CORPUS       ?= evals/corpus.txt
ARXIVLENS_API_URL ?= http://localhost:8080
EVAL_SEED_LOG     ?= seed-ids.log

# arXiv politeness (Technical PRD §9): `sleep 3` before every ingest, retries included. Hard-coded on purpose.
# Up to 3 attempts per ID (backoff 10 s, 30 s) on HTTP 000/5xx; 4xx or a 200 without chunks+embeddings fails at once.
# Never skips an ID: goes through the whole corpus, then exits 1 if any failed. Stops early (exit 1) after 2 IDs
# in a row used up their retries. One line per ID goes to $(EVAL_SEED_LOG).
eval-seed: ## Ingest every evals/corpus.txt ID via the query API (EVAL_CORPUS=, ARXIVLENS_API_URL=); fails if any fails
	@ids="$$(grep -oE '^[0-9]{4}\.[0-9]{4,5}' "$(EVAL_CORPUS)")"; n=$$(printf '%s\n' "$$ids" | grep -c .); \
	if [ "$$n" -eq 0 ]; then echo "::error::eval-seed: no arXiv IDs in $(EVAL_CORPUS)"; exit 1; fi; \
	body="$$(mktemp)"; cerr="$$(mktemp)"; trap 'rm -f "$$body" "$$cerr"' EXIT; \
	printf 'id\tattempts\thttp\tchunks\tseconds\n' > "$(EVAL_SEED_LOG)"; \
	i=0; nf=0; fails=""; streak=0; left=""; \
	for id in $$ids; do i=$$((i + 1)); \
	  if [ "$$streak" -ge 2 ]; then left="$$left $$id"; continue; fi; \
	  t0=$$(date +%s); a=0; ok=0; retry=0; chunks=0; \
	  while [ "$$a" -lt 3 ]; do a=$$((a + 1)); \
	    if [ "$$a" -eq 2 ]; then sleep 10; elif [ "$$a" -eq 3 ]; then sleep 30; fi; \
	    sleep 3; : > "$$body"; \
	    code="$$(curl -sS -m 360 -o "$$body" -w '%{http_code}' -X POST "$(ARXIVLENS_API_URL)/v1/papers/ingest" \
	      -H 'Content-Type: application/json' -d "{\"arxiv_id\":\"$$id\"}" 2>"$$cerr")"; code="$${code:-000}"; \
	    case "$$code" in \
	      2??) retry=0; chunks="$$($(PY) -c 'import json,sys; d=json.load(sys.stdin); c, e = int(d.get("chunks") or 0), int(d.get("embeddings") or 0); \
	             print(c); sys.exit(0 if c > 0 and e > 0 else 1)' < "$$body" 2>/dev/null)" && ok=1; chunks="$${chunks:-0}"; break ;; \
	      000|5??) retry=1; echo "eval-seed: $$id attempt $$a/3: HTTP $$code $$(cat "$$body" "$$cerr" 2>/dev/null | tr '\n' ' ' | head -c 200)" ;; \
	      *) retry=0; break ;; \
	    esac; \
	  done; \
	  printf '%s\t%s\t%s\t%s\t%s\n' "$$id" "$$a" "$$code" "$$chunks" "$$(( $$(date +%s) - t0 ))" >> "$(EVAL_SEED_LOG)"; \
	  if [ "$$ok" = 1 ]; then streak=0; echo "eval-seed: [$$i/$$n] $$id ok ($$chunks chunks, $$a attempt(s))"; \
	  else nf=$$((nf + 1)); msg="$$(cat "$$body" "$$cerr" 2>/dev/null | tr '\n' ' ' | head -c 300)"; \
	    fails="$$fails $$id (HTTP $$code: $$msg);"; echo "eval-seed: [$$i/$$n] $$id FAILED after $$a attempt(s): HTTP $$code $$msg"; \
	    if [ "$$retry" = 1 ]; then streak=$$((streak + 1)); else streak=0; fi; fi; \
	done; \
	if [ -n "$$left" ]; then echo "::error::eval-seed: stopped early, 2 IDs in a row used up their retries (arXiv down or blocking?); not tried:$$left"; fi; \
	if [ "$$nf" -gt 0 ] || [ -n "$$left" ]; then echo "::error::eval-seed: $$nf/$$n failed:$$fails"; exit 1; fi; \
	echo "eval-seed: $$n/$$n ingested"

# ArxivLens harness. Agents and CI use these targets; keep them deterministic and secret-free.
# Run `make help` for the list.

PY      ?= python3
PYTEST  ?= $(PY) -m pytest
export PYTHONPATH := $(CURDIR)

.DEFAULT_GOAL := help
.PHONY: help setup-py setup-fe build build-go build-fe lint lint-go lint-fe \
        test-unit test-go test-py test-fe test-full infra-up infra-down migrate eval

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

# --- setup -------------------------------------------------------------------
setup-py: ## Install Python deps into the active environment
	$(PY) -m pip install -r requirements-m0.txt minio ulid-py fastapi uvicorn

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
test-unit: test-go test-py ## Fast tests, no infra (Go + M0). Frontend tests: make test-fe

test-go: ## Go unit tests
	go test ./...

test-py: ## M0 Python unit tests (downloads BGE model on first run)
	$(PYTEST) -q tests/test_m0.py

test-fe: ## Frontend tests (available after FE-1)
	@cd frontend && if npm run | grep -q " test"; then npm test; else echo "no frontend tests yet (FE-1)"; fi

test-full: ## All milestone suites M0-M6 (needs infra + workers + query service running)
	$(PYTEST) -q tests/

# --- infra -------------------------------------------------------------------
infra-up: ## Start Postgres+pgvector, Kafka (+topics), MinIO
	docker compose up -d

infra-down: ## Stop infra (keeps volumes)
	docker compose down

migrate: build-go ## Apply DB schema
	./bin/arxivlens migrate

# --- quality -----------------------------------------------------------------
eval: ## Retrieval/answer eval; uses the gate from EVAL-1 when present
	@if [ -f scripts/eval_gate.py ]; then $(PY) scripts/eval_gate.py; else $(PY) scripts/evaluate_retrieval.py; fi

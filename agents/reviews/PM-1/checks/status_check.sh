#!/usr/bin/env bash
# Spot-check Product PRD §6 status claims against the code at HEAD.
cd "$(git rev-parse --show-toplevel)"
g() { local label="$1"; shift; local out; out=$(git grep -n -E "$@" 2>/dev/null | head -3); if [ -n "$out" ]; then echo "[FOUND]  $label"; echo "$out" | sed 's/^/           /' | cut -c1-150; else echo "[ABSENT] $label"; fi; }
echo "== ✅ rows: expect FOUND =="
g "ING-1 arXiv 3s politeness"            -i '3 \* time\.Second|3\*time\.Second|time\.Second \* 3' -- internal/crawler
g "ING-2 content-hashed PDF"             -i 'sha256' -- internal/downloader
g "ING-4 on-demand ingest route"         'POST /v1/papers/ingest' -- internal/query/server.go
g "ING-5 discover ingest"                -i 'func .*discover' -- internal/query
g "ING-7 versions as distinct records"   -i 'version' -- migrations/000001_init_schema.up.sql
g "QA-1 RRF fusion"                      -i 'rrf|reciprocal' -- internal/query/search.go
g "QA-2 cross-encoder rerank"            '/internal/rerank' -- internal/query services/embedder/api.py
g "QA-4 SupportThreshold"                'SupportThreshold' -- internal/query/rag.go
g "QA-5 refusal"                         -i "don't know|refus|insufficient" -- internal/query/rag.go
g "QA-6 appendix/supplementary penalty"  -i 'appendix|supplementary' -- internal/query/rag.go
g "QA-7 category filter (API)"           -i 'categor' -- internal/query/server.go internal/query/search.go
g "OPS-1 retry/DLQ"                      -i 'maxRetries|max_retries|dlq' -- internal/kafka/consumer.go
g "OPS-2 --dry-run / --error-type"       -e 'dry-run' -e 'error-type' -- cmd/arxivlens/main.go
g "OPS-3 processed_events ledger"        'processed_events' -- migrations services/common
g "OPS-4 paper status route"             'GET /v1/papers/' -- internal/query/server.go
g "OPS-5 traceparent"                    'traceparent' -- shared/events internal/telemetry
g "UI-1..4 components"                   -l . -- frontend/src/components/HeroSearch.jsx frontend/src/components/PapersLibrary.jsx frontend/src/components/IngestModal.jsx frontend/src/components/InfrastructureModal.jsx
echo
echo "== ◐ UI-5: expect per-citation BibTeX FOUND, saved questions / bulk .bib ABSENT =="
g "UI-5 per-citation BibTeX"             -i 'bibtex' -- frontend/src
g "UI-5 saved questions"                 -i 'saved ?question|savedQuestions' -- frontend/src
g "UI-5 export all as .bib file"         -i '\.bib\b|text/x-bibtex|download=' -- frontend/src
echo
echo "== ⏳ rows: expect ABSENT =="
g "ING-6 scheduler / cron / watermark"   -i 'cron|ticker|watermark|schedule' -- cmd internal services
g "QA-9 streaming answers"               -i 'text/event-stream|http\.Flusher|EventSource|StreamingResponse' -- cmd internal services frontend/src
g "QA-8 LLM synthesis client"            -i 'anthropic|openai|ollama|chat/completions' -- cmd internal services
g "OPS-6 Prometheus / metrics dashboard" -i 'prometheus|promhttp|/metrics' -- cmd internal services frontend/src docker-compose.yml

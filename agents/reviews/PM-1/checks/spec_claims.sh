cd "$(git rev-parse --show-toplevel)"
echo "## OPS-H5 / Tech PRD §5: no migration creates the HNSW index"
echo "\$ grep -rni hnsw migrations/"; grep -rni hnsw migrations/ || echo "(no matches)"
echo; echo "\$ grep -n 'hnsw' scripts/benchmark_hnsw.py | grep -i 'create index\|ef_construction'"
grep -n -i 'create index\|ef_construction *=' scripts/benchmark_hnsw.py | head -4
echo; echo "## OPS-H5: 'arxivlens migrate' runs a single file"
grep -n 'RunMigrations\|migrationFile\|000001' cmd/arxivlens/main.go internal/db/db.go | head -6
echo; echo "## OPS-H5: named M5/M6 test exists"
grep -n 'def test_pgvector_hnsw_index_presence_and_querying' tests/test_m5_m6_telemetry_eval.py
echo; echo "## AI-2: fallback marks citation supported (rag.go)"
grep -n 'score = 0.75\|supported = true' internal/query/rag.go
echo; echo "## AI-3: one EmbedTexts call per hit and per cited sentence"
grep -n 'EmbedTexts' internal/query/rag.go
echo; echo "## EVAL-2: PR #2 eval rerank mode is a bi-encoder, production uses /internal/rerank"
git show origin/agent/ai/EVAL-1:scripts/evaluate_retrieval.py | grep -n -i 'rerank' | head -6
grep -n '/internal/rerank' internal/query/client.go
echo; echo "## AI-1 excludes behaviour changes (AI-2 rationale)"
grep -n -i 'out of scope\|behaviour\|behavior' agents/tasks/AI-1.md | head -4
echo; echo "## Ownership of files named in new specs' Scope (AGENTS.md §1)"
echo "OPS-H4 (backend): .gitignore, Makefile | OPS-H5 (backend): migrations/, internal/db, cmd/ | AI-2/AI-3 (ai): internal/query/rag.go(+_test) | EVAL-2 (ai): scripts/, evals/, tests/"
grep -n 'ai-engineer\|backend-engineer' AGENTS.md | head -4 | cut -c1-200

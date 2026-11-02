#!/usr/bin/env bash
# Ownership check for PRs from the AI agent team (branches agent/<role>/<task>).
# Each role may only touch its own code area, and no planning/review/agent files may enter this repo:
# tickets, PRDs, ADRs, QA evidence and agent rules live in the arxivlens-agents repo.
# Usage: .github/scripts/ownership-check.sh <base-ref> <head-branch-name>
set -euo pipefail

BASE="${1:?base ref, e.g. origin/main}"
BRANCH="${2:?head branch name}"
ROLE="$(echo "$BRANCH" | cut -d/ -f2)"

COMMON='^(README\.md$)'
case "$ROLE" in
  ai)       ALLOW='^(services/embedder/[^/]+\.py$|internal/query/rag(_test)?\.go$|scripts/|evals/|tests/)' ;;
  backend)  ALLOW='^(cmd/|internal/|services/(processor|common)/|services/embedder/Dockerfile$|shared/|migrations/|tests/|docker-compose\.yml$|Makefile$|\.github/|requirements[^/]*\.txt$|\.gitignore$|go\.(mod|sum)$|CONTRIBUTING\.md$)' ;;
  frontend) ALLOW='^frontend/' ;;
  pm|qa)    echo "::error::role '$ROLE' does not open code PRs in this repo (its work lives in arxivlens-agents)"; exit 1 ;;
  *) echo "Unknown role '$ROLE' in branch '$BRANCH' (expected agent/<ai|backend|frontend>/<TASK-ID>)"; exit 1 ;;
esac

DENY_BACKEND_RAG='^internal/query/rag(_test)?\.go$'
NOT_HERE='^(agents/|docs/)|(^|/)\.claude/|(^|/)(AGENTS|CLAUDE)\.md$'
FORBIDDEN='(^|/)\.env($|\.(local|prod|production|dev|development|test))|\.pdf$|\.(pt|bin|safetensors)$|(^|/)node_modules/'

fail=0
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if echo "$f" | grep -Eq "$NOT_HERE"; then
    echo "::error file=$f::planning/review/agent files belong in arxivlens-agents, not in this repo"; fail=1; continue
  fi
  if echo "$f" | grep -Eq "$FORBIDDEN"; then
    echo "::error file=$f::forbidden file type committed"; fail=1; continue
  fi
  if [ "$ROLE" = "backend" ] && echo "$f" | grep -Eq "$DENY_BACKEND_RAG"; then
    echo "::error file=$f::rag.go is owned by ai-engineer"; fail=1; continue
  fi
  if ! echo "$f" | grep -Eq "$ALLOW" && ! echo "$f" | grep -Eq "$COMMON"; then
    echo "::error file=$f::outside $ROLE's code area"; fail=1
  fi
done <<< "$(git -c core.quotePath=false diff --no-renames --name-only "$BASE"...HEAD)"
# --no-renames: a moved file reports both its old and new path, so moving code out of another area is caught.

lines="$(git diff --no-renames --shortstat "$BASE"...HEAD | { grep -Eo '[0-9]+ (insertion|deletion)' || true; } | awk '{s+=$1} END {print s+0}')"
if [ "$lines" -gt 400 ]; then
  echo "::warning::$lines changed lines (> 400). Consider splitting the change."
fi

[ "$fail" -eq 0 ] && echo "Ownership OK for role=$ROLE ($lines changed lines)"
exit "$fail"

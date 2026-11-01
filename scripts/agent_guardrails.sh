#!/usr/bin/env bash
# Fails an agent PR that edits files outside its role's owned paths (see AGENTS.md §1).
# Usage: scripts/agent_guardrails.sh <base-ref> <head-branch-name>
# Branch convention: agent/<role>/<TASK-ID>, role in {ai, backend, frontend, pm, qa}.
set -euo pipefail

BASE="${1:?base ref, e.g. origin/main}"
BRANCH="${2:?head branch name}"
ROLE="$(echo "$BRANCH" | cut -d/ -f2)"

# Paths every role may touch: shared memory and PRD status columns.
COMMON='^(agents/|docs/PRD-(Product|Technical)\.md$|README\.md$)'

case "$ROLE" in
  ai)       ALLOW='^(services/embedder/(api|worker)\.py$|internal/query/rag(_test)?\.go$|scripts/|evals/|tests/)' ;;
  backend)  ALLOW='^(cmd/|internal/|services/(processor|common)/|services/embedder/Dockerfile$|services/AGENTS\.md$|shared/|migrations/|tests/|docker-compose\.yml$|Makefile$|\.github/|requirements[^/]*\.txt$|\.gitignore$|go\.(mod|sum)$|scripts/agent_guardrails\.sh$)' ;;
  frontend) ALLOW='^frontend/' ;;
  qa)       ALLOW='^agents/reviews/' ;;
  pm)       ALLOW='^(docs/|AGENTS\.md$|CLAUDE\.md$|[a-z]+/AGENTS\.md$|\.claude/)' ;;
  *) echo "Unknown role '$ROLE' in branch '$BRANCH' (expected agent/<ai|backend|frontend|pm|qa>/<TASK-ID>)"; exit 1 ;;
esac

# rag.go belongs to the AI engineer even though it lives under internal/.
DENY_BACKEND_RAG='^internal/query/rag(_test)?\.go$'
# Never committed by anyone.
FORBIDDEN='(^|/)\.env($|\.)|\.pdf$|\.(pt|bin|safetensors)$|(^|/)node_modules/'

fail=0
changed="$(git diff --name-only "$BASE"...HEAD)"
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if echo "$f" | grep -Eq "$FORBIDDEN"; then
    echo "::error file=$f::forbidden file type committed"; fail=1; continue
  fi
  if [ "$ROLE" = "backend" ] && echo "$f" | grep -Eq "$DENY_BACKEND_RAG"; then
    echo "::error file=$f::rag.go is owned by ai-engineer"; fail=1; continue
  fi
  if ! echo "$f" | grep -Eq "$ALLOW" && ! echo "$f" | grep -Eq "$COMMON"; then
    echo "::error file=$f::outside $ROLE's owned paths (AGENTS.md §1)"; fail=1
  fi
done <<< "$changed"

# Only the human owner accepts or rejects ADRs.
if git diff "$BASE"...HEAD -- docs/adr/ | grep -Eq '^\+.*\*\*Status:\*\* *(Accepted|Rejected)'; then
  echo "::error::agents may not set an ADR to Accepted/Rejected; only the human owner does"; fail=1
fi

lines="$(git diff --shortstat "$BASE"...HEAD | grep -Eo '[0-9]+ (insertion|deletion)' | awk '{s+=$1} END {print s+0}')"
if [ "$lines" -gt 400 ]; then
  echo "::warning::$lines changed lines (> 400). Consider splitting the task."
fi

[ "$fail" -eq 0 ] && echo "Guardrails OK for role=$ROLE ($lines changed lines)"
exit "$fail"

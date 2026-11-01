cd "$(git rev-parse --show-toplevel)"
echo "## Product PRD §6 QA-4 row at HEAD (status column + acceptance/task column):"
git show HEAD:docs/PRD-Product.md | grep -n '^| QA-4 '
git show HEAD:docs/PRD-Product.md | grep '^| QA-4 ' | python3 -c 'import sys,unicodedata; c=[x.strip() for x in sys.stdin.read().strip().strip("|").split("|")]; print("   status cell =", [unicodedata.name(ch) for ch in c[3]], "| acceptance/task cell =", repr(c[4]))'
echo; echo "## internal/query/rag.go:142-154 at HEAD (support check):"
sed -n 142,154p internal/query/rag.go | expand -t4 | nl -ba -v142
echo; echo "## The PR's own AI-2 spec on QA-4 (agents/tasks/AI-2.md, Goal):"
grep -n 'QA-4 requires\|breaks the core trust' agents/tasks/AI-2.md
echo; echo "## Where the PR links AI-2 from the PRDs:"
git grep -n 'AI-2' HEAD -- docs/PRD-Product.md docs/PRD-Technical.md | cut -c1-200

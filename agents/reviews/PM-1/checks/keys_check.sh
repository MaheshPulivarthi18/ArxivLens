cd "$(git rev-parse --show-toplevel)"
echo "# Gate keys cited in Product PRD §8 (PM-1 HEAD) vs evals/thresholds.json on PR #2 (origin/agent/ai/EVAL-1)"
echo "## evals/thresholds.json @ PR #2:"; git show origin/agent/ai/EVAL-1:evals/thresholds.json
echo; echo "## key-by-key:"
for k in $(git show HEAD:docs/PRD-Product.md | grep -oE '\(`[a-z_0-9]+`\)' | tr -d '()`' | sort -u); do
  if git show origin/agent/ai/EVAL-1:evals/thresholds.json | grep -q "\"$k\""; then echo "  $k  -> present in thresholds.json"; else echo "  $k  -> NOT in thresholds.json"; fi
done
echo; echo "## gated mode name used by PR #2 gate:"
git show origin/agent/ai/EVAL-1:scripts/eval_gate.py | grep -n 'Hybrid RRF + Rerank' | head -3
echo; echo "## Makefile 'eval' target @ PM-1 HEAD (fallback claim in §8):"
grep -n -A1 '^eval:' Makefile

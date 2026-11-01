cd "$(git rev-parse --show-toplevel)"
echo "# New task specs added by this PR: required fields/sections present? (✓ = present)"
NEW=$(git diff --name-only --diff-filter=A origin/main...HEAD -- agents/tasks)
printf "%-24s" "field"; for f in $NEW; do printf "%-9s" "$(basename $f .md)"; done; echo
for k in '\*\*Owner\*\*' '\*\*Priority\*\*' '\*\*PRD ref\*\*' '\*\*Depends on\*\*' '\*\*Status\*\*' '^## Goal' '^## Scope' 'Out of scope' '^## Acceptance criteria' '^## Verification' '^## Notes'; do
  printf "%-24s" "$(echo $k | sed 's/\\\*//g;s/\^//')"; for f in $NEW; do grep -qE "$k" $f && printf "%-9s" "✓" || printf "%-9s" "MISSING"; done; echo
done
printf "%-24s" "# of ACs"; for f in $NEW; do printf "%-9s" "$(grep -cE '^- \[ \] AC[0-9]' $f)"; done; echo
echo; echo "# Owner / Depends on / Status lines"
for f in $NEW; do echo "$(basename $f .md): $(grep -E '\*\*Owner\*\*' $f | cut -d'|' -f3 | xargs) | deps: $(grep -E '\*\*Depends on\*\*' $f | cut -d'|' -f3 | xargs) | $(grep -E '\*\*Status\*\*' $f | cut -d'|' -f3 | xargs)"; done
echo; echo "# Dependencies named in specs resolve to a task file?"
for d in AI-1 OPS-H1 EVAL-1; do [ -f agents/tasks/$d.md ] && echo "  $d -> agents/tasks/$d.md exists" || echo "  $d -> MISSING"; done

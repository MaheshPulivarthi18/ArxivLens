#!/usr/bin/env bash
# Frontend dist/ report: prints font and CSS facts about a built frontend into the CI job log.
# It prints 7 kinds of line, always in this order:
#   DIST woff2 files: <n>                  (.woff2 files under <dist>/assets)
#   DIST woff2: <name>                     (one per .woff2, sorted; none when n = 0)
#   DIST css @font-face rules: <n>         (occurrences across <dist>/assets/*.css)
#   DIST css .font-serif: yes|no
#   DIST css .prose: yes|no
#   DIST google font hosts: <n>            (fonts.googleapis.com / fonts.gstatic.com in any file under <dist>)
#   DIST google font host files: <paths>   (comma-separated, relative to <dist>, or "none")
# Report-only: it exits 0 whatever the counts are, and non-zero only if <dist> or <dist>/index.html is missing.
# Why: agent QA sandboxes can't run `npm run build`, so this puts the dist/ evidence (self-hosted fonts, no Google
# font hosts, built CSS classes) where QA can read it (FE-0 design Q3). Turning it into a gate is a separate decision.
# Usage: .github/scripts/frontend-dist-report.sh <dist-dir>
set -euo pipefail

DIST="${1:?dist directory, e.g. frontend/dist}"
if [ ! -d "$DIST" ]; then
  echo "::error::dist directory '$DIST' does not exist" >&2; exit 1
fi
if [ ! -f "$DIST/index.html" ]; then
  echo "::error::'$DIST' has no index.html" >&2; exit 1
fi
DIST="${DIST%/}"
ASSETS="$DIST/assets"

# woff2 files (names only, sorted).
woff2=()
if [ -d "$ASSETS" ]; then
  while IFS= read -r f; do
    [ -n "$f" ] && woff2+=("$f")
  done < <(find "$ASSETS" -type f -name '*.woff2' -exec basename {} \; | LC_ALL=C sort)
fi
echo "DIST woff2 files: ${#woff2[@]}"
for f in "${woff2[@]}"; do
  echo "DIST woff2: $f"
done

# Built CSS: concatenate every top-level assets/*.css (empty if none).
css=""
if [ -d "$ASSETS" ]; then
  while IFS= read -r -d '' f; do
    css+="$(cat "$f")"$'\n'
  done < <(find "$ASSETS" -maxdepth 1 -type f -name '*.css' -print0)
fi
fontface="$(printf '%s' "$css" | { grep -o '@font-face' || true; } | wc -l | tr -d ' ')"
echo "DIST css @font-face rules: $fontface"
has() { if grep -qF -- "$1" <<< "$css"; then echo yes; else echo no; fi; }  # here-string: no pipe for -q to break
echo "DIST css .font-serif: $(has '.font-serif')"
echo "DIST css .prose: $(has '.prose')"

# Google font hosts in any file under dist (html, css, js, ...).
HOSTS='fonts\.(googleapis|gstatic)\.com'
hosts="$({ grep -rEaoh -- "$HOSTS" "$DIST" || true; } | wc -l | tr -d ' ')"
echo "DIST google font hosts: $hosts"
files="$(cd "$DIST" && { grep -rEal -- "$HOSTS" . || true; } | sed 's|^\./||' | LC_ALL=C sort | paste -sd, -)"
echo "DIST google font host files: ${files:-none}"
exit 0

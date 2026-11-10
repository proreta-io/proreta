#!/usr/bin/env bash
# Fails when a tracked public file cites an internal record: an ADR number or a work-item id
# (a letter, a dash and two digits). State the rule in the comment instead of pointing at a label.
set -euo pipefail

cd "$(dirname "$0")/.."

if git rev-parse --git-dir >/dev/null 2>&1 && [ -n "$(git ls-files | head -n 1)" ]; then
  files=$(git ls-files -z | tr '\0' '\n')
else
  files=$(find . -type f -not -path './.git/*' -not -path '*/node_modules/*' -not -path '*/dist/*' | sed 's|^\./||')
fi

# Generated code and lockfiles carry hashes and third-party text; this script names the patterns.
files=$(printf '%s\n' "$files" | grep -vE '(^|/)(package-lock\.json|go\.sum|check-internal-refs\.sh)$|\.gen\.go$|\.sql\.go$|(^|/)generated/|\.(png|jpe?g|gif|ico|woff2?|ttf|otf|webp|svg|pdf|tgz)$' || true)

hits=""
while IFS= read -r f; do
  [ -f "$f" ] || continue
  out=$(grep -nIE 'ADR-[0-9]{4}|(^|[^A-Za-z0-9_-])[A-Z]-[0-9]{2}([^A-Za-z0-9_-]|$)|\b[LR]-[0-9]{3}\b' "$f" 2>/dev/null \
    | grep -vE '(UTF|ISO|SHA|TLS|AES|ECDSA|RSA|HS|RS|ES|PS|CRC)-[0-9]' | sed "s|^|$f:|" || true)
  [ -n "$out" ] && hits="${hits}${out}"$'\n'
done <<<"$files"

if [ -n "$hits" ]; then
  echo "internal record references (state the rule instead of citing a label):" >&2
  printf '%s' "$hits" >&2
  exit 1
fi
echo "check-internal-refs: ok"

#!/usr/bin/env bash
# Run a test command, keep its output in a file and, when it fails, turn the failing tests into
# GitHub annotations (the job log is not readable without a sign-in). Usage: ci-test.sh <output-file> <command...>
set -uo pipefail
out=$1
shift
"$@" 2>&1 | tee "$out"
code=${PIPESTATUS[0]}
[ "$code" -eq 0 ] && exit 0
# Workflow command data: % \r \n must be escaped.
esc() { awk '{ gsub(/%/, "%25"); gsub(/\r/, "%0D"); printf "%s%s", (NR > 1 ? "%0A" : ""), $0 }'; }
pat='^[[:space:]]*(--- FAIL|FAIL|panic:|fatal error|WARNING: DATA RACE)'
echo "::group::failing tests"
grep -nE "$pat" "$out" | head -30 | while IFS= read -r l; do
  printf '::error title=%s::%s\n' "go test" "$(printf '%s' "$l" | esc)"
done
first=$(grep -nE "$pat" "$out" | head -1 | cut -d: -f1)
if [ -n "$first" ]; then
  printf '::error title=%s::%s\n' "go test: first failure with context" "$(sed -n "${first},$((first + 15))p" "$out" | esc)"
else
  printf '::error title=%s::%s\n' "go test" "exit code $code, no failing test line found; last output: $(tail -n 15 "$out" | esc)"
fi
echo "::endgroup::"
exit "$code"

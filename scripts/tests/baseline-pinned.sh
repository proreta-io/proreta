#!/usr/bin/env bash
# Tests scripts/check-baseline-pinned.sh against a throwaway migrations directory.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
f=20261119111332_baseline.sql
echo "create table a();" > "$tmp/$f"
printf '# header\n' > "$tmp/released.sha256"

failures=0
# expect <ok|fail> <name> <version>
expect() {
  local want=$1 name=$2 rc=0
  bash "$root/scripts/check-baseline-pinned.sh" "$3" "$tmp" >/dev/null 2>&1 || rc=$?
  if { [ "$want" = ok ] && [ "$rc" != 0 ]; } || { [ "$want" = fail ] && [ "$rc" = 0 ]; }; then
    echo "FAIL: $name (exit $rc)"; failures=$((failures + 1))
  else
    echo "ok: $name"
  fi
}

expect fail "final tag, baseline not pinned" v1.0.0
expect ok "rc tag, baseline not pinned" v1.0.0-rc.1
expect ok "other suffix, baseline not pinned" 1.0.0-beta.2
(cd "$tmp" && shasum -a 256 "$f" >> released.sha256)
expect ok "final tag, baseline pinned" v1.0.0
echo "create table b();" > "$tmp/$f"
expect fail "final tag, baseline edited after pin" v1.0.0
[ "$failures" = 0 ]

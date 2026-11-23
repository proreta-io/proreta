#!/usr/bin/env bash
# Tests scripts/check-release-notes.sh against a throwaway git repository with two tags.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
out=$(mktemp)
trap 'rm -rf "$tmp" "$out"' EXIT
cd "$tmp"
git init -q .
git config user.email t@example.com
git config user.name t
mkdir -p scripts docs/release-notes backend/migrations
cp "$root/scripts/check-release-notes.sh" scripts/

echo "create table a();" > backend/migrations/20260101000000_baseline.sql
git add -A && git commit -qm one && git tag v1.0.0
prev=v1.0.0

failures=0
# expect <ok|fail> <name> <version> <previous>
expect() {
  local want=$1 name=$2 rc=0
  shift 2
  bash scripts/check-release-notes.sh "$@" >"$out" 2>&1 || rc=$?
  if { [ "$want" = ok ] && [ "$rc" != 0 ]; } || { [ "$want" = fail ] && [ "$rc" = 0 ]; }; then
    echo "FAIL: $name (exit $rc)"; cat "$out"; failures=$((failures + 1))
  else
    echo "ok: $name"
  fi
}
notes() { mkdir -p docs/release-notes; printf '%s\n' "$@" > docs/release-notes/1.1.0.md; }

# no change since the previous tag: no heading needed, notes only have to exist
notes "# 1.1.0"
git add -A && git commit -qm notes
expect ok "no migration change, no heading" 1.1.0 $prev
rm docs/release-notes/1.1.0.md
expect fail "stable tag without notes" 1.1.0 $prev
expect ok "pre-release without notes" 1.1.0-rc.1 $prev

# a new migration needs the heading
echo "alter table a add column b int;" > backend/migrations/20260102000000_b.sql
notes "# 1.1.0"
git add -A && git commit -qm migration
expect fail "new migration without heading" 1.1.0 $prev
notes "# 1.1.0" "## Migrations" "One new migration."
expect ok "new migration with heading" v1.1.0 $prev
notes "# 1.1.0" "### Migrations"
expect fail "heading of the wrong level" 1.1.0 $prev

# changed released.sha256 counts, a changed test file does not
git checkout -qf -b other v1.0.0
notes "# 1.1.0"
echo "x  y.sql" > backend/migrations/released.sha256
git add -A && git commit -qm hash
expect fail "released.sha256 changed" 1.1.0 $prev
git checkout -qf -b tests v1.0.0
notes "# 1.1.0"
echo "package x" > backend/migrations/a_test.go
git add -A && git commit -qm gotest
expect ok "only a test file changed" 1.1.0 $prev

# a changed baseline needs the wording
git checkout -qf -b base v1.0.0
echo "create table c();" > backend/migrations/20260101000000_baseline.sql
notes "# 1.1.0" "## Migrations" "Changed."
git add -A && git commit -qm baseline
expect fail "baseline changed, no wording" 1.1.0 $prev
notes "# 1.1.0" "## Migrations" "The baseline is replaced." "Install as a new installation."
expect ok "baseline changed, wording present" 1.1.0 $prev
notes "# 1.1.0" "## Migrations" "The baseline is replaced; reinstall."
expect ok "baseline changed, reinstall wording" 1.1.0 $prev
notes "# 1.1.0" "## Migrations" "Please reinstall."
expect fail "baseline changed, word baseline missing" 1.1.0 $prev

# first release: no previous tag
notes "# 1.1.0"
expect fail "none: heading and wording required" 1.1.0 none
notes "# 1.1.0" "## Migrations" "A new installation starts from the baseline."
expect ok "none: heading and wording present" 1.1.0 none
expect fail "unknown previous tag" 1.1.0 v0.0.1

[ "$failures" = 0 ] && echo "release-notes tests: ok"
exit "$failures"

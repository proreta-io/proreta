#!/usr/bin/env bash
# Usage: check-release-notes.sh <version> <previous-tag|none>   (run from a checkout of the release tag)
# Fails when the release notes docs/release-notes/<version>.md are missing (stable versions), lack a
# `## Migrations` heading although the migrations changed since <previous-tag>, or do not say
# `baseline` and `new installation`/`reinstall` although the baseline migration changed.
# <previous-tag> is the tag before this one (a 0.x tag counts: the baseline reset shows as a changed
# baseline file); `none` means there is no earlier tag, so everything counts as new.
# A pre-release (X.Y.Z-rc.N) without notes only warns.
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <version> <previous-tag|none>" >&2; exit 2; }
version="${1#v}"
prev="$2"
cd "$(dirname "$0")/.."

notes="docs/release-notes/${version}.md"
mig=backend/migrations
fail=0
err() { echo "::error::$*"; fail=1; }

if [ ! -f "$notes" ]; then
  if [[ "$version" == *-* ]]; then
    echo "::warning::pre-release $version has no $notes; checks skipped"
    exit 0
  fi
  err "$notes is missing: copy docs/release-notes/X.Y.Z.md and fill it in"
  exit 1
fi

if [ "$prev" != none ]; then
  git rev-parse -q --verify "refs/tags/${prev}^{commit}" >/dev/null \
    || { echo "::error::previous tag $prev not found" >&2; exit 1; }
fi

changed=0
if [ "$prev" = none ]; then
  changed=1
elif ! git diff --quiet "$prev" HEAD -- "$mig" ':!*_test.go' ':!*/testdata/*'; then
  changed=1
fi
if [ "$changed" = 1 ] && ! grep -qE '^## Migrations[[:space:]]*$' "$notes"; then
  err "$notes needs a '## Migrations' heading: $mig changed since $prev (write 'No migrations.' only if none ships)"
fi

baseline=""
for f in "$mig"/*_baseline.sql; do [ -f "$f" ] && baseline=$f; done
if [ -n "$baseline" ]; then
  new_hash=$(sha256sum "$baseline" | cut -d' ' -f1)
  old_hash=none
  if [ "$prev" != none ]; then
    old_hash=$(git show "$prev:$baseline" 2>/dev/null | sha256sum | cut -d' ' -f1) || old_hash=none
    # an empty blob (file absent in the previous tag) is a different baseline too
    git cat-file -e "$prev:$baseline" 2>/dev/null || old_hash=none
  fi
  if [ "$new_hash" != "$old_hash" ]; then
    grep -qi 'baseline' "$notes" || err "$notes must mention the 'baseline': it differs from $prev"
    grep -qiE 'new installation|reinstall' "$notes" \
      || err "$notes must say 'new installation' or 'reinstall': the baseline differs from $prev, so there is no upgrade"
  fi
fi

[ "$fail" = 0 ] && echo "check-release-notes: ok ($version, previous $prev)"
exit "$fail"

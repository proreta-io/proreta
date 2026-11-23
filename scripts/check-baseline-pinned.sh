#!/usr/bin/env bash
# Usage: check-baseline-pinned.sh <version> [migrations dir]
# A final release (no -rc or other suffix) must ship a baseline whose SHA-256 is in released.sha256,
# so the freeze is armed before the tag is cut. A pre-release passes without it: the owner appends the
# hash last, for the final tag (docs/runbooks/migrations.md).
set -euo pipefail

version=${1:?usage: check-baseline-pinned.sh <version> [migrations dir]}
dir=${2:-$(dirname "$0")/../backend/migrations}
baseline=20261119111332_baseline.sql

version=${version#v}
if [[ "$version" == *-* ]]; then
  echo "pre-release $version: baseline pin not required"
  exit 0
fi
sum=$(shasum -a 256 "$dir/$baseline" | awk '{print $1}')
if ! grep -qx "$sum  $baseline" "$dir/released.sha256"; then
  echo "::error::final release $version: $baseline ($sum) is not pinned in backend/migrations/released.sha256; append it (see the header of that file), merge, then tag again"
  exit 1
fi
echo "baseline pinned"

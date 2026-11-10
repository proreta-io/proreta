#!/usr/bin/env bash
# Fails when a TODO/FIXME in source does not reference a GitHub issue, e.g. TODO(#123) or FIXME(#45).
set -euo pipefail

cd "$(dirname "$0")/.."

bad=$(grep -rnE '\b(TODO|FIXME)\b' \
    --include='*.go' --include='*.ts' --include='*.tsx' --include='*.js' --include='*.sql' \
    --include='*.yaml' --include='*.yml' --include='*.sh' --include='Dockerfile' --include='Makefile' \
    --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=gen --exclude-dir=docs \
    --exclude='check-todos.sh' --exclude='*.gen.go' --exclude='*.sql.go' \
    backend api web deploy scripts .github 2>/dev/null \
  | grep -vE '\b(TODO|FIXME)\(#[0-9]+\)' || true)

if [ -n "$bad" ]; then
  echo "TODO/FIXME without an issue reference (open a GitHub issue and write TODO(#<number>)):" >&2
  echo "$bad" >&2
  exit 1
fi
echo "check-todos: ok"

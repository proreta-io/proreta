<!--
The PR title and description become the squash commit message on main.
Title: conventional style, `type(scope): summary` (feat, fix, docs, refactor, test, ci, chore, build).
Write plain text only: no AI attribution (no trailer or line naming an AI model or tool, no
"Generated with" lines) and no references to internal records (decision numbers, ticket ids).
Co-Authored-By trailers for human collaborators are fine. Remove this comment before submitting.
-->

## What and why

## How it was verified
<!-- Commands you ran, tests added, what you checked by hand. -->

## Migrations
- [ ] No migration in this PR
- [ ] Migration included: forward-only, works with the previous application version (expand/contract), starts with `SET LOCAL lock_timeout`, rules in CONTRIBUTING.md

## Breaking API change
- [ ] No change to `api/openapi.yaml` that breaks existing clients
- [ ] Breaking change: documented in the description, entry in `api/oasdiff-err-ignore.txt` with its reason

## Checklist
- [ ] Lint and tests pass locally (`make lint test`; web: `npm run lint && npm run knip && npm test`)
- [ ] Generated code is up to date (`make generate`, `npm run gen:api`) after spec or query changes
- [ ] Every new TODO references an issue: `TODO(#123)`
- [ ] Operator docs under `docs/` updated if behaviour changed

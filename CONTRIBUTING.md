# Contributing to Proreta

Thank you for your interest. Proreta is in early development; before larger changes, please open an issue to discuss the approach.

**License of contributions:** Proreta is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution you agree that it is licensed under the same terms (section 5 of the license). There is no CLA and no sign-off requirement.

## Development setup

Requirements:
- Go (the version in `backend/go.mod`).
- Node.js 22 (`web/.nvmrc`) and npm.
- PostgreSQL 15 or newer for the integration tests (a throwaway local instance or a container).
- Optional: `golangci-lint` v2.15.0 (the version CI uses), `helm`, `kubeconform`, Python 3 with PyYAML (chart checks).

Code generators (sqlc, oapi-codegen) are pinned in `backend/tools/go.mod` and run with `go tool`; nothing else needs a global install.

### Backend

```sh
make build            # binaries into backend/bin/
make test             # go test -race -count=1 ./... (integration tests skip without a database)
make test-e2e         # end-to-end test with the proretatest build tag (needs a database)
make lint             # golangci-lint run
make generate         # regenerate sqlc and oapi-codegen output
make check-generated  # generate, then fail if the committed code differs
```

Integration tests run when `TEST_DATABASE_ADMIN_URL` points at a superuser of a scratch PostgreSQL server; they create and drop their own databases and roles. Never point it at a server that holds data you care about.

```sh
docker run --rm -d --name proreta-pg -e POSTGRES_PASSWORD=postgres -p 55432:5432 postgres:16
export TEST_DATABASE_ADMIN_URL='postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable'
make test test-e2e
```

### Web

```sh
cd web
npm ci
npm run dev           # dev server on :5173, /api proxied to PRORETA_API_TARGET (default http://localhost:8080)
npm run format:check && npm run lint && npm run typecheck && npm test && npm run build
npm run gen:api       # regenerate src/api/generated from ../api/openapi.yaml
```

More in [web/README.md](web/README.md).

### Helm chart

```sh
bash scripts/validate-chart.sh   # helm lint and template for every value combination, invariants, kubeconform
```

## How the code is organised

- **Package by feature** under `backend/internal/<domain>`. Each table has one owning module; other modules change it only through the owner's service. Read-only cross-table queries for the dashboard live in `internal/queries`.
- **Interfaces only at real seams** (prober, notifier, result store and similar), declared on the consumer side. Dependencies are wired by hand in each `main.go`; there is no DI framework.
- **Tenant isolation:** all tenant data access goes through `db.WithTenantTx`, which sets `app.tenant_id` with `SET LOCAL`; row-level security is enforced in the database. Never use the connection pool directly for tenant data. Every tenant-scoped table has `tenant_id`, and references use composite `(tenant_id, id)` keys.
- **Identifiers and types:** IDs are UUIDv7 generated in the application, timestamps are `timestamptz` in UTC, enums are `text` with a `CHECK`. Secrets stored in the database are encrypted with AES-256-GCM and carry a key id for rotation.
- **Outbox:** a state change and its notification are one transaction (incident transitions write `notification_deliveries`); delivery happens later in `jobs`.
- **Spec first:** change `api/openapi.yaml` first, regenerate (`make generate`, `npm run gen:api`), then implement. Errors are RFC 9457 `application/problem+json`, mapped in one place. CI runs spectral and `oasdiff breaking` against the base branch; an intentional breaking change goes into `api/oasdiff-err-ignore.txt` with its reason.
- **SSRF defence lives in code** (`internal/safehttp`): connect-time IP validation, blocked ranges, redirect re-validation, no proxy from the environment, no response body returned to users. Do not weaken it without a documented decision.
- **Database roles:** runtime roles are never owners and never `BYPASSRLS`; the worker claims jobs through `SECURITY DEFINER` functions.
- **Configuration** comes from `PRORETA_*` environment variables, validated at startup (fail fast). Never log secret values.
- **Logs and metrics:** `slog` JSON with `request_id` and `tenant_id`; Prometheus metrics on the admin port (`:9090`), never routed externally. A new metric must be documented in `docs/runbooks/observability.md` (a test checks this).

## Database migrations

Migrations are SQL files for goose in `backend/migrations`, embedded in the `migrate` binary and applied by the chart's migration Job as `proreta_owner`. The schema starts from one baseline migration; details, the schema snapshot and the rules for the next migration: [docs/runbooks/migrations.md](docs/runbooks/migrations.md).

- Forward-only: no `+goose Down`. File names are UTC timestamps: `YYYYMMDDHHMMSS_snake_case.sql`.
- Expand/contract: a migration must work with the previous application version.
- Start every migration with `SET LOCAL lock_timeout = '5s';`.
- Large indexes: `CREATE INDEX CONCURRENTLY` in a `-- +goose NO TRANSACTION` migration.
- New tenant tables: `tenant_id`, `ENABLE` and `FORCE ROW LEVEL SECURITY`, a policy on `app.tenant_id`, composite keys. The schema-lint integration test fails on a missing policy.
- The database and its roles are created by `proretactl db bootstrap`, never by migrations.
- Never edit a migration that a release has shipped; write a new one.
- A schema change also updates the catalog snapshot `backend/migrations/testdata/schema.sql` (`go test ./migrations -run TestIntegrationSchemaSnapshot -update`).

## Tests

- Unit tests for pure logic (the incident state machine is test-driven).
- Integration tests against a real PostgreSQL (`TEST_DATABASE_ADMIN_URL`, skipped when unset).
- Handler tests against the generated server.
- Web: Vitest with Testing Library; `fetch` is mocked, no network.

## Workflow

`main` changes only through pull requests, squash-merged.

- **Branches:** one branch per change with a short descriptive name (for example `fix-probe-timeout` or `feat/status-page-title`); an issue number may prefix it: `123-fix-timeout`. Branch from the current `main`.
- **Small pull requests:** one vertical slice per pull request; split unrelated changes.
- **Title and description:** the pull request title and description become the squash commit message on `main`. Use the conventional style `type(scope): summary` (`feat`, `fix`, `docs`, `refactor`, `test`, `ci`, `chore`, `build`), with a short description of what and why. Plain text only, no internal record ids. A `Co-Authored-By` trailer crediting a human collaborator is welcome; AI attribution is not allowed in titles, descriptions or commit messages: no trailer naming an AI model or tool, no generated-with lines. Individual commit messages inside the branch are discarded by the squash, but keep them plain too.
- **CI must be green:** the required check is `ci-ok`, an aggregate of every CI job (jobs skipped because a change does not touch their area count as passed). The branch must be up to date with `main`; when it is behind, rebase onto `main` or merge `main` into the branch and push again.
- **Merge:** squash-merge only; the head branch is deleted after the merge. Auto-merge may be enabled once the checks are required and green. Only `release` (container images) runs after the merge; tests run on the pull request, not again on `main`.
- **Update the documentation** under `docs/` when operator-visible behaviour changes.
- Comments explain the non-obvious *why*. Code and documentation are written in English.

### Run the same checks locally

```sh
make check-generated test lint     # backend: generated code, tests, golangci-lint (make test-e2e needs a database)
cd web && npm ci && npm run gen:api && npm run format:check && npm run lint && npm run typecheck && npm test && npm run build
bash scripts/validate-chart.sh     # chart, when deploy/helm/ changed
bash scripts/check-todos.sh        # TODO format
bash scripts/check-internal-refs.sh  # no internal record ids
actionlint                         # workflows, when .github/workflows/ changed
```

### Pull requests with a migration

- Say so in the description (the template has a section) and name the migration file.
- Follow every rule in [Database migrations](#database-migrations): forward-only, expand/contract, `lock_timeout`, row-level security for new tenant tables.
- Keep the migration and the code that needs it in one pull request, and do not mix it with unrelated changes: a merged migration reaches installations with the next release.
- A change to `api/openapi.yaml` that breaks clients needs an entry in `api/oasdiff-err-ignore.txt` with its reason; `oasdiff` runs on every pull request.

### TODO format

Every `TODO` or `FIXME` must reference a GitHub issue: `TODO(#123): ...`. CI fails on bare TODOs (`scripts/check-todos.sh`).

### References in comments

Do not reference internal records (decision-record numbers, ticket or work-item ids) in code, comments, tests or docs. State the rule or the reason itself; link a public GitHub issue (`#123`) when a reference is needed. `scripts/check-internal-refs.sh` fails CI on `ADR-` numbers and work-item ids.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).

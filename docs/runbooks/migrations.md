# Runbook: database migrations

This page is the practical side of the migration rules summarised in [CONTRIBUTING.md](../../CONTRIBUTING.md#database-migrations).

Every migration is expand/contract: the version that is still running keeps working on the new schema during a rollout, and a contract step waits for the release after the one that stopped using the old shape. A migration that cannot be written this way needs a recorded design decision and a planned maintenance window.

## Before every release that contains a migration
Take a backup or snapshot of the database right before the rollout that applies it. The schema is forward-only and images cannot be rolled back across a migration, so the backup is the only way back from a migration that damages data. On Amazon RDS, for example (always pass an explicit `--profile`):
```sh
aws --profile <profile> rds create-db-snapshot --db-instance-identifier <instance> --db-snapshot-identifier proreta-pre-<migration version>
aws --profile <profile> rds wait db-snapshot-available --db-snapshot-identifier proreta-pre-<migration version>   # read-only
```
If the server hosts other databases, restoring a snapshot means a new instance and a coordinated switch, not an in-place rollback.

## Adding a migration
1. Create `backend/migrations/<UTC YYYYMMDDHHMMSS>_<snake_case_description>.sql` (use the current UTC time).
2. Start with `-- +goose Up`, then `SET LOCAL lock_timeout = '5s';`. No `Down` section.
3. Write it so the previous application version still works (expand/contract, binding, see above). Add nullable or defaulted columns; do not drop or rename in the same release.
4. New tenant-scoped tables need: `tenant_id uuid not null`, `ENABLE` and `FORCE ROW LEVEL SECURITY`, a policy on `current_setting('app.tenant_id', true)::uuid`, composite foreign keys `(tenant_id, id)`. Default privileges already grant the runtime roles; add explicit grants only when you need less than the default (for example insert-only).
5. Large indexes: a separate migration with `-- +goose NO TRANSACTION` and `CREATE INDEX CONCURRENTLY` (use `IF NOT EXISTS` so a retried Job does not fail on a finished index). A `CONCURRENTLY` build that fails (lock timeout, cancelled Job) leaves an INVALID index that `IF NOT EXISTS` then skips: check with `SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid;` as `proreta_owner`, drop it with `DROP INDEX CONCURRENTLY <name>;` and rerun the migration (delete its row from `goose_db_version` first if it was recorded).
6. Update the queries in the owning module and run `make generate`. Commit the generated code. Regenerate the schema snapshot (`go test ./migrations -run TestIntegrationSchemaSnapshot -update` with `TEST_DATABASE_ADMIN_URL` set) and commit `backend/migrations/testdata/schema.sql`; its diff is the reviewable picture of the schema change.
7. A new `SECURITY DEFINER` function also needs an entry in `definerAllowList` (`backend/migrations/integration_test.go`) with the one runtime role allowed to execute it, `SET search_path = public, pg_temp`, `REVOKE ALL ... FROM PUBLIC` and a `GRANT EXECUTE` to that role only. A new non-isolation policy needs an entry in `extraPolicies`. Both are reviewed changes.
8. Changing the signature of a definer function the running version calls breaks it during the rollout: add the new function next to the old one and drop the old one a release later. Changing only its body with `CREATE OR REPLACE` (same arguments and result) keeps the owner and the grants and is rollout-safe.
9. Add an upgrade-path test when the migration changes existing rows: copy `TestIntegrationUpgradePath` (`backend/migrations/upgrade_path_integration_test.go`), seed data on the baseline, apply the new migration and assert what it promises for those rows.
10. CI applies all migrations from scratch on Postgres 15, runs the schema lint and the snapshot test, and checks that generated code did not drift.

## The baseline
`backend/migrations/20261109150000_baseline.sql` is the whole schema of a new installation, written by domain: default privileges, tenancy and auth, projects, monitors and probe locations, check results and rollups, incidents and notifications, heartbeats, status pages, invitations and OAuth domains, plans, platform admin, the grant on `goose_db_version`, and the seeds (the `default` probe location, the `free`, `internal` and `community` plans). It ends with `SELECT ensure_partitions();` so the first partitions get every grant and policy of their parent. The database, the roles and the `citext` extension come from `proretactl db bootstrap`, never from a migration.

Every later change is a new file after it; the baseline itself is never edited once an installation has applied it. `definerAllowList` and `extraPolicies` in `backend/migrations/integration_test.go` list the current definer functions and owner policies; the snapshot in `backend/migrations/testdata/schema.sql` shows every table, column, constraint, index, policy, trigger, function and grant.

## What the tests enforce
- `TestMigrationFiles` (unit, no database): name `YYYYMMDDHHMMSS_snake_case.sql` with a valid UTC timestamp and no duplicate, no `+goose Down`, the first statement after `+goose Up` is `SET LOCAL lock_timeout = '5s';` (`SET lock_timeout = '5s';` in a `NO TRANSACTION` migration). CI also checks file naming in the repo-hygiene job.
- `TestIntegrationMigrateFromScratchAndRerun`: all migrations apply to an empty database created by `dbbootstrap` and a second run is a no-op.
- `TestIntegrationSchemaLint` (schema lint), on the migrated database:
  - every table with `tenant_id` has RLS enabled and forced and a permissive `tenant_isolation`-shaped policy (all commands, PUBLIC or runtime roles, `USING` and `WITH CHECK` on `app.tenant_id`);
  - any other permissive policy on a tenant table must be in `extraPolicies` with exactly its command and roles (today only the `owner_*` policies for `proreta_owner` used by definer functions);
  - runtime roles own nothing, are not superusers, do not bypass RLS and are not members of `proreta_owner`;
  - every `SECURITY DEFINER` function is in `definerAllowList`, owned by `proreta_owner`, has a pinned `search_path`, is not executable by PUBLIC and is executable only by its allow-listed role (directly or through a group).
  The test also proves each rule fails on a deliberately broken object.
- `TestIntegrationSchemaSnapshot`: the migrated schema, read from the catalog with `pg_get_*def` (stable across Postgres major versions, unlike `pg_dump`), equals `testdata/schema.sql`. Regenerate it with `-update` after reviewing a schema change.
- `TestIntegrationSeeds`: the seeded plans (limits and features) and the `default` probe location.
- `TestIntegrationUpgradePath`: the template for upgrade-path tests (baseline, seed data, the next migration).
- The other integration tests in `backend/migrations` check the behaviour of the final schema from the point of view of the runtime roles: grants and RLS of every table, the definer functions (claims, fencing, rollups and their catch-up, retention, heartbeats, reminders, invitations, OAuth domains, status pages, the platform admin functions and their SQLSTATEs `PRA01`..`PRA17`, `PRO01`/`PRO02`), the login tenant order and the start-up schema guard.

## How it runs in the cluster
Helm chart with ArgoCD, sync order: bootstrap hook (wave -3, when enabled), migrate Job (wave -1), workloads (wave 0). A failed migration stops the sync; old pods keep running. Look at the Job logs: `kubectl --context <cluster-context> -n proreta logs job/proreta-migrate`.

## Fixing a bad migration
Never edit an applied file. Write a new forward migration that corrects it. For an emergency, stop the rollout, fix forward, push. If a migration is half-applied (non-transactional), check `goose_db_version` and the catalog before writing the fix.

## Rollback of the application
Roll the image tag back in your values file only between releases with the same migrations: api, worker and jobs refuse to start when the database has a migration newer than their binary. Otherwise roll forward; the schema is never rolled back (restore from the pre-release snapshot as a last resort).

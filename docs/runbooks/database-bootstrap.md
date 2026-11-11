# Runbook: database bootstrap

The database `proreta` and the roles `proreta_owner`, `proreta_api`, `proreta_worker` and `proreta_statuspage` are created by `proretactl db bootstrap`. The Helm chart runs it as the hook Job `proreta-db-bootstrap` before the migration Job when `database.bootstrap.enabled` is `true`. Names on this page are for the release `proreta`; the database Secret is `database.secretName`, by default `<fullname>-db` (`proreta-db` here). This page covers enabling it, verifying it and the manual fallback. Chart details: [deploy/helm/proreta/README.md](../../deploy/helm/proreta/README.md).

## Prerequisites
- A PostgreSQL server (15 or newer) the cluster can reach, and an admin login on it with `LOGIN`, `CREATEROLE` and `CREATEDB` that can connect to the database `postgres` (no superuser needed; on Amazon RDS: the master user). The Proreta roles are never superusers and never `BYPASSRLS`. Exact privileges, extensions and managed-database notes: [installation guide](../install.md#postgresql).
- The role names are fixed: one PostgreSQL server hosts one Proreta installation.
- The admin login, with the keys `username` and `password`, from one of two places:
  - **A plain Kubernetes Secret** you create in the release namespace, named in `database.bootstrap.adminSecret.existingSecret` (the default way; `values.example.yaml` uses `proreta-db-admin-login`). No External Secrets Operator is needed. Create it without putting the password on the command line, for example `kubectl --context <cluster-context> -n proreta create secret generic proreta-db-admin-login --from-env-file=/dev/stdin` and type `username=...` and `password=...` lines, then Ctrl-D. You can delete the Secret after the first install (see Skipping below). Other key names than `username` and `password`: set `database.bootstrap.adminSecret.usernameKey` and `passwordKey`.
  - **Your secret store**, through the External Secrets Operator (opt-in, `externalSecrets.enabled: true`): key `database.bootstrap.adminSecret.remoteKey` (default `proreta/db-admin`, properties `username` and `password`, or `usernameKey` / `passwordKey`), read through the store named in `externalSecrets.secretStoreRef` (`name` is required) while `existingSecret` is empty. The chart renders a short-lived ExternalSecret that is deleted after the Job succeeded.

  Rendering fails when neither is configured.
- The admin and encryption Secrets are generated in the cluster by the separate hook Job `proreta-secrets-bootstrap` (default `secrets.*.source: generate`). That Job has no database credentials and is independent of `database.bootstrap.enabled`.

## First deployment
1. In your values file set `database.bootstrap.enabled: true` and `database.host` to the server host (and `database.port`, `database.name`, `database.sslmode` if they differ from `5432`, `proreta`, `require`) (see [values.example.yaml](../../deploy/helm/proreta/values.example.yaml)).
2. Install (`helm upgrade --install`, see the [installation guide](../install.md)) or sync. Order (Helm hook weights, the same numbers as ArgoCD sync waves): admin ExternalSecret (External Secrets only) and RBAC (-4), db and secrets bootstrap Jobs (-3), migrate ServiceAccount (-2), migrate Job (-1), workloads (0).
3. Verify: `kubectl --context <cluster-context> -n proreta logs job/proreta-db-bootstrap` shows the role attributes summary (no secrets): four `proreta_*` roles with `SUPERUSER false`, `BYPASSRLS false`, database `proreta` owned by `proreta_owner`. Confirm the Secret `proreta-db` exists (list its keys only, never print values). With External Secrets, confirm that the temporary Secret `proreta-db-admin` was removed after the Job.
4. Delete the admin Secret you created (`existingSecret`); with External Secrets it is already gone. The flag may stay on.

## Skipping
Leaving `database.bootstrap.enabled: true` is safe. The Job references the admin Secret as optional, and `proretactl db bootstrap` skips itself (exit 0, without reading the admin login, logging `bootstrap skipped`) when all of these hold:
- the Secret `proreta-db` exists with every password and its URLs match the chart settings;
- `proreta_owner` connects to the database with the password from it;
- the table `goose_db_version` exists (bootstrap is only needed before the first migration).

A Secret left over from an earlier database does not skip: the owner login or the migration table is missing there, so the full bootstrap runs. When the full bootstrap is needed and no admin login is available, the Job fails with `the database is not ready and no database admin login is available` and the reason; create the admin Secret and rerun. The skip check never prints passwords.

`database.bootstrap.force: true` (`PRORETA_BOOTSTRAP_FORCE=true`) always runs the full bootstrap and needs the admin login.

## When to enable it again
- A new database or environment.
- Regenerating role passwords: delete the Secret `proreta-db`, provide the admin login again, sync (the missing Secret makes the Job run; `database.bootstrap.force: true` reruns it with the existing Secret, for example after editing a password in it); the Job generates new passwords and runs `ALTER ROLE`; restart the workloads to pick up the new Secret.
- An installation whose `proreta-db` Secret has no `statuspage_url` key yet (the Job does not skip, so it needs the admin login): one run creates `proreta_statuspage` and adds the key, leaving the other keys untouched (see the [status page runbook](status-page.md)).

## Without the bootstrap Job
With `database.bootstrap.enabled: false` from the start, create the database Secret yourself before the install, with the keys `api_url`, `worker_url`, `statuspage_url` (only with `statuspage.enabled`) and `owner_url` (libpq URLs of the four roles), and set `database.secretName` to its name (or name it `<fullname>-db`). The roles and grants must match what the bootstrap creates; running the bootstrap once (Job or manual fallback below) is the supported way.

## Manual fallback
Run the same command by hand against a server you can reach, with the environment variables the Job uses (`PRORETA_BOOTSTRAP_HOST`, `PRORETA_BOOTSTRAP_PORT`, `PRORETA_BOOTSTRAP_ADMIN_USER`, `PRORETA_BOOTSTRAP_ADMIN_PASSWORD`, `PRORETA_BOOTSTRAP_DB_NAME`, `PRORETA_BOOTSTRAP_SSLMODE`, `PRORETA_BOOTSTRAP_SECRET_NAME`, `PRORETA_BOOTSTRAP_NAMESPACE`), from a pod or machine with access to the Kubernetes API for the Secret. Never pass passwords as command arguments and never commit them.

## Notes
- `sslmode=require` encrypts the connection but does not verify the server certificate.
- Proreta roles can connect to other databases of the server (default `PUBLIC` connect privilege) but own and are granted nothing there. Revoke `CONNECT` from `PUBLIC` on those databases if that matters for your server.

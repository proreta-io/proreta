// Package dbbootstrap implements `proretactl db bootstrap`: it creates (or repairs) the Proreta
// database and its four roles inside an existing PostgreSQL 15 instance and keeps their
// passwords in a Kubernetes Secret. It runs in a hook Job before the migration Job and is
// idempotent: running it again changes nothing except re-applying the same passwords.
//
// # Kubernetes Secret contract
//
// The Secret (default name proreta-db, in the pod's namespace) is type Opaque with exactly
// these keys:
//
//	owner_password   password of proreta_owner (migrations)
//	api_password     password of proreta_api (cmd/api)
//	worker_password      password of proreta_worker (cmd/worker, cmd/jobs)
//	statuspage_password  password of proreta_statuspage (cmd/statuspage)
//	owner_url            postgres://proreta_owner:<pw>@<host>:<port>/<db>?sslmode=<mode>
//	api_url              postgres://proreta_api:<pw>@<host>:<port>/<db>?sslmode=<mode>
//	worker_url           postgres://proreta_worker:<pw>@<host>:<port>/<db>?sslmode=<mode>
//	statuspage_url       postgres://proreta_statuspage:<pw>@<host>:<port>/<db>?sslmode=<mode>
//
// Passwords are 32 random bytes, URL-safe base64 without padding (43 characters); in the URLs
// they are percent-escaped anyway. The workloads read only the *_url keys.
//
// Behaviour:
//   - Secret exists with the owner, api and worker *_password keys: those passwords are used (so a manual
//     rotation is done by editing the Secret and re-running). Missing or stale *_url keys (for
//     example after a host or password change) are rebuilt with a merge patch and a drift
//     notice naming only the keys is logged; the password keys are never rewritten.
//   - Secret exists without statuspage_password (an older Secret): that password and its URL
//     are generated and added with a merge patch before any role is changed.
//   - Secret does not exist: four passwords are generated and the Secret is created BEFORE
//     any role is changed, so a password is never set in the database without being stored.
//     If another run created it in the meantime, that Secret is read and used.
//   - Secret exists but lacks a password key: the run fails; it never overwrites a Secret it
//     did not write completely.
//
// The Job's ServiceAccount needs get, create and patch on secrets in its namespace (the token, CA
// bundle and namespace are read from /var/run/secrets/kubernetes.io/serviceaccount).
//
// # Skipping
//
// Ensure, which `proretactl db bootstrap` calls, does nothing and needs no admin login when the
// database is already set up: the Secret exists with every password and current URLs,
// proreta_owner connects with the password from it, and the goose_db_version table exists
// (bootstrap is only needed before the first migration). Otherwise, or when Options.Force is
// set, it runs the full bootstrap below, and fails with ErrAdminRequired when there is no admin
// login. A Secret left over from an earlier database does not skip: the owner login or the
// migration table is missing there.
//
// # Database changes
//
// Connected to database "postgres" as the instance admin (on RDS the master user, a member of
// rds_superuser and not a real superuser), under a cluster-wide advisory lock:
//   - roles proreta_owner (CONNECTION LIMIT 3), proreta_api (20), proreta_worker (20),
//     proreta_statuspage (10): LOGIN,
//     NOSUPERUSER, NOCREATEDB, NOCREATEROLE, NOREPLICATION, NOBYPASSRLS. Superuser,
//     replication and bypassrls can only be changed by a real superuser, so they are set at
//     creation and verified (not altered) afterwards; the run fails if one is set.
//   - passwords are sent as SCRAM-SHA-256 verifiers computed here, so the plaintext never
//     reaches the server (or its statement log);
//   - role settings: statement_timeout (owner 5min, api 15s, worker 30s, statuspage 5s),
//     idle_in_transaction_session_timeout (30s api and worker, 10s statuspage) and
//     default_transaction_read_only for proreta_statuspage;
//   - database OWNER proreta_owner (the admin is temporarily granted proreta_owner, as RDS
//     requires, and the membership is revoked at the end), REVOKE ALL FROM PUBLIC, CONNECT for
//     the four roles;
//   - inside the database: schema public owned by proreta_owner, no privileges for PUBLIC, USAGE
//     for proreta_api, proreta_worker and proreta_statuspage, extension citext created as proreta_owner
//     (so no object in the database is owned by the admin login), and EXECUTE on
//     status_page_snapshot(text) for proreta_statuspage once the migration created it (the
//     migration grants it too when the role already exists).
//
// Verification fails if a role has an unsafe attribute, the database has another owner, or
// a runtime role (proreta_api, proreta_worker, proreta_statuspage) is a member of proreta_owner. The summary also reports whether the
// admin is still a member of proreta_owner after the temporary grant is revoked.
//
// Passwords and URLs are never logged. The summary printed at the end lists role attributes
// only.
package dbbootstrap

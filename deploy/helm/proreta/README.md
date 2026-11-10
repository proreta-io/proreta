# proreta Helm chart

Deploys Proreta (multi-tenant uptime monitoring): `api`, `worker`, `jobs` and `statuspage` (all from the single
`proreta-backend` image, one binary each), the `web` frontend, a database bootstrap Job, a secrets bootstrap Job, a
migration Job and a Gateway API `HTTPRoute`. Optionally ExternalSecrets, a NetworkPolicy, a PodDisruptionBudget and
ServiceMonitors. Gateway API only for now: no Ingress objects are created (Ingress is not supported yet).

**Installing:** follow [docs/install.md](../../../docs/install.md) (prerequisites, step-by-step commands, upgrade,
uninstall, troubleshooting). This page is the reference for the chart's values and behaviour.

## Prerequisites
- Kubernetes 1.29 or newer (`kubeVersion`), `amd64` or `arm64` nodes, Helm 3.
- Gateway API CRDs v1.2 or newer (standard channel), a Gateway controller, and a Gateway with an HTTPS listener that
  admits routes from the release namespace. TLS is terminated by the Gateway.
- An external PostgreSQL 15 or newer, and for the first install a login with `CREATEROLE` and `CREATEDB` (see
  [database bootstrap](../../../docs/runbooks/database-bootstrap.md)). Only the trusted extension `citext` is used.
  One PostgreSQL server hosts one Proreta installation (the role names are fixed).
- No External Secrets Operator, cert-manager, metrics-server or StorageClass is needed (no PersistentVolumeClaims).

## Install
From a git checkout (no chart repository yet); the image tag is required there:
```sh
helm upgrade --install proreta deploy/helm/proreta -n proreta -f my-values.yaml --set image.tag=<tag> --wait --timeout 10m
```
Once a release is published, from the OCI registry. Start from that chart's own defaults, not from a checkout's
`values.example.yaml`, and leave `image.tag` unset: the packaged chart's `appVersion` is the release and is used as the
image tag (a tag in your values file would override it):
```sh
helm show values oci://ghcr.io/proreta-io/charts/proreta --version X.Y.Z > my-values.yaml
helm upgrade --install proreta oci://ghcr.io/proreta-io/charts/proreta --version X.Y.Z -n proreta -f my-values.yaml --wait --timeout 10m
```
`templates/NOTES.txt` prints the next steps after every install and upgrade.

## Files
- `values.yaml` generic defaults, every key commented.
- `values.example.yaml` the keys an installation usually sets (image tag, URL, Gateway, database host), each one to
  change marked `CHANGE`. Copy it and pass it with `-f`. CI renders the chart with it.
- `values.schema.json` strict validation with a title and description per key: Helm rejects a values file with an
  unknown key (for example a typo such as `database.hots`) or a malformed value and names the key.
- Images: `ghcr.io/proreta-io/proreta-backend` and `ghcr.io/proreta-io/proreta-web`, tagged `X.Y.Z` for releases and
  `sha-<7 hex>` for builds of `main` (`.github/workflows/release.yml`).

## Values reference

Every key is validated by `values.schema.json`; unknown keys fail the install. Free-form maps are only the
Kubernetes pass-through fields (`podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity`,
`podSecurityContext`, `securityContext`, `serviceAccount.annotations`, `serviceMonitor.labels`) and the
`*.env` lists of EnvVar objects. `<fullname>` is the release name, or `<release>-proreta` when the release name does
not contain `proreta` (`fullnameOverride` replaces it).

| Key | Default | Description |
|---|---|---|
| `nameOverride`, `fullnameOverride` | `""` | Replace the chart name / the full base name of every resource. |
| `image.repository` | `ghcr.io/proreta-io/proreta-backend` | Backend image. |
| `image.tag` | `""` | `X.Y.Z` (no `v`) or `sha-<7 hex>`. Empty: the chart's `appVersion` (packaged chart only; required from a git checkout). |
| `image.pullPolicy` | `IfNotPresent` | Pull policy of every container. |
| `imagePullSecrets` | `[]` | Pull secrets for a private registry, `[{name: ...}]`. |
| `config.publicUrl` | `""` | **Required.** The https URL users open, no trailing slash. Its host is the default of `httpRoute.hostnames`. |
| `config.logLevel` | `info` | `PRORETA_LOG_LEVEL`. |
| `config.adminPort` | `9090` | Health and metrics port of the backend pods. |
| `config.sessionTTL`, `config.cookieSecure` | `24h`, `true` | Session lifetime; `Secure` cookies (keep `true`). |
| `config.trustedProxyHops` | `0` | Trusted proxies in front of the api; `1` behind one Gateway. |
| `config.registrationEnabled` | `false` | Self-service creation of organizations (no email verification). |
| `config.passwordLoginEnabled` | `true` | `false`: password form for the platform admin only; needs an OAuth provider. |
| `config.blockedCIDRs` | `[]` | Extra ranges the SSRF guard refuses (cluster and VPC CIDRs). |
| `database.secretName` | `""` | Secret with the connection URLs. Empty: `<fullname>-db`. |
| `database.host` | `""` | PostgreSQL host; required when `database.bootstrap.enabled`. |
| `database.port`, `database.name`, `database.sslmode` | `5432`, `proreta`, `require` | Server port, database name, libpq `sslmode`. |
| `database.bootstrap.enabled` | `false` | Run the database bootstrap hook Job (first install; turn it off afterwards). |
| `database.bootstrap.adminSecret.existingSecret` | `""` | Secret in the namespace with the server admin login. |
| `database.bootstrap.adminSecret.remoteKey` | `proreta/db-admin` | With External Secrets and no `existingSecret`: the remote secret. |
| `database.bootstrap.adminSecret.usernameKey`, `.passwordKey` | `username`, `password` | Keys of the login in that Secret (properties of the remote secret). |
| `database.bootstrap.backoffLimit`, `.activeDeadlineSeconds`, `.ttlSecondsAfterFinished`, `.resources` | `2`, `300`, `600`, 50m/64Mi | Job settings. |
| `secrets.admin.source`, `.login`, `.remoteKey` | `generate`, `admin`, `proreta/admin` | Platform admin Secret `<fullname>-admin`. |
| `secrets.crypto.source`, `.keyId`, `.remoteKey` | `generate`, `k1`, `proreta/crypto` | Encryption key Secret `<fullname>-crypto`. |
| `secrets.statuspage.source`, `.remoteKey` | `generate`, `proreta/statuspage` | Status page cookie key Secret `<fullname>-statuspage`. |
| `secrets.oauth.existingSecret`, `.remoteKey` | `""`, `proreta/oauth` | OAuth client credentials. |
| `secrets.bootstrap.backoffLimit`, `.activeDeadlineSeconds`, `.ttlSecondsAfterFinished`, `.resources` | `2`, `300`, `600`, 50m/96Mi | Secrets bootstrap Job settings. |
| `oauth.google.enabled`, `oauth.github.enabled` | `false` | Sign-in providers. |
| `oauth.allowedDomains` | `[]` | Google Workspace domains organizations may claim. |
| `externalSecrets.enabled` | `false` | Render ExternalSecrets (needs the operator). |
| `externalSecrets.secretStoreRef.kind`, `.name` | `ClusterSecretStore`, `""` | The store; the name is required when enabled. |
| `externalSecrets.apiVersion`, `.refreshInterval`, `.creationPolicy` | `external-secrets.io/v1`, `"0"`, `Owner` | ExternalSecret settings. |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` | empty | Applied to every pod. |
| `podSecurityContext`, `securityContext` | restricted profile | Pod and container security contexts. Free-form maps: overriding them can break the restricted Pod Security profile. |
| `strategy.rollingUpdate` | `maxSurge: 1`, `maxUnavailable: 0` | Rollout of every Deployment. |
| `revisionHistoryLimit`, `terminationGracePeriodSeconds` | `3`, `30` | Deployment defaults. |
| `serviceAccount.annotations` | `{}` | Annotations of every ServiceAccount. |
| `api.replicas`, `.httpPort`, `.resources`, `.env`, `.service.port` | `2`, `8080`, 50m/96Mi, `[]`, `8080` | API Deployment. |
| `api.terminationGracePeriodSeconds` | unset | Overrides the top-level value. |
| `api.pdb.enabled`, `.minAvailable` | `false`, `1` | PodDisruptionBudget of the api. |
| `worker.replicas`, `.resources`, `.env`, `.terminationGracePeriodSeconds` | `2`, 50m/96Mi, `[]`, `45` | Worker Deployment. |
| `worker.claimBatch`, `.concurrency`, `.tenantConcurrency`, `.hostConcurrency` | `100`, `50`, `5`, `2` | Check scheduling limits. |
| `worker.location` | `default` | Probe location whose checks the worker runs (the database is seeded with `default`). |
| `jobs.replicas`, `.resources`, `.env`, `.terminationGracePeriodSeconds` | `1` (0 or 1), 25m/64Mi, `[]`, `45` | Slack delivery and housekeeping. |
| `statuspage.enabled`, `.replicas`, `.httpPort`, `.resources`, `.env`, `.service.port` | `true`, `1`, `8080`, 10m/48Mi, `[]`, `8080` | Public status page renderer at `/status`. |
| `web.enabled`, `.replicas`, `.httpPort`, `.resources`, `.env`, `.service.port` | `true`, `2`, `8080`, 10m/32Mi, `[]`, `8080` | Web app at `/`. `httpPort` must be the port nginx listens on (8080). |
| `web.image.repository`, `.tag` | `ghcr.io/proreta-io/proreta-web`, `""` | Web image; the tag defaults to the backend tag. |
| `migration.hookPhase` | `Sync` | ArgoCD hook phase of the migrate Job (`PreSync` only without the database bootstrap). |
| `migration.backoffLimit`, `.activeDeadlineSeconds`, `.ttlSecondsAfterFinished`, `.resources` | `2`, `600`, `3600`, 25m/64Mi | Migrate Job settings. |
| `httpRoute.enabled` | `true` | Render the HTTPRoute. |
| `httpRoute.hostnames` | `[]` | Host names; empty: the host of `config.publicUrl`. |
| `httpRoute.parentRef.name` | `""` | **Required when `httpRoute.enabled`.** Your Gateway. |
| `httpRoute.parentRef.namespace`, `.sectionName` | `""`, `""` | Gateway namespace (empty: the release namespace); listener (empty: every listener that admits the route). |
| `httpRoute.apiPathPrefix`, `.apiRequestTimeout`, `.statusPathPrefix` | `/api`, `60s`, `/status` | Routing rules (`statusPathPrefix` is fixed). |
| `networkPolicy.enabled` | `false` | Ingress NetworkPolicy. |
| `networkPolicy.gatewayNamespace` | `""` | Namespace of the Gateway's proxy pods; empty: `httpRoute.parentRef.namespace`, else the release namespace. |
| `networkPolicy.monitoringNamespace` | `monitoring` | Namespace allowed to scrape the admin port. |
| `serviceMonitor.enabled`, `.interval`, `.path`, `.labels` | `false`, `30s`, `/metrics`, `{}` | Prometheus Operator ServiceMonitor. |
| `topologySpread.enabled`, `.topologyKey`, `.maxSkew`, `.whenUnsatisfiable` | `false`, `kubernetes.io/hostname`, `1`, `ScheduleAnyway` | Spread the pods. |

Fixed by the chart, not values: the container commands (`/app/api`, `/app/worker`, `/app/jobs`,
`/app/statuspage`, `/app/migrate`, `/app/proretactl db bootstrap`, `/app/proretactl secrets bootstrap`), the keys of the
database Secret each workload reads (below) and the database role names.

## Image tag
`image.tag` defaults to the chart's `appVersion`. A chart packaged by the release workflow carries the release
version there (`helm package --app-version X.Y.Z`); the chart in a git checkout has the placeholder `0.0.0`, so there
`image.tag` is required and rendering fails without it. Pick
- a release version, for example `1.2.3` (the Git tag is `v1.2.3`, the image tag has no `v`), or
- a build of `main`: `sha-` plus the first 7 hex digits of the commit, for example `sha-1a2b3c4`.

`web.image.tag` defaults to the backend tag. When you publish the images from a fork, GitHub creates the ghcr
packages as private on the first push: set each package's visibility to public (package settings on GitHub) after
the first release, or set `imagePullSecrets`.

## Render / lint

```sh
bash scripts/validate-chart.sh   # every value combination, helm lint --strict, invariants, kubeconform
helm lint --strict deploy/helm/proreta -f deploy/helm/proreta/values.example.yaml --set image.tag=sha-0000000
helm template proreta deploy/helm/proreta -n proreta -f deploy/helm/proreta/values.example.yaml --set image.tag=sha-0000000
```

`values.schema.json` rejects, among others, unknown keys, a `config.publicUrl` that is not https or ends with a slash,
a `config.registrationEnabled` that is not a boolean, an admin login outside 3-254 of `[a-z0-9._@+-]`,
`externalSecrets.enabled` without a store and a workload without `resources.requests` and `resources.limits`. The
templates additionally fail with a message for a missing image tag, a missing `config.publicUrl`, a missing
`httpRoute.parentRef.name`, a database bootstrap without `database.host` or without an admin login source, OAuth
without a credentials source, External Secrets without a store name and an `externalSecret` source while
`externalSecrets.enabled` is false. `scripts/check-rendered-chart.py` asserts the invariants listed under
"Environment per workload" and "Hardening" on every render, including the Pod Security Standard "restricted"
profile and the hook order.

## Environment per workload

Source of truth: `backend/internal/platform/config/config.go`. Secret names below are for the release `proreta`.

| Variable | api | worker | jobs | migrate | db-bootstrap |
|---|---|---|---|---|---|
| `PRORETA_LOG_LEVEL` | x | x | x | x | x |
| `PRORETA_ADMIN_ADDR`, `PRORETA_DATABASE_URL` (`api_url` / `worker_url`) | x | x | x | | |
| `PRORETA_ENCRYPTION_KEYS`, `PRORETA_ENCRYPTION_ACTIVE_KEY_ID` (`proreta-crypto`) | x | **never** | x | | |
| `PRORETA_ADMIN_LOGIN`, `PRORETA_ADMIN_PASSWORD_HASH` (`proreta-admin`) | x | **never** | **never** | | |
| `PRORETA_HTTP_ADDR`, `PRORETA_SESSION_TTL`, `PRORETA_COOKIE_SECURE`, `PRORETA_TRUSTED_PROXY_HOPS` (`config.trustedProxyHops`: number of trusted proxies in front of the api, default `0`; set `1` behind one gateway) | x | | | | |
| `PRORETA_REGISTRATION_ENABLED` (`config.registrationEnabled`, chart default `false`; self-service creation of new organizations) | x | | | | |
| `PRORETA_PASSWORD_LOGIN_ENABLED` (`config.passwordLoginEnabled`, default `true`, `false` keeps the password form for the platform admin only and needs an OAuth provider) | x | | | | |
| `PRORETA_OAUTH_ALLOWED_DOMAINS` (`oauth.allowedDomains`, only rendered when non-empty; the Workspace domains organizations may claim) | x | | | | |
| `PRORETA_OAUTH_GOOGLE_CLIENT_ID`/`_SECRET`, `PRORETA_OAUTH_GITHUB_CLIENT_ID`/`_SECRET` (`secrets.oauth.existingSecret` or `proreta-oauth`, only for providers enabled in `oauth.*.enabled`) | x | **never** | **never** | | |
| `PRORETA_PUBLIC_URL` (`config.publicUrl`, required; also set on the statuspage Deployment, see below) | x | | x | | |
| `PRORETA_BLOCKED_CIDRS` (`config.blockedCIDRs`, only rendered when non-empty) | x | x | x | | |
| `PRORETA_WORKER_CLAIM_BATCH`, `_CONCURRENCY`, `_TENANT_CONCURRENCY`, `_HOST_CONCURRENCY` | | x | | | |
| `PRORETA_WORKER_LOCATION` (`worker.location`, default `default`) | | x | | | |
| `PRORETA_MIGRATE_DATABASE_URL` (`owner_url`) | | | | x | |
| `PRORETA_BOOTSTRAP_*` (`database.*` and the admin login) | | | | | x |

The `statuspage` Deployment (`statuspage.enabled`) gets exactly `PRORETA_LOG_LEVEL`,
`PRORETA_ADMIN_ADDR`, `PRORETA_DATABASE_URL` (key `statuspage_url`, role `proreta_statuspage`), `PRORETA_PUBLIC_URL`,
`PRORETA_HTTP_ADDR`, `PRORETA_COOKIE_SECURE`, `PRORETA_TRUSTED_PROXY_HOPS` and `PRORETA_STATUSPAGE_COOKIE_KEY`
(Secret `proreta-statuspage`, key `cookie_key`; generated by the secrets bootstrap Job or read from
`secrets.statuspage.remoteKey`): never encryption keys, admin credentials or SSRF settings. The HTTPRoute sends
`/status` to it; `/api` and `/` are unchanged. `scripts/check-rendered-chart.py` asserts all of this.

The secrets bootstrap Job gets only `PRORETA_LOG_LEVEL` and `PRORETA_SECRETS_*` (modes, admin login,
Secret names, key id): no database URL and no database admin login.

Only the migrate Job reads `owner_url`.

## Hardening and resources

- Every pod meets the Pod Security Standard **restricted** profile (the namespace can enforce it): non-root (UID
  65532), read-only root filesystem, `allowPrivilegeEscalation: false`, drop ALL capabilities, seccomp
  `RuntimeDefault`, only `emptyDir` volumes; no `hostNetwork`, `hostPort` or privileged containers.
- ServiceAccount tokens: only the two bootstrap Jobs mount one (they create Secrets); every other ServiceAccount and
  pod sets `automountServiceAccountToken: false`.
- Graceful shutdown: `terminationGracePeriodSeconds` 45 for worker and jobs (in-flight checks and sends finish), 30
  for the others; rolling updates use `maxSurge: 1`, `maxUnavailable: 0`.
- Probes: api, worker, jobs and statuspage use `/healthz` (liveness) and `/readyz` (readiness) on the admin port; web
  uses `/healthz` on its `http` port (served by nginx itself). Web mounts only `/tmp` (emptyDir) because
  `web/nginx.conf` keeps the pid and temp files there.
- Requests (cpu/memory): api 50m/96Mi, worker 50m/96Mi, jobs 25m/64Mi, web 10m/32Mi, statuspage 10m/48Mi (limit
  192Mi: one 64 MiB argon2id password check at a time); memory limits are 3-4x. With the default replicas the memory
  requests stay around 0.5Gi. Sizing and scaling per component: [docs/install.md](../../../docs/install.md#sizing).
  `jobs.replicas` may only be 0 or 1.
- No Service is of type `LoadBalancer` or `NodePort`: traffic enters through your Gateway only.
- Optional and off: `api.pdb`, `serviceMonitor` (needs the Prometheus Operator CRDs), `networkPolicy`, `topologySpread`.
- Routing: `/api` goes to the api (60 s request timeout), `/status` to the statuspage, `/` to the web when `web.enabled`.

## Secrets

The default install needs no External Secrets Operator: everything is generated in the cluster or comes from a
Secret you create. `externalSecrets.enabled` is `false` by default; set it to `true` (with
`externalSecrets.secretStoreRef.name`) to read any of the Secrets below from your secret store instead.

Workloads always read `<fullname>-admin` (`login`, `password_hash`) and `<fullname>-crypto` (`keys`,
`active_key_id`). `secrets.admin.source` and `secrets.crypto.source` choose, per Secret, who creates them:

- `generate` (default): the secrets bootstrap hook Job (`/app/proretactl secrets bootstrap`) creates the Secret
  in the cluster when it is absent and never overwrites it (an incomplete existing Secret fails the Job).
  - admin: login `secrets.admin.login` (default `admin`, stored under the key `login`), a random password whose
    argon2id hash goes to `<fullname>-admin`; the plaintext goes ONCE to `<fullname>-admin-initial`
    (`login`, `password`). Read it, log in, change the password under Profile, delete `<fullname>-admin-initial`
    (always safe, nothing reads it). It is never recreated.
  - crypto: one random 32-byte key with id `secrets.crypto.keyId` (default `k1`). It lives only in the cluster;
    deleting the namespace loses it (it only encrypts the Slack webhook URLs, which can be re-entered).
- `externalSecret`: an ExternalSecret (`externalSecrets.secretStoreRef`, wave -2) materialises it. Needs
  `externalSecrets.enabled: true`; rendering fails otherwise.

OAuth client credentials (only when a provider in `oauth` is enabled), keys `google_client_id`,
`google_client_secret`, `github_client_id`, `github_client_secret` (only those of enabled providers), from either
- a Secret you create in the namespace, named in `secrets.oauth.existingSecret` (no External Secrets needed), or
- the ExternalSecret `<fullname>-oauth` read from `secrets.oauth.remoteKey` (needs `externalSecrets.enabled`).
Rendering fails when neither is configured. See [docs/runbooks/oauth.md](../../../docs/runbooks/oauth.md).

| Remote key (External Secrets only) | Properties | K8s Secret |
|---|---|---|
| `secrets.admin.remoteKey` (`proreta/admin`) | `login`, `password_hash` | `<fullname>-admin` |
| `secrets.crypto.remoteKey` (`proreta/crypto`) | `keys` (JSON string), `active_key_id` | `<fullname>-crypto` |
| `secrets.statuspage.remoteKey` (`proreta/statuspage`) | `cookie_key` | `<fullname>-statuspage` |
| `secrets.oauth.remoteKey` (`proreta/oauth`, when `secrets.oauth.existingSecret` is empty) | `google_client_id`, `google_client_secret`, `github_client_id`, `github_client_secret` (only those of enabled providers) | `<fullname>-oauth` |
| `database.bootstrap.adminSecret.remoteKey` (`proreta/db-admin`, when `database.bootstrap.adminSecret.existingSecret` is empty) | `usernameKey`, `passwordKey` (default `username`, `password`) | `<database Secret>-admin` (hook, deleted after success) |

The secrets Job (`<fullname>-secrets-bootstrap`, rendered when any source is `generate`) has its own ServiceAccount
with an API token and a Role limited to `create` on secrets and `get,update,patch` on exactly `<fullname>-admin`,
`<fullname>-admin-initial`, `<fullname>-crypto` (and `<fullname>-statuspage`). It has no database credentials, so
unlike the database bootstrap it stays enabled. Admin password reset: delete `<fullname>-admin` (and
`<fullname>-admin-initial` if still there), run the upgrade again and restart the api. The api applies the hash of
`<fullname>-admin` only when the admin never changed its password in the app, or when the hash is a new one; a restart
with the same hash keeps a password chosen in the app.

### The database Secret

All backend pods read their connection URL from one Secret, `database.secretName` (default `<fullname>-db`, so two
releases in one namespace never share it). It is NOT an ExternalSecret and NOT managed by Helm/ArgoCD: the
database bootstrap Job creates it at runtime. Its keys are fixed, not values:

| Key | Read by |
|---|---|
| `api_url` | api (`PRORETA_DATABASE_URL`) |
| `worker_url` | worker and jobs (`PRORETA_DATABASE_URL`; jobs claims through `SECURITY DEFINER` functions) |
| `statuspage_url` | statuspage (`PRORETA_DATABASE_URL`) |
| `owner_url` | the migrate Job only (`PRORETA_MIGRATE_DATABASE_URL`) |
| `owner_password`, `api_password`, `worker_password`, `statuspage_password` | nobody (kept for rotation) |

With `database.bootstrap.enabled=false` the chart still renders and the migrate Job and workloads assume the
Secret already exists: either from an earlier bootstrap, or one you created yourself with those keys (set
`database.secretName` to its name). `helm uninstall` leaves it and the generated Secrets in place.

## Database bootstrap hook (`database.bootstrap`)

When `database.bootstrap.enabled=true` (on in `values.example.yaml` for the first install), a hook Job runs
`/app/proretactl db bootstrap` before the migration: it connects to `database.host`:`database.port` (`sslmode`
`database.sslmode`), creates the database `database.name` and the roles (`proreta_owner`, `proreta_api`,
`proreta_worker`, `proreta_statuspage`) and the database Secret. It reads the database server admin login (keys
`database.bootstrap.adminSecret.usernameKey` and `.passwordKey`, default `username` and `password`) from either
- a plain Secret you create in the namespace, named in `database.bootstrap.adminSecret.existingSecret` (the default
  way, no External Secrets Operator needed; delete it after the bootstrap), or
- with `externalSecrets.enabled`, a short-lived ExternalSecret `<database Secret>-admin`
  (`database.bootstrap.adminSecret.remoteKey`, default `proreta/db-admin`, deleted after success).
Rendering fails when neither is configured.

It uses a dedicated ServiceAccount `<fullname>-db-bootstrap` (one of the two in the chart with an API token
mounted), limited by a Role (`create` on secrets; `get,update,patch` only on the database Secret).

The role names are fixed, so one PostgreSQL server hosts one Proreta installation: a second installation on the
same server would reset the role passwords of the first.

Security trade-off: while enabled, the Job holds the database server admin login and runs on every upgrade
(idempotent). After the first successful install set `database.bootstrap.enabled: false`, upgrade, then delete the
admin Secret (the database Secret persists).

## Install order (Helm hooks and ArgoCD sync waves)

The bootstrap and migration Jobs are Helm `pre-install,pre-upgrade` hooks and, with the same numbers, ArgoCD `Sync`
hooks with waves, so plain Helm and ArgoCD run the same order and the very first install works. ArgoCD takes the hook
phase and wave from its own annotations but adds the Helm delete policies to its own: with either tool the hook
ServiceAccounts, Roles and RoleBindings are deleted after a successful run (harmless, they are recreated by the next
run, and it reduces standing privileges), while the Jobs stay until the next run or their TTL.

| Weight / wave | Resources (release `proreta`) |
|---|---|
| -4 | `proreta-db-admin` ExternalSecret (External Secrets only), db and secrets bootstrap ServiceAccount/Role/RoleBinding |
| -3 | `proreta-db-bootstrap` and `proreta-secrets-bootstrap` Jobs |
| -2 | migrate ServiceAccount; ArgoCD also applies the `proreta-admin` / `proreta-crypto` / `proreta-statuspage` / `proreta-oauth` ExternalSecrets here (with Helm they are ordinary resources) |
| -1 | `proreta-migrate` Job |
| 0 | workloads (api, worker, jobs, statuspage, web), Services, HTTPRoute |

Helm waits for each Job to complete and fails the install or upgrade when one fails or exceeds `--timeout`; the
hook ServiceAccounts and Roles are deleted after all hooks succeeded, the finished Jobs remain until
`ttlSecondsAfterFinished` so their logs stay readable. `migration.hookPhase=PreSync` (ArgoCD only) is available only
with `database.bootstrap.enabled=false` (rendering fails otherwise) and would run the Job before the ExternalSecrets
exist on a first sync.

## Notes

- `imagePullSecrets` is empty by default: the published images are meant to be public (a fork must make its ghcr
  packages public after the first release); set it for a private registry or mirror.
- `networkPolicy` (off by default) admits ingress to the api, web and statuspage ports only from the namespace
  `networkPolicy.gatewayNamespace` (where your Gateway's proxy pods run; empty: `httpRoute.parentRef.namespace`, else
  the release namespace) and to the admin port only from `networkPolicy.monitoringNamespace`. Some controllers run the
  proxies outside the Gateway's namespace (Envoy Gateway: in the controller's namespace by default); set
  `gatewayNamespace` explicitly then. Egress stays open because checks reach arbitrary targets. It protects only when
  the cluster CNI enforces NetworkPolicy.
- `serviceMonitor` is off by default (needs the Prometheus Operator CRDs).
- The worker must never use `hostNetwork`. The node metadata endpoint (169.254.169.254) must be unreachable from
  pods; block it yourself on clouds where the platform does not, and add the cluster and VPC CIDRs to
  `config.blockedCIDRs`.

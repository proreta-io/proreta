# Installing Proreta on Kubernetes

This guide takes you from an empty namespace to a working installation with the Helm chart in [deploy/helm/proreta](../deploy/helm/proreta/README.md). It assumes you have a Kubernetes cluster with Gateway API and a PostgreSQL server, and nothing else from this project.

Contents: [Prerequisites](#prerequisites) · [Install](#install) · [After installing](#after-installing) · [Upgrade](#upgrade) · [Uninstall](#uninstall) · [Sizing](#sizing) · [Troubleshooting](#troubleshooting)

## Prerequisites

### Kubernetes
- **Kubernetes 1.29 or newer** (the chart's `kubeVersion`), nodes `amd64` or `arm64` (the images are multi-arch).
- **`kubectl` and Helm 3** (tested with Helm 3.18; other Helm 3 versions should work but were not tested, and Helm 4 was not tested at all). Always point both at the right cluster (`--context` / `--kube-context`); the commands below leave that out for readability.
- **Gateway API, not Ingress.** The chart creates one `HTTPRoute` (`gateway.networking.k8s.io/v1`) and no Ingress objects; Ingress is not supported yet. You need:
  - the Gateway API CRDs, standard channel, v1.2 or newer (the route sets request `timeouts`);
  - a Gateway controller that implements them (tested with Envoy Gateway; Istio, Cilium, NGINX Gateway Fabric and others implement the same API);
  - a `Gateway` with an **HTTPS listener** for your host name, whose `allowedRoutes` admits routes from the Proreta namespace (for example `namespaces: {from: All}` or a selector).
- **TLS is the Gateway's job.** The listener needs a certificate (cert-manager or any other way). Proreta's session cookies are `Secure`, so sign-in only works over https.
- **The cluster must reach the PostgreSQL server** and, for the checks, the internet (port 443 of the sites you monitor).
- **Not needed:** External Secrets Operator (optional, see the chart README), cert-manager (only if your Gateway uses it), metrics-server, Prometheus Operator (only for the optional `ServiceMonitor`), a StorageClass (the chart creates no PersistentVolumeClaims).
- The pods satisfy the Pod Security Standard **restricted** profile, so the namespace can enforce it.

### PostgreSQL
- **PostgreSQL 15 or newer** (CI tests 15; the install below was tested on 16), reachable from the cluster. A bundled database is not part of the chart (not supported yet): bring your own server, managed or self-hosted.
- **An admin login** for the one-time bootstrap. It needs `LOGIN`, `CREATEROLE` and `CREATEDB`; it does not need to be a superuser. It must be able to connect to the database `postgres`. On Amazon RDS and Aurora use the master user (member of `rds_superuser`), on Google Cloud SQL the default `postgres` user (`cloudsqlsuperuser`), on Azure Database for PostgreSQL the server admin.
- **What the bootstrap does with it** (`proretactl db bootstrap`, idempotent, under an advisory lock):
  - creates the roles `proreta_owner` (migrations), `proreta_api`, `proreta_worker` and `proreta_statuspage`, all `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, with connection limits 3, 20, 20 and 10 and per-role statement timeouts. Passwords are random, sent as SCRAM verifiers, and stored only in the Secret `proreta-db` in your namespace;
  - creates the database `proreta` (`database.name`) owned by `proreta_owner`, revokes `PUBLIC` access, grants `CONNECT` to the four roles;
  - temporarily grants itself `proreta_owner` (needed on RDS to create a database owned by another role) and revokes it at the end. On PostgreSQL 16 and newer the admin keeps an implicit membership as the role's creator; the bootstrap log reports whether the admin is still a member (`admin member of proreta_owner: true` or `false`, depending on the server and its version); both are fine, because the migrations and the application do not depend on it;
  - in the new database: makes `proreta_owner` the owner of schema `public` and creates the extension **`citext`** as `proreta_owner`.
- **Extensions:** only `citext` is needed. It is a trusted extension, so the database owner may create it without superuser rights. It is part of the standard contrib modules (present in the official images, RDS, Aurora and Cloud SQL). On Azure Database for PostgreSQL allow-list it first (server parameter `azure.extensions` must include `CITEXT`). The migrations themselves create no extensions.
- **One installation per PostgreSQL server:** the role names (`proreta_owner`, `proreta_api`, `proreta_worker`, `proreta_statuspage`) are fixed and not configurable, so one PostgreSQL server hosts exactly one Proreta installation. A second installation on the same server, even with another `database.name`, would reset the role passwords of the first one. Use a separate server (or managed instance) per installation.
- `pg_hba.conf` (or the provider's firewall) must allow password (SCRAM) logins of the four roles and the admin login from the cluster's pod network, and `max_connections` must leave room for up to 53 connections of these roles.
- TLS: the default `database.sslmode` is `require` (encrypted, certificate not verified). Use `disable` only for a server without TLS, for example a throwaway test database.

### Images
`ghcr.io/proreta-io/proreta-backend` (api, worker, jobs, status pages, migrations, bootstrap) and `ghcr.io/proreta-io/proreta-web` (the web app, nginx), both `amd64` and `arm64`. Release tags are `X.Y.Z` (no `v`); builds of `main` are `sha-<7 hex>`. If you build them from a fork, GitHub creates the packages as private: make them public or set `imagePullSecrets`.

## Install

The commands below install the chart from a git checkout. Every release is also published as an OCI artifact; see [Installing from the OCI registry](#installing-from-the-oci-registry) for the three steps that differ.

Worked example: host `proreta.example.com`, Gateway `gateway` with the HTTPS listener `https` in namespace `gateway-system`, PostgreSQL at `postgres.example.internal`. The resource and Secret names below are those of the release `proreta`; a release name that does not contain `proreta` gives `<release>-proreta-...` names (for example the database Secret `<release>-proreta-db`, or `database.secretName` when set), and a name that contains it is used as it is (a release called `platform-proreta` gives `platform-proreta-api`, `platform-proreta-admin-initial` and so on; Argo CD uses the name of the Application as the release name unless you set `helm.releaseName`), so several releases can share a namespace, each with its own PostgreSQL server.

**1. Get the chart**
```sh
git clone https://github.com/proreta-io/proreta.git
cd proreta
git checkout <release tag or commit>   # the chart and the image tag should come from the same version
```

**2. Create the namespace and the database admin Secret**
```sh
kubectl create namespace proreta
kubectl label namespace proreta pod-security.kubernetes.io/enforce=restricted   # optional, the chart complies
kubectl -n proreta create secret generic proreta-db-admin-login --from-env-file=/dev/stdin
```
The last command reads from the terminal, so the password stays out of your shell history. Type two lines, then press Ctrl-D:
```text
username=<admin login>
password=<admin password>
```
The values are taken literally (no quotes, no trailing spaces). You delete this Secret again after the first install.

**3. Write your values file**
```sh
cp deploy/helm/proreta/values.example.yaml my-values.yaml
```
Edit every line marked `CHANGE`:

| Key | Example | Notes |
|---|---|---|
| `image.tag` | `1.2.3` or `sha-1a2b3c4` | Required from a git checkout: pass it with `--set image.tag=...` (step 4) or uncomment it in the file. `web.image.tag` follows it. |
| `config.publicUrl` | `https://proreta.example.com` | Required. https, no trailing slash. Its host is the host name of the HTTPRoute (set `httpRoute.hostnames` only to serve more names). |
| `httpRoute.parentRef` | `gateway` / `gateway-system` / `https` | Name (required), namespace (empty: the Proreta namespace) and listener (`sectionName`) of your Gateway. There is no built-in Gateway name. |
| `database.host` | `postgres.example.internal` | `port` (5432) and `sslmode` (`require`) next to it. |
| `config.trustedProxyHops` | `1` | Already `1` in the example: one Gateway in front. `0` only without a proxy. |

Leave `database.bootstrap.adminSecret.existingSecret: proreta-db-admin-login` (the Secret from step 2), `config.registrationEnabled: false` and `secrets.*.source: generate`. The chart validates the file against `values.schema.json` and fails with a message naming the key when something is missing, malformed or unknown (a typo such as `database.hots` is rejected, not ignored).

**4. Install**
```sh
TAG=1.2.3   # a release version (no v) or sha-<7 hex>, matching the checkout from step 1
helm upgrade --install proreta deploy/helm/proreta -n proreta -f my-values.yaml --set image.tag="$TAG" --wait --timeout 10m
```
Keep using the same `--set image.tag=...` on every later `helm upgrade` (or put the tag into `my-values.yaml`): an upgrade without it fails to render.
Helm runs these steps in order (the same order applies to ArgoCD as sync waves) and stops at the first failure:

| Order | What | Result |
|---|---|---|
| 1 | ServiceAccounts and Roles of the two bootstrap Jobs | allowed to create Secrets in this namespace only |
| 2 | Job `proreta-db-bootstrap` | database, roles, Secret `proreta-db` |
| 2 | Job `proreta-secrets-bootstrap` | Secrets `proreta-admin`, `proreta-admin-initial`, `proreta-crypto`, `proreta-statuspage` (created only if absent, never overwritten) |
| 3 | ServiceAccount `proreta-migrate`, then Job `proreta-migrate` | applies the database migrations as `proreta_owner` |
| 4 | Deployments `api`, `worker`, `jobs`, `statuspage`, `web`, Services, HTTPRoute | `--wait` waits until they are ready |

With plain Helm only the ServiceAccounts, Roles and Jobs above are hooks. ExternalSecrets (`externalSecrets.enabled`) are ordinary release objects, so Helm creates them with the Deployments in step 4, after the migration; the migration needs only the database Secret that the bootstrap Job creates. ArgoCD orders the ExternalSecrets before the migration with sync waves (-2).

On a small test cluster the whole install took about 20 seconds. The output ends with the next steps (`NOTES`). To follow it from a second terminal:
```sh
kubectl -n proreta get jobs,pods -w
kubectl -n proreta logs job/proreta-db-bootstrap    # role summary, never passwords
kubectl -n proreta logs job/proreta-migrate
```

**5. Check the route**
```sh
kubectl -n proreta get httproute proreta -o jsonpath='{range .status.parents[*].conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
kubectl -n gateway-system get gateway gateway     # ADDRESS: point the DNS name of config.publicUrl here
```
`Accepted=True` and `ResolvedRefs=True` mean the Gateway serves the route. `/api` goes to the api, `/status` to the status page renderer, everything else to the web app.

**6. Sign in as the platform admin**
```sh
kubectl -n proreta get secret proreta-admin-initial -o jsonpath='{.data.login}' | base64 -d; echo
kubectl -n proreta get secret proreta-admin-initial -o jsonpath='{.data.password}' | base64 -d; echo
```
Open `config.publicUrl`, sign in with that login (default `admin`) and password and store the password in a password manager. The application reads only the hash (Secret `proreta-admin`), so nothing breaks if you keep `proreta-admin-initial`, but it is the live password in plain text and readable by everyone who can read Secrets in the namespace, so deleting it is recommended. It is never recreated:
```sh
kubectl -n proreta delete secret proreta-admin-initial
```
There is no way to change this password in the app yet. To reset it, delete the Secrets `proreta-admin` and `proreta-admin-initial`, run the same `helm upgrade` again (a new one-time password is generated) and restart the api, which reads the hash only at start: `kubectl -n proreta rollout restart deploy/proreta-api` ([admin panel runbook](runbooks/admin-panel.md)).

**7. Start using it.** The admin is the owner of the organization **Default** (slug `default`; plan `internal`: up to 1000 monitors, 100 heartbeats, 10 notification channels, 50 members). For a single company that is all you need: add monitors there and invite colleagues under **Members → Invite someone** (an invitation link works while registration is off; see [invitations](runbooks/invitations.md)). Self-service registration (`config.registrationEnabled: true`) is only for letting other people create separate organizations (plan `free`); there is no email verification, so leave it off unless you want that.

### Installing from the OCI registry
Steps 1, 3 and 4 change; everything else is the same. Do not copy `values.example.yaml` from a checkout and do not set `image.tag`: the packaged chart uses its own release as the image tag.

Write a minimal `my-values.yaml` with the keys from the table in step 3 (everything else keeps the chart defaults):
```yaml
config:
  publicUrl: https://proreta.example.com
  trustedProxyHops: 1
database:
  host: postgres.example.internal
  sslmode: require
  bootstrap:
    enabled: true
    adminSecret:
      existingSecret: proreta-db-admin-login
httpRoute:
  parentRef:
    name: gateway
    namespace: gateway-system   # empty: the Proreta namespace
    sectionName: https          # optional: a single listener
```
Then install:
```sh
VERSION=0.1.0
helm upgrade --install proreta oci://ghcr.io/proreta-io/charts/proreta --version "$VERSION" -n proreta -f my-values.yaml --wait --timeout 10m
```
`helm show values oci://ghcr.io/proreta-io/charts/proreta --version "$VERSION"` prints every default with its comment, as a reference. For later upgrades pass the new `--version`.

### Installing with Argo CD
The chart works under Argo CD with the same order as Helm: the ServiceAccounts, Roles, bootstrap Jobs and the migration Job are hooks ordered by sync waves, the workloads come last. Tested with Argo CD 3.x, Envoy Gateway and Amazon RDS (PostgreSQL 16), from the git source below; installing from the OCI registry as an Argo CD source was not tested (it needs the registry registered in Argo CD as a Helm repository with OCI enabled).

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: proreta
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/proreta-io/proreta.git
    targetRevision: v0.1.0            # the release tag; chart and image tag come from the same version
    path: deploy/helm/proreta
    helm:
      releaseName: proreta            # Argo CD otherwise uses the Application name, which changes every resource name
      parameters:
        - name: image.tag
          value: "0.1.0"              # required from a git checkout; image tags have no leading v
      valuesObject:                   # the minimal values from "Installing from the OCI registry"
        config:
          publicUrl: https://proreta.example.com
          trustedProxyHops: 1
        database:
          host: postgres.example.internal
          bootstrap:
            enabled: true
            adminSecret:
              existingSecret: proreta-db-admin-login
        httpRoute:
          parentRef: {name: gateway, namespace: gateway-system, sectionName: https}
  destination:
    server: https://kubernetes.default.svc
    namespace: proreta
  syncPolicy:
    syncOptions: [CreateNamespace=true]
```

- Create the namespace yourself if you want Pod Security enforcement (`pod-security.kubernetes.io/enforce=restricted`; the pods satisfy it) instead of `CreateNamespace=true`.
- The database admin Secret (`proreta-db-admin-login`, keys `username` and `password`) can come from an ExternalSecret or any other source; it only has to exist before the first sync.
- If you do not set `helm.releaseName`, replace `proreta-` in the Secret and workload names used elsewhere in this guide with the Application name (for example `platform-proreta-admin-initial`).
- Turning the bootstrap off later is a second change to the Application (`bootstrap.enabled: false`) and a sync; only then remove the admin Secret ([After installing](#after-installing)).

### Trying it without a Gateway controller
For a local trial (kind, minikube) you can skip the Gateway controller, but the Gateway API CRDs are still required: the chart creates an `HTTPRoute`. Without a controller the route has no `status` and nothing serves the host name, yet the pods and the API work. Set `httpRoute.parentRef.name` to any name. The Services listen on port 8080 (not 80); reach them with port-forwards:
```sh
kubectl -n proreta port-forward svc/proreta-api 8080:8080     # the API
kubectl -n proreta port-forward svc/proreta-web 8081:8080     # the web app, http://localhost:8081
```
Over plain http the `Secure` session cookie is not kept, so for such a trial only, set `config.cookieSecure: false` (the cookie is then named `proreta_session` instead of `__Host-proreta_session`). Never do this in production. The web app expects `/api` on the same origin, so without a Gateway use the API directly with `curl`:
```sh
ADMIN_PASSWORD=$(kubectl -n proreta get secret proreta-admin-initial -o jsonpath='{.data.password}' | base64 -d)
curl -s -c jar -H 'Content-Type: application/json' \
  -d "{\"login\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  http://localhost:8080/api/v1/auth/login > session.json    # the answer contains "csrfToken"
CSRF=$(sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p' session.json)
curl -s -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"type":"http","name":"Example","http":{"url":"https://example.com/"}}' \
  http://localhost:8080/api/v1/monitors
```
Writes (POST, PUT, PATCH, DELETE) need the `X-CSRF-Token` header; the login itself does not. `projectId` is optional (the Default project). The first check runs within about 30 seconds; `curl -s -b jar http://localhost:8080/api/v1/monitors` shows the status. The API reference is [api/openapi.yaml](../api/openapi.yaml).

## After installing

**Delete the admin login Secret (recommended).** `database.bootstrap.enabled` may stay `true`: on every later `helm upgrade` the bootstrap Job checks whether the database is already set up (the Secret `proreta-db` exists, `proreta_owner` connects with its password, and the migrations have started) and, if so, skips itself without reading the admin login. The Job treats the admin Secret as optional, so deleting it does not stop the pod. The admin login is the database server's admin account, so remove it from the namespace once it is no longer needed:
```sh
kubectl -n proreta delete secret proreta-db-admin-login
```
The Secret `proreta-db` (the role passwords) stays; it is not managed by Helm. If a later run needs the login again (a new or empty database, a missing or outdated `proreta-db`), the Job fails with `the database is not ready and no database admin login is available`: create the admin Secret again (step 2) and rerun the upgrade. To rotate the role passwords or add a role on a working installation, set `database.bootstrap.force: true` together with the admin Secret, upgrade, then set it back to `false` ([database bootstrap](runbooks/database-bootstrap.md)). You can still set `database.bootstrap.enabled: false` after the first install if you prefer not to run the Job at all.

**Optional next steps:** Slack channels in the app, [sign-in with Google or GitHub](runbooks/oauth.md), [status pages](runbooks/status-page.md), [Alertmanager watchdog](runbooks/heartbeat-alertmanager.md), [metrics and alerts](runbooks/observability.md). Add the CIDRs of your cluster and VPC to `config.blockedCIDRs` so monitors cannot probe internal addresses ([chart README](../deploy/helm/proreta/README.md)).

## Upgrade
1. Read the release notes. **Back up the database** (snapshot or `pg_dump`) right before the upgrade: migrations are forward-only and the backup is the only way back ([migrations runbook](runbooks/migrations.md)).
2. Check out the new version of the chart and use the new image tag in `--set image.tag=...`. With the OCI chart pass the new `--version` only:
   ```sh
   helm upgrade --install proreta oci://ghcr.io/proreta-io/charts/proreta --version "$NEW_VERSION" -n proreta -f my-values.yaml --wait --timeout 10m
   ```
3. Run the same `helm upgrade --install ... --wait --timeout 10m`. The secrets bootstrap (a no-op when the Secrets exist) and the migration Job run first; then the Deployments roll with `maxUnavailable: 0`, so the old pods keep serving until the new ones are ready.

**Never roll the image back below the database schema.** api, worker and jobs refuse to start when the database has a migration their image does not know ("database schema is newer than this binary"). A `helm rollback` or an older `image.tag` across a migration therefore leaves the new pods crash-looping while the old ones keep running; roll forward instead, or restore the backup together with the old image.

## Uninstall
```sh
helm uninstall proreta -n proreta
```
This removes the Deployments, Services, HTTPRoute and ServiceAccounts. It leaves:
- the Secrets created by the bootstrap Jobs: `proreta-db`, `proreta-admin`, `proreta-crypto`, `proreta-statuspage` (and `proreta-admin-initial` if you did not delete it). Keep them to reinstall later with the same data: `proreta-crypto` holds the key that encrypts the Slack webhook URLs stored in the database;
- the finished Jobs, until `ttlSecondsAfterFinished` removes them (10 minutes for the bootstrap Jobs, 1 hour for the migration);
- the database and the four roles on your PostgreSQL server. There are no PersistentVolumeClaims.

To remove everything for good:
```sh
kubectl -n proreta delete secret proreta-db proreta-admin proreta-admin-initial proreta-crypto proreta-statuspage --ignore-not-found
kubectl delete namespace proreta
```
If you keep the namespace, also remove hook objects that a failed install left behind (after a successful run Helm deletes them itself; all chart objects carry the label `app.kubernetes.io/instance=<release>`):
```sh
kubectl -n proreta delete sa,role,rolebinding,job -l app.kubernetes.io/instance=proreta
```
and, as the admin login on the PostgreSQL server: `DROP DATABASE proreta;` then `DROP ROLE proreta_api, proreta_worker, proreta_statuspage, proreta_owner;`.

## Sizing
The defaults fit a single-company installation with a few hundred monitors on a small cluster: about 0.5 GiB of memory requests in total (plus 64 MiB for two web pods). Every container has requests and limits ([values.yaml](../deploy/helm/proreta/values.yaml)).

| Component | Default replicas | Requests (cpu/memory) | Scaling |
|---|---|---|---|
| `api` | 2 | 50m / 96Mi | Stateless; add replicas for load. Login and status page rate limits are counted per replica. |
| `worker` | 2 | 50m / 96Mi | Runs the checks. Replicas share the due checks through database leases (`FOR UPDATE SKIP LOCKED`), so more replicas add capacity; also see `worker.concurrency`. |
| `jobs` | 1 | 25m / 64Mi | Slack delivery and housekeeping. The chart allows only 0 or 1; keep 1 (0 stops alerts). |
| `statuspage` | 1 | 10m / 48Mi | Caches per replica; more replicas multiply the per-replica rate limits. |
| `web` | 2 | 10m / 32Mi | Static files served by nginx. |

Two replicas of `api` and `worker` keep the service up during node drains; the `api.pdb` and `topologySpread` values (off by default) help on multi-node clusters. Requests are small on purpose; raise them deliberately when the metrics show pressure.

## Troubleshooting

### Helm fails with `pre-install hooks failed` (or `pre-upgrade hooks failed`)
 One of the Jobs did not finish. The last line of the message tells how:
- `job proreta-db-bootstrap failed: BackoffLimitExceeded` (or `proreta-secrets-bootstrap`, `proreta-migrate`): the container ran and exited with an error three times (`backoffLimit: 2`). The Job's logs say why.
- `job ... failed: DeadlineExceeded`: the Job did not finish within `activeDeadlineSeconds` (300 s for the bootstrap Jobs, 600 s for the migration). Usually the pod never started: a missing Secret the Job needs (the admin login Secret is optional, so it is not the usual cause) or an image that cannot be pulled.
- `timed out waiting for the condition`: Helm's `--timeout` ran out first, for the same reasons. Use `--timeout 10m` so the Job's own limit decides.

Find which Job and why:
```sh
kubectl -n proreta get jobs,pods
kubectl -n proreta get job proreta-db-bootstrap -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl -n proreta describe pod -l app.kubernetes.io/component=db-bootstrap   # or secrets-bootstrap, migrate
kubectl -n proreta logs job/proreta-db-bootstrap
```
After fixing the cause, run the same `helm upgrade --install` again; a failed first install is retried in place (tested), and the failed Job is replaced.

### `another operation (install/upgrade/rollback) is in progress`

A previous `helm` command was interrupted (Ctrl-C, closed terminal, lost connection) while it waited for the hooks, and the release is stuck in a `pending-*` state. Check with `helm -n proreta history proreta`, then:
- last status `pending-install` (first install): `helm -n proreta uninstall proreta`, then install again. This keeps the Secrets generated by the bootstrap Jobs and the database, so nothing is lost.
- last status `pending-upgrade`: `helm -n proreta rollback proreta` (to the last `deployed` revision; no migration runs on a rollback), then upgrade again.

This recovery is the generic Helm procedure. It was tried against this chart only for an install interrupted after its hook Jobs had finished; an interrupted upgrade (`pending-upgrade`, `helm rollback`) was not tried. Wait until no `helm` process is still running for the release before you use it.

### `ImagePullBackOff` / `ErrImagePull`
The first pod to fail is usually `proreta-db-bootstrap`. `kubectl -n proreta describe pod <pod>` shows the message under Events:
- `not found` / `failed to resolve reference`: the tag does not exist. Check `image.tag` (release tags have no `v`; builds of `main` are `sha-` plus 7 hex digits) against the package list on GitHub, or `docker manifest inspect ghcr.io/proreta-io/proreta-backend:<tag>`.
- `unauthorized` / `denied`: the package is private (typical for images built from a fork). Make it public in the package settings on GitHub, or create a pull Secret and set `imagePullSecrets: [{name: <secret>}]`.

### The database bootstrap cannot connect or is not allowed
`kubectl -n proreta logs job/proreta-db-bootstrap` names the cause:
- `failed SASL auth: FATAL: password authentication failed`: wrong `username` or `password` in the admin Secret. Recreate it (step 2), then rerun the upgrade.
- `tls error: server refused TLS connection`: the server has no TLS; set `database.sslmode: disable` (only on a trusted network) or enable TLS on the server.
- `no pg_hba.conf entry`, `i/o timeout`, `connection refused`, `no such host`: the pod cannot reach the server. Check `database.host` and `database.port`, DNS, security groups or firewall rules, `pg_hba.conf`, and any NetworkPolicy in the namespace.
- `permission denied to create role` or `permission denied to create database`: the admin login lacks `CREATEROLE` or `CREATEDB`.
- `the database is not ready and no database admin login is available`: the database has to be set up (the log names why: `proreta-db` missing or outdated, `proreta_owner` cannot connect, or no migration table yet) and the admin Secret named in `database.bootstrap.adminSecret.existingSecret` does not exist in the namespace or lacks the keys `username` and `password` (or the names set in `usernameKey` / `passwordKey`). Create it (step 2) and rerun the upgrade.

### The migration or the bootstrap fails because of an extension
- `extension "citext" is not available` or `not allow-listed` in the bootstrap log: install the contrib modules on a self-hosted server, or allow-list `CITEXT` (Azure: server parameter `azure.extensions`), then rerun.
- `type "citext" does not exist` in `kubectl -n proreta logs job/proreta-migrate`: the database was created without the bootstrap. Create the extension as the database owner (`CREATE EXTENSION citext;` in database `proreta`), or run the bootstrap once.
- `permission denied for schema public`: the database or schema is not owned by `proreta_owner`; run the bootstrap once, it repairs both.

### The pods run but the site does not answer: the HTTPRoute is not accepted
```sh
kubectl -n proreta get httproute proreta -o jsonpath='{range .status.parents[*].conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl -n <gateway namespace> get gateway <gateway> -o yaml    # listeners, hostnames, allowedRoutes, status
```
- No `status` at all: no controller handles this Gateway (check its `gatewayClassName` and the controller), or `httpRoute.parentRef` names a Gateway that does not exist.
- `NoMatchingParent`: `httpRoute.parentRef.sectionName` is not a listener of the Gateway, or the listener's `hostname` does not match the route's host names (`httpRoute.hostnames`, by default the host of `config.publicUrl`).
- `NotAllowedByListeners`: the listener's `allowedRoutes` does not admit the `proreta` namespace.
- `Accepted=True` but no answer: the DNS name does not point at the Gateway's address, or the listener's certificate Secret is missing (see the Gateway's listener conditions).

### Pods crash with `database schema is newer than this binary`
`kubectl -n proreta logs deploy/proreta-api` (or `-worker`, `-jobs`) shows `database at migration N, binary knows up to M`. The image is older than the database, usually after a rollback or a typo in `image.tag`. Set `image.tag` to a release that contains migration N (the current one or newer) and upgrade. The old pods keep serving meanwhile.

### OAuth sign-in fails with a redirect URI mismatch
The provider shows `redirect_uri_mismatch` (Google) or "The redirect_uri is not associated with this application" (GitHub). The registered URI must be exactly `<config.publicUrl>/api/v1/auth/oauth/google/callback` (or `.../github/callback`): https, the same host, no trailing slash. Compare with `helm get values proreta -n proreta`. Details: [OAuth runbook](runbooks/oauth.md).

### Other first-day problems
- **The schema check rejects my values** (`values don't meet the specifications of the schema`): the line `at '/config/publicUrl'` (for example) names the key; its description is in `values.schema.json` and [values.yaml](../deploy/helm/proreta/values.yaml).
- **Sign-in "works" but you are logged out at once:** the site is served over http. The session cookie is `Secure`; use the Gateway's HTTPS listener.
- **Every visitor shares one rate limit:** `config.trustedProxyHops` is `0` behind a Gateway; set `1`.

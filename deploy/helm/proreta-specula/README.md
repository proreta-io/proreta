# Proreta Specula chart

Installs the **Specula executor** in a Kubernetes cluster (or any namespace) inside your own network. It checks services that have no public address and reports to Proreta over HTTPS; it accepts no inbound connections and has no database. See the [Specula runbook](../../../docs/runbooks/specula.md).

## Install
1. In Proreta, open **Specula → Register a Specula** (owners and maintainers), name it, choose who can use it and copy the token. It is shown once.
2. Put the token in a Secret in the namespace where the executor will run, without leaving it in your shell history (paste it, then press Ctrl-D):
   ```sh
   kubectl create namespace proreta-specula
   kubectl -n proreta-specula create secret generic proreta-specula-token --from-file=token=/dev/stdin
   ```
3. Install the chart:
   ```sh
   helm upgrade --install specula oci://ghcr.io/proreta-io/charts/proreta-specula --version <release> \
     --namespace proreta-specula \
     --set proreta.url=https://proreta.example.com \
     --set token.existingSecret=proreta-specula-token
   ```
   The image tag defaults to the chart's version, so a released chart installs the matching executor.
4. In Proreta the Specula turns **Online** within a minute. Choose it under **Run from** when you add a monitor.

The pods must reach `proreta.url` over HTTPS and the services you want to check. Nothing needs to reach the pods.

## Values
| Value | Default | Meaning |
|---|---|---|
| `proreta.url` | none, required | Your Proreta address (`PRORETA_URL`). |
| `token.existingSecret`, `token.key` | none, required; `token` | The Secret that holds the token. |
| `replicas` | `2` | Pods sharing the token; a check is leased to one at a time. |
| `specula.concurrency` | `20` | Checks in flight per pod. |
| `specula.pollInterval` | `5s` | How often a pod asks for work (1s to 1m). |
| `specula.logLevel`, `specula.adminPort` | `info`, `9090` | Logging and the health/metrics port. |
| `image.repository`, `image.tag` | `ghcr.io/proreta-io/proreta-backend`, chart appVersion | The image holds `/app/specula`. Set `image.tag` when installing from a git checkout. |
| `env` | `[]` | Extra environment variables, for example `HTTPS_PROXY` for the call to Proreta. Checks never use a proxy. |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, `podLabels`, `imagePullSecrets` | | Usual Kubernetes settings. |

The pod runs as a non-root user with a read-only root file system, no capabilities and no Kubernetes API token. Unknown values are rejected (`values.schema.json`).

## Rotating or revoking
Register a new Specula, update the Secret and restart the Deployment, then revoke the old one in Proreta. A revoked token makes the executor stop with an error; the Deployment then restarts in a loop until the Secret is replaced or the release is removed.

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
| `replicas` | `1` | One pod per Specula (`0` stops it). A second pod with the same token would run every check twice, so values above 1 are rejected. The Deployment replaces the pod with the `Recreate` strategy. |
| `state.enabled`, `state.mountPath`, `state.sizeLimit` | `true`, `/var/lib/specula`, `512Mi` | The directory (`PRORETA_SPECULA_STATE_DIR`) where the executor keeps its schedule and the results it could not send yet, an `emptyDir` of that size. `enabled: false` mounts nothing and the executor works from memory only. |
| `specula.concurrency` | `20` | Checks in flight per pod. |
| `specula.pollInterval` | `5s` | How often a pod asks for work (1s to 1m). |
| `specula.logLevel`, `specula.adminPort` | `info`, `9090` | Logging and the health/metrics port. |
| `serviceMonitor.enabled`, `.interval`, `.path`, `.labels` | `false`, `30s`, `/metrics`, `{}` | A Service and a Prometheus Operator ServiceMonitor for the admin port (needs the Operator CRDs) |
| `image.repository`, `image.tag` | `ghcr.io/proreta-io/proreta-specula`, chart appVersion | The image holds only the executor binary. Set `image.tag` when installing from a git checkout. |
| `env` | `[]` | Extra environment variables, for example `HTTPS_PROXY` for the call to Proreta. Checks never use a proxy. |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, `podLabels`, `imagePullSecrets` | | Usual Kubernetes settings. |

## State and what it survives
The executor runs checks from a schedule it caches, and keeps results it cannot deliver in a buffer, both in the state directory. With the default `emptyDir` they survive a restart of the container (a crash, an out-of-memory kill) but **not the replacement of the pod** (an upgrade, an eviction, a node drain). If the pod is replaced while the link to Proreta is down, the buffered results and the cached schedule are lost and the executor checks nothing until it can reach Proreta again; when the link is up a replacement is safe, because the executor sends what it has when it is told to stop. A volume that outlives the pod is not offered yet. With `state.enabled: false` nothing is written to disk, which also keeps the decrypted monitor settings (headers and bodies may hold tokens) out of the file system, but a restart while Proreta is unreachable stops the checks. See the [Specula runbook](../../../docs/runbooks/specula.md) for the buffer, the outage behaviour and the upgrade.

The pod runs as a non-root user with a read-only root file system (only `/tmp` and the state directory are writable), no capabilities and no Kubernetes API token. Unknown values are rejected (`values.schema.json`).

## Rotating or revoking
Register a new Specula, update the Secret and restart the Deployment, then revoke the old one in Proreta. A revoked token makes the executor stop with an error; the Deployment then restarts in a loop until the Secret is replaced or the release is removed.

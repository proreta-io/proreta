# Proreta

Proreta is a multi-tenant uptime-monitoring service you can run yourself.

- **Uptime checks:** external HTTPS checks with expected status codes, request method, headers and body, and TCP port checks, with response times and history, run through an SSRF-hardened client. A **Specula** executor checks services inside your own network ([docs](docs/runbooks/specula.md)).
- **Incidents:** a monitor that keeps failing opens an incident; recovery resolves it. Every state change and its notification are written in one transaction.
- **Alerts:** Slack and generic webhook notification channels (a message template of your own) per project or per organization, with reminders while an incident stays open.
- **Watchdog heartbeats:** a dead man's switch URL, for example for the always-firing `Watchdog` alert of Prometheus Alertmanager. When the pings stop, Proreta raises an incident.
- **Status pages:** public, optionally password-protected pages per organization, with an RSS feed, optionally on the customer's own domain ([docs](docs/runbooks/custom-domains.md)).
- **Organizations, projects and roles:** owners, maintainers, members and viewers on the organization and on each project (a project role raises an organization role inside that project); people can belong to an organization only through projects; projects scope monitors and channels; invitations by link. See [docs/roles.md](docs/roles.md).
- **Sign-in:** password, Google and GitHub (OAuth), with optional Google Workspace domain auto-join.
- **Platform admin panel:** organizations, plans and limits, suspension, users, Specula executors, email settings, password resets ([docs](docs/runbooks/admin-panel.md)). A self-hosted installation has one plan, `internal`, with no limits and every feature.

## Releases

See the [releases](https://github.com/proreta-io/proreta/releases) and the [release notes](docs/release-notes/) of each version for what changed and what to do when upgrading.

### Versioning

Proreta follows semantic versioning. The API contract ([api/openapi.yaml](api/openapi.yaml)) is version 1.0 and changes only in compatible ways, except for operations marked `x-experimental` (the platform admin API). Database migrations are forward-only: take a backup before every upgrade ([install guide](docs/install.md#upgrade)). The Helm chart version always equals the application version.

## Layout

| Path | What |
|---|---|
| `backend/` | Go modular monolith: `cmd/api`, `cmd/worker`, `cmd/jobs`, `cmd/statuspage`, `cmd/migrate`, `cmd/proretactl`, and the Specula gateway and executor (`cmd/speculagw`, `cmd/specula`) |
| `backend/migrations/` | PostgreSQL migrations (goose), embedded in the `migrate` binary |
| `api/openapi.yaml` | API contract, the source of truth for the Go server and the TypeScript client |
| `web/` | React single-page app (Vite, TypeScript) |
| `deploy/helm/proreta/` | Helm chart; `deploy/helm/proreta-specula/` is the chart of the Specula executor |
| `docs/` | Operator documentation ([index](docs/README.md)) |

## Installing

Proreta runs on Kubernetes with the Helm chart in [deploy/helm/proreta](deploy/helm/proreta/README.md). Follow the **[installation guide](docs/install.md)**: prerequisites, the exact commands, upgrades, uninstalling and troubleshooting.

Prerequisites in short:
- Kubernetes 1.29 or newer, `amd64` or `arm64` nodes, Helm 3.
- **Gateway API only:** the Gateway API CRDs (v1.2 or newer), a Gateway controller and a Gateway with an HTTPS listener (TLS is the Gateway's job). The chart creates an `HTTPRoute`; Ingress is not supported.
- An external PostgreSQL 15 or newer and a login with `CREATEROLE` and `CREATEDB` for the one-time bootstrap (no superuser needed; only the `citext` extension is used). A bundled database is not part of the chart.
- Nothing else: no External Secrets Operator, cert-manager or metrics-server is required.

The images are `ghcr.io/proreta-io/proreta-backend` and `ghcr.io/proreta-io/proreta-web` (multi-arch, `amd64` and `arm64`). Every release tag (`X.Y.Z`) is published for both, and the chart is published as `oci://ghcr.io/proreta-io/charts/proreta` with the same version.

After the install, the platform admin signs in with a generated one-time password and owns the organization "Default"; colleagues join by invitation link. Optional: [OAuth sign-in](docs/runbooks/oauth.md), [status pages](docs/runbooks/status-page.md), [Alertmanager watchdog](docs/runbooks/heartbeat-alertmanager.md), [metrics and alerts](docs/runbooks/observability.md).

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for the local setup (Go, Node, PostgreSQL), the `make` targets, tests and generated code.

## Security

Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## License

Proreta is licensed under the [Apache License, Version 2.0](LICENSE). Third-party notices are in [NOTICE](NOTICE). The name and logo are not covered by the license; see [TRADEMARK.md](TRADEMARK.md).

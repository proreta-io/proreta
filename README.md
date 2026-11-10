# Proreta

Proreta is a multi-tenant uptime-monitoring service you can run yourself.

- **Uptime checks:** external HTTPS checks with expected status codes, response times and history, run through an SSRF-hardened HTTP client.
- **Incidents:** a monitor that keeps failing opens an incident; recovery resolves it. Every state change and its notification are written in one transaction.
- **Alerts:** Slack notification channels per project or per organization, with reminders while an incident stays open.
- **Watchdog heartbeats:** a dead man's switch URL, for example for the always-firing `Watchdog` alert of Prometheus Alertmanager. When the pings stop, Proreta raises an incident.
- **Status pages:** public, optionally password-protected pages per organization, with an RSS feed.
- **Organizations, projects and roles:** owners, admins, members and viewers; projects scope monitors and channels; invitations by link.
- **Sign-in:** password, Google and GitHub (OAuth), with optional Google Workspace domain auto-join.
- **Platform admin panel:** plans and limits, suspension, password resets.

## Status

Early development. The API and the database schema may still change between releases. There is no stable release yet.

### Versioning

Proreta follows semantic versioning. While the major version is 0, a minor release (0.x.0) may contain breaking changes to the API, the configuration or the database schema, and a patch release (0.x.y) does not. The Helm chart version always equals the application version.

## Layout

| Path | What |
|---|---|
| `backend/` | Go modular monolith: `cmd/api`, `cmd/worker`, `cmd/jobs`, `cmd/statuspage`, `cmd/migrate`, `cmd/proretactl` |
| `backend/migrations/` | PostgreSQL migrations (goose), embedded in the `migrate` binary |
| `api/openapi.yaml` | API contract, the source of truth for the Go server and the TypeScript client |
| `web/` | React single-page app (Vite, TypeScript) |
| `deploy/helm/proreta/` | Helm chart |
| `docs/` | Operator documentation ([index](docs/README.md)) |

## Installing

Proreta runs on Kubernetes with the Helm chart in [deploy/helm/proreta](deploy/helm/proreta/README.md). Follow the **[installation guide](docs/install.md)**: prerequisites, the exact commands, upgrades, uninstalling and troubleshooting.

Prerequisites in short:
- Kubernetes 1.29 or newer, `amd64` or `arm64` nodes, Helm 3.
- **Gateway API only for now:** the Gateway API CRDs (v1.2 or newer), a Gateway controller and a Gateway with an HTTPS listener (TLS is the Gateway's job). The chart creates an `HTTPRoute`; Ingress is not supported yet.
- An external PostgreSQL 15 or newer and a login with `CREATEROLE` and `CREATEDB` for the one-time bootstrap (no superuser needed; only the `citext` extension is used). A bundled database is not supported yet.
- Nothing else: no External Secrets Operator, cert-manager or metrics-server is required.

The images are `ghcr.io/proreta-io/proreta-backend` and `ghcr.io/proreta-io/proreta-web` (multi-arch, `amd64` and `arm64`). There is no published release yet: until there is, install from a git checkout with an image tag that exists, as described in the guide. Releases will also publish the chart as `oci://ghcr.io/proreta-io/charts/proreta`.

After the install, the platform admin signs in with a generated one-time password and owns the organization "Default"; colleagues join by invitation link. Optional: [OAuth sign-in](docs/runbooks/oauth.md), [status pages](docs/runbooks/status-page.md), [Alertmanager watchdog](docs/runbooks/heartbeat-alertmanager.md), [metrics and alerts](docs/runbooks/observability.md).

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for the local setup (Go, Node, PostgreSQL), the `make` targets, tests and generated code.

## Security

Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## License

Proreta is licensed under the [Apache License, Version 2.0](LICENSE). Third-party notices are in [NOTICE](NOTICE). The name and logo are not covered by the license; see [TRADEMARK.md](TRADEMARK.md).

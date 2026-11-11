# Documentation

Operator and contributor documentation for Proreta.

## Getting started
- [Project overview](../README.md)
- [Installation guide](install.md): prerequisites, install, upgrade, uninstall, troubleshooting
- [Helm chart](../deploy/helm/proreta/README.md) and its [example values](../deploy/helm/proreta/values.example.yaml)
- [Running locally](development.md): throwaway database in Docker, all processes with `go run`, no Kubernetes
- [Contributing](../CONTRIBUTING.md): development setup, conventions, tests, migrations

## Runbooks
| Runbook | Covers |
|---|---|
| [Database bootstrap](runbooks/database-bootstrap.md) | Creating the database and roles, the bootstrap Job, manual fallback |
| [Database migrations](runbooks/migrations.md) | Writing migrations, what the tests enforce, rollouts and rollbacks |
| [Platform admin panel](runbooks/admin-panel.md) | Plans, suspension, deletion, users, members, projects, registration switch, password resets, audit |
| [Members, roles and invitations](runbooks/invitations.md) | Roles, invitation links, organization switcher, recovery |
| [Sign-in with Google and GitHub](runbooks/oauth.md) | OAuth clients, redirect URIs, Workspace domains, OAuth-only mode |
| [Outgoing email](runbooks/email.md) | Optional SMTP, set up in Admin → Email: settings, what is sent, delivery and troubleshooting |
| [Public status page](runbooks/status-page.md) | Enabling the renderer, caching, protected pages, abuse |
| [Heartbeats with Alertmanager](runbooks/heartbeat-alertmanager.md) | Watchdog heartbeats for Prometheus Alertmanager |
| [Observability](runbooks/observability.md) | Metrics, logs and suggested alert rules |

## API
The API contract is [api/openapi.yaml](../api/openapi.yaml) (OpenAPI 3.0). The Go server and the web client are generated from it.

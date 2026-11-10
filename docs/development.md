# Running Proreta locally

Everything runs on your machine without Kubernetes: a throwaway PostgreSQL in Docker, the Go processes with `go run`, and the web dev server, which proxies `/api` and `/status`. This is for development and trials only.

## Requirements
- Docker, Go (the version in `backend/go.mod`), Node.js 22 and npm, `openssl`.
- Free ports: 55432 (PostgreSQL), 8080 (api), 8081 (status page), 5173 (web) and 9090-9093 (admin ports of the four processes).

## 1. Database
```sh
make dev-db           # DEV_WEB_PORT=5180 make dev-db  if 5173 is taken
make dev-migrate
```
`scripts/dev-db.sh up` starts `postgres:16` on `127.0.0.1:55432`, then runs `proretactl db bootstrap` against it, the same code the chart's bootstrap Job uses, so the database, the four roles (`proreta_owner`, `proreta_api`, `proreta_worker`, `proreta_statuspage`), their limits and grants, and the `citext` extension are identical to an installation. Only the place of the credentials differs: `PRORETA_BOOTSTRAP_SECRET_FILE` makes it write them to `.dev/db.secret` instead of a Kubernetes Secret.

It also writes `.dev/env` (database URLs, a fresh encryption key and status page cookie key, the argon2id hash of the first admin's password, `PRORETA_PUBLIC_URL=http://localhost:5173` and `PRORETA_COOKIE_SECURE=false`) and `.dev/admin-login` (login `admin` and a random password). `.dev/` is ignored by Git. Set `DEV_ADMIN_PASSWORD` to choose the password yourself, `DEV_DB_PORT` for another database port.

To hash a password of your own for `PRORETA_ADMIN_PASSWORD_HASH` in any setup: `printf '%s\n' 'my password' | go -C backend run ./cmd/proretactl hashpw`.

## 2. Processes
Each target runs one process in the foreground with the settings of `.dev/env`. Use one terminal each, or let make run them together:
```sh
make -j5 -O dev-api dev-worker dev-jobs dev-statuspage dev-web
```
Open <http://localhost:5173> and sign in with the login and password from `.dev/admin-login`. The admin is the owner of the organization **Default**; add a monitor with a public `https://` URL and the first check runs within about 30 seconds. The worker's SSRF guard refuses private addresses, so a monitor for a service on your own machine is rejected.

Without a browser:
```sh
B=http://localhost:5173
curl -s -c .dev/jar -H 'Content-Type: application/json' -d "{\"login\":\"admin\",\"password\":\"$(sed -n 's/^password: //p' .dev/admin-login)\"}" $B/api/v1/auth/login > .dev/session.json
CSRF=$(sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p' .dev/session.json)
curl -s -b .dev/jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"type":"http","name":"Example","http":{"url":"https://example.com/"}}' $B/api/v1/monitors
curl -s -b .dev/jar $B/api/v1/monitors
```

## Plain http on localhost
`PRORETA_PUBLIC_URL` must be https, with one exception: `http://localhost`, `http://127.0.0.1` and `http://[::1]` (with or without a port) are accepted for local development. Because browsers and `curl` do not send a `Secure` cookie back over plain http, the api and the status page renderer then refuse to start unless `PRORETA_COOKIE_SECURE=false` is set explicitly, and the session cookie is named `proreta_session` instead of `__Host-proreta_session`. Never use this in production; `.dev/env` sets both values for you. The Helm chart accepts the same pair (`config.publicUrl: http://localhost:8081` with `config.cookieSecure: false`) for a trial on a local cluster, see [Trying it without a Gateway controller](install.md#trying-it-without-a-gateway-controller).

## Clean up
Stop the processes with Ctrl-C, then:
```sh
make dev-db-down      # removes the container, its data and .dev/
```

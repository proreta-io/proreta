# Backend developer and CI entry points. Generators are pinned in backend/tools/go.mod and run
# with `go tool`, so nothing has to be installed globally (golangci-lint excepted, see lint).
ROOT    := $(abspath .)
BACKEND := $(ROOT)/backend
TOOLS   := go -C $(BACKEND)/tools tool

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Paths written by `generate`; check-generated fails if they drift from what is committed.
GENERATED := backend/internal/api/gen backend/internal/auth/authdb backend/internal/tenants/tenantsdb backend/internal/audit/auditdb \
	backend/internal/scheduler/schedulerdb backend/internal/monitors/monitorsdb backend/internal/incidents/incidentsdb backend/internal/results/resultsdb \
	backend/internal/notify/notifydb backend/internal/plans/plansdb backend/internal/projects/projectsdb \
	backend/internal/statuspages/statuspagesdb

.PHONY: generate check-generated lint test test-e2e build tidy dev-db dev-db-down dev-migrate dev-api dev-worker dev-jobs dev-statuspage dev-speculagw dev-specula dev-web

generate:
	$(TOOLS) sqlc generate -f $(ROOT)/sqlc.yaml
	$(TOOLS) oapi-codegen -config $(BACKEND)/internal/api/gen/oapi-codegen.yaml \
		-o $(BACKEND)/internal/api/gen/api.gen.go $(ROOT)/api/openapi.yaml

check-generated: generate
	@if [ -n "$$(git status --porcelain -- $(GENERATED))" ]; then \
		echo "generated code is out of date; run 'make generate' and commit:"; \
		git status --porcelain -- $(GENERATED); \
		git --no-pager diff --stat -- $(GENERATED); \
		exit 1; \
	fi

# Integration tests run when TEST_DATABASE_ADMIN_URL points at a scratch Postgres superuser.
test:
	cd $(BACKEND) && go test -race -count=1 ./...

# End-to-end test: api, worker and jobs against real Postgres and loopback TLS servers
# through the real SSRF guard. Only this target sets the proretatest tag, which compiles
# safehttp.NewLoopbackTestClient; release builds never do. Skipped without TEST_DATABASE_ADMIN_URL.
test-e2e:
	cd $(BACKEND) && go test -tags proretatest -race -count=1 ./internal/e2e/...

# CI pins golangci-lint v2.15.0 (.github/workflows/ci.yml, job backend-lint); use the same locally.
lint:
	cd $(BACKEND) && golangci-lint run ./...

build:
	cd $(BACKEND) && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ \
		./cmd/api ./cmd/worker ./cmd/jobs ./cmd/migrate ./cmd/proretactl ./cmd/statuspage

tidy:
	cd $(BACKEND) && go mod tidy
	cd $(BACKEND)/tools && go mod tidy

# Local development without Kubernetes, see docs/development.md. dev-db starts a throwaway PostgreSQL
# in Docker and writes .dev/env; every other dev-* target runs one process with it, in the foreground.
DEV_ENV := set -a && . $(ROOT)/.dev/env && set +a

dev-db:
	scripts/dev-db.sh up

dev-db-down:
	scripts/dev-db.sh down

dev-migrate:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_MIGRATE_DATABASE_URL=$$DEV_OWNER_URL go run ./cmd/migrate

dev-api:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_DATABASE_URL=$$DEV_API_URL PRORETA_HTTP_ADDR=127.0.0.1:8080 PRORETA_ADMIN_ADDR=127.0.0.1:9090 go run ./cmd/api

dev-worker:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_DATABASE_URL=$$DEV_WORKER_URL PRORETA_ADMIN_ADDR=127.0.0.1:9091 go run ./cmd/worker

dev-jobs:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_DATABASE_URL=$$DEV_WORKER_URL PRORETA_ADMIN_ADDR=127.0.0.1:9092 go run ./cmd/jobs

dev-statuspage:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_DATABASE_URL=$$DEV_STATUSPAGE_URL PRORETA_HTTP_ADDR=127.0.0.1:8081 PRORETA_ADMIN_ADDR=127.0.0.1:9093 go run ./cmd/statuspage

# The Specula gateway and an executor against it. Register a Specula in the app first, then run
# `make dev-specula SPECULA_TOKEN=spc_...`.
dev-speculagw:
	$(DEV_ENV) && cd $(BACKEND) && PRORETA_DATABASE_URL=$$DEV_WORKER_URL PRORETA_HTTP_ADDR=127.0.0.1:8082 PRORETA_ADMIN_ADDR=127.0.0.1:9094 go run ./cmd/speculagw

dev-specula:
	@test -n "$(SPECULA_TOKEN)" || { echo "usage: make dev-specula SPECULA_TOKEN=spc_..."; exit 1; }
	cd $(BACKEND) && PRORETA_URL=http://127.0.0.1:8082 PRORETA_SPECULA_TOKEN=$(SPECULA_TOKEN) PRORETA_ADMIN_ADDR=127.0.0.1:9095 go run ./cmd/specula

dev-web:
	$(DEV_ENV) && cd $(ROOT)/web && { [ -d node_modules ] || npm ci; } && npm run dev

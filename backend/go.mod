module github.com/proreta-io/proreta/backend

go 1.26.0

// The standard library of 1.26.0 carries known vulnerabilities (govulncheck in CI); keep this in step with the golang image tag in the Dockerfile.
toolchain go1.26.11

require (
	github.com/getkin/kin-openapi v0.153.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/oapi-codegen/nethttp-middleware v1.3.1
	github.com/oapi-codegen/runtime v1.9.0
	github.com/pressly/goose/v3 v3.30.1
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/client_model v0.6.3
	golang.org/x/crypto v0.59.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.25.0
	golang.org/x/term v0.48.0
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.72.0 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

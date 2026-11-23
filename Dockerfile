# syntax=docker/dockerfile:1.7
# One image with the backend binaries in /app (the Specula executor has its own, Dockerfile.specula); the chart picks the command per workload.
# Base images are pinned by multi-arch index digest; Dependabot (docker) bumps tag and digest together.
# Multi-arch: the Go toolchain runs on the build platform and cross-compiles (no emulation).

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/ ./cmd/api ./cmd/worker ./cmd/jobs ./cmd/migrate ./cmd/proretactl ./cmd/statuspage ./cmd/speculagw

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /app/
USER 65532:65532
CMD ["/app/api"]

# syntax=docker/dockerfile:1

# ---- build stage: compile static Go binaries ----
# Runs on the build machine's own platform and cross-compiles for the target
# (Go needs no emulation for that), so multi-arch release builds stay fast.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS builder
WORKDIR /src

# Copy module files first so dependency download is cached across code changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
# CGO disabled -> fully static binaries that run on distroless/static.
# -trimpath and -buildvcs=false make builds reproducible across machines.
RUN for cmd in api worker migrate seed; do \
      CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.version=${VERSION}" -o /out/$cmd ./cmd/$cmd || exit 1; \
    done

# ---- worker-builder: DEVELOPMENT ONLY ----
# The worker with git + docker CLIs, for an in-cluster worker that builds
# images through the node's Docker socket. Socket access is root-equivalent
# on the node, so this image runs as root and must never be used in
# production (the Helm chart refuses it there). See ADR-008.
FROM alpine:3.24 AS worker-builder
RUN apk add --no-cache git docker-cli docker-cli-buildx ca-certificates
COPY --from=builder /out/worker /worker
ENTRYPOINT ["/worker"]

# ---- runtime (default): no shell, no package manager, non-root ----
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=builder /out/api /out/worker /out/migrate /out/seed /
USER nonroot:nonroot
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl. Health is probed by
# docker-compose (via the host) and by Kubernetes liveness/readiness probes.
ENTRYPOINT ["/api"]

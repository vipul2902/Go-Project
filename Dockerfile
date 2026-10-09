# syntax=docker/dockerfile:1

# ---- build stage: compile a static Go binary ----
FROM golang:1.27-alpine AS builder
WORKDIR /src

# Copy module files first so dependency download is cached across code changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO disabled -> fully static binary that runs on distroless/static.
# -trimpath and -buildvcs=false make builds reproducible across machines.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/migrate ./cmd/migrate

# ---- runtime stage: no shell, no package manager, non-root ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/api /out/migrate /
USER nonroot:nonroot
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl. Health is probed by
# docker-compose (via the host) and by Kubernetes liveness/readiness probes.
ENTRYPOINT ["/api"]

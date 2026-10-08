# ADR-001: Go for the backend

- **Status:** Accepted
- **Date:** 2026-10-09

## Context
CloudDeploy is a control plane. It talks to Kubernetes, Docker registries and cloud APIs, and it runs long-lived concurrent deployment jobs.

## Decision
The backend is written in Go, routing uses the standard library's `net/http` and logging uses `log/slog`.

## Rationale
- Kubernetes, Helm, Terraform and Docker are all written in Go. The official Kubernetes client (`client-go`) is a Go library.
- Goroutines plus `context` cancellation are a good fit for bounded worker pools and per-deployment timeouts.
- Go compiles to a single static binary. The runtime image is about 15 MB on distroless, with no shell.
- Since Go 1.22, `ServeMux` supports method and path-parameter patterns, so we don't need a router dependency.

## Consequences
Error handling is more verbose than in exception-based languages. Go's generics are deliberately limited.

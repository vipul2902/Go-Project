# Local Development

## Resource budget

The reference machine is an M1 MacBook Air with 8 GB RAM, running Colima with 4 CPUs and 4 GB RAM. The rules:

- Run the Go API natively (`make dev`) while iterating. Use containers only for its dependencies.
- Docker Compose services are grouped into profiles. `core` holds the API, PostgreSQL and Redis. `observability` holds Prometheus and Grafana and is opt-in.
- Stop unrelated containers before starting Kubernetes, which arrives in Phase 10.

## Toolchain status (audited 2026-10-09)

| Tool | Status |
|---|---|
| Go | 1.27.1 (Homebrew) |
| Docker / Colima | Colima 0.10.3, docker runtime, aarch64 |
| Docker Compose plugin | **Broken.** The `~/.docker/cli-plugins/docker-compose` symlink points to a removed Docker Desktop install. |
| psql | 14.14 (client only) |
| gh | 2.96.0, authenticated |
| kubectl, Helm, Terraform | Not installed. They will be added in Phases 10, 11 and 17. |

### Fixing the Compose plugin

```bash
brew install docker-compose
rm ~/.docker/cli-plugins/docker-compose          # dangling Docker Desktop link
ln -s "$(brew --prefix)/opt/docker-compose/bin/docker-compose" ~/.docker/cli-plugins/docker-compose
docker compose version
```

## Graceful shutdown

When the API receives SIGTERM, it stops accepting new connections and waits up to `SHUTDOWN_TIMEOUT` for in-flight requests to finish before it exits. Kubernetes sends SIGTERM during rolling updates, so this behaviour prevents dropped requests during a deploy.

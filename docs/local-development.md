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
| Docker Compose plugin | 5.6.0 (Homebrew). The symlink was repaired on 2026-10-09; it had pointed to a removed Docker Desktop install. |
| Native PostgreSQL | Versions 16 and 17 are installed under `/Library/PostgreSQL` and occupy ports **5432 and 5433**. Our container uses **15432**. |
| psql | 14.14 (client only) |
| gh | 2.96.0, authenticated |
| kubectl, Helm, Terraform | Not installed. They will be added in Phases 10, 11 and 17. |

### Fixing the Compose plugin (if it breaks again)

```bash
brew install docker-compose
rm ~/.docker/cli-plugins/docker-compose          # dangling Docker Desktop link
ln -s "$(brew --prefix)/opt/docker-compose/bin/docker-compose" ~/.docker/cli-plugins/docker-compose
docker compose version
```

## Graceful shutdown

When the API receives SIGTERM, it stops accepting new connections and waits up to `SHUTDOWN_TIMEOUT` for in-flight requests to finish before it exits. Kubernetes sends SIGTERM during rolling updates, so this behaviour prevents dropped requests during a deploy.

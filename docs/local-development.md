# Local Development

## Resource budget

The reference machine is an M1 MacBook Air with 8 GB RAM, running Colima with 4 CPUs and 4 GB RAM. The rules:

- Run the Go API natively (`make dev`) while iterating. Use containers only for its dependencies.
- Docker Compose services are grouped into profiles:
  - `deps`: PostgreSQL and Redis, for running the API natively.
  - `core`: `deps` plus the migrate job and the API in a container.
  - `observability`: Prometheus and Grafana, opt-in.
- The `deps` profile uses about 85 MB and the full `core` stack about 105 MB.
- Stop unrelated containers before starting Kubernetes (`make k8s-up`).

## Toolchain status (audited 2026-10-09)

| Tool | Status |
|---|---|
| Go | 1.27.2 (from `go.mod`; the toolchain downloads it automatically) |
| Docker / Colima | Colima 0.10.3, docker runtime, aarch64, 30 GB disk |
| Docker Compose plugin | 5.6.0 (Homebrew). The symlink was repaired on 2026-10-09; it had pointed to a removed Docker Desktop install. |
| Native PostgreSQL | Versions 16 and 17 are installed under `/Library/PostgreSQL` and occupy ports **5432 and 5433**. Our container uses **15432**. |
| Native Redis | A Homebrew `redis` service occupies **6379**. Our container uses **16379**. If you don't need the native one, `brew services stop redis` saves a little RAM. |
| psql | 14.14 (client only) |
| gh | 2.96.0, authenticated |
| kubectl | 1.37 (Homebrew) |
| Kubernetes | Colima k3s 1.35, enabled with `make k8s-up`. It uses about 0.8 GB of the VM's 4 GB. |
| Helm | 4.3 (Homebrew) |
| Terraform | 1.16.5 |
| k6 | 2.3 (load tests, `make perf`) |
| golangci-lint, actionlint, shellcheck | Same versions as CI (`make lint`) |

### Fixing the Compose plugin (if it breaks again)

```bash
brew install docker-compose
rm ~/.docker/cli-plugins/docker-compose          # dangling Docker Desktop link
ln -s "$(brew --prefix)/opt/docker-compose/bin/docker-compose" ~/.docker/cli-plugins/docker-compose
docker compose version
```

## Graceful shutdown

When the API receives SIGTERM, it stops accepting new connections and waits up to `SHUTDOWN_TIMEOUT` for in-flight requests to finish before it exits. Kubernetes sends SIGTERM during rolling updates, so this behaviour prevents dropped requests during a deploy.

## Colima disk space

Docker's images and containers live on Colima's **data disk** (`/mnt/lima-colima`), not on the VM's root disk. On the reference machine that disk was 5.9 GB and filled up in Phase 8, which broke image builds (`no space left on device`).

It was fixed on 2026-10-13:
- Two unrelated images were removed: `azure-sql-edge` (its data volumes, `mssql-data` among them, were kept) and `dotnet/sdk:8.0`.
- Colima was restarted with `--disk 30`, and the data disk grew to 30 GB.

To check usage:

```bash
docker system df                                   # what Docker is using
colima ssh -- df -h /mnt/lima-colima               # the actual limit
```

To free space:
- `docker image prune` removes dangling images.
- `docker builder prune` removes the build cache.
- Large images that aren't used here, such as `azure-sql-edge` and `dotnet/sdk`, can be removed if you don't need them elsewhere.
- Or stop Colima and restart it with a larger disk: `colima stop && colima start --disk 30`.

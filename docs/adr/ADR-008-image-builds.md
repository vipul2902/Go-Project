# ADR-008: Image builds with the git and docker CLIs locally, isolated builders in production

- **Status:** Accepted
- **Date:** 2026-10-13

## Context
The worker must turn a repository and commit into a container image. Building means running the repository's own Dockerfile, which is **untrusted code**: every `RUN` instruction executes with the builder's privileges.

## Decision
**Local development:** the worker runs the `git` and `docker` command-line tools as subprocesses against the developer's own Docker daemon (Colima). Images stay in that daemon, with no registry.

**Production (Phase 18):** builds run on **ACR Tasks** (`WORKER_BUILDER=acr`). Azure clones the repository at the commit and builds on ephemeral, Azure-managed agents outside the cluster, then pushes to ACR, and the cluster pulls with its kubelet identity. The worker never gets a Docker socket. It authenticates with workload identity and holds a custom role limited to scheduling, reading, log access and cancelling runs. BuildKit rootless or kaniko in a dedicated node pool remain the alternative where builds must stay inside the cluster.

## Why the command-line tools rather than Go SDKs (go-git, the Docker Engine API)
- They're the exact commands developers and CI run, so failures can be reproduced by hand.
- Cancellation is simply interrupting a process. BuildKit cancels the build when its client disconnects; this is verified by a test that checks the daemon for orphaned processes.
- No large SDK dependencies, which avoids churn in the Docker module ecosystem.
- Trade-off: the worker needs `git` and `docker` installed. The distroless worker image doesn't include them, so containerised workers use `WORKER_BUILDER=dryrun` locally.

## Why not mount the Docker socket into the worker container
Access to `/var/run/docker.sock` is equivalent to root on the host: a client can start a privileged container that mounts the host filesystem. A worker that builds untrusted code must never hold that capability. This is the main reason production uses isolated builders.

## Mitigations in place today
- `https` repositories only, with no embedded credentials. This is checked by the API and again by the builder.
- Git never runs hooks, ignores global and system config, blocks the `file://` transport, and never prompts.
- The source tree is limited to 500 MB, with a 15-minute build timeout and per-deployment cancellation.
- No secrets or build arguments are passed to builds. Secret references are resolved only at deploy time, inside the cluster.
- Every build gets a fresh temporary directory, which is always removed.

## Consequences
- Local images are built for the host architecture (arm64 on Apple Silicon). Multi-arch builds (`buildx --platform`) come with CI/CD.
- Private repositories aren't supported until GitHub App integration, which will supply short-lived tokens without storing credentials.

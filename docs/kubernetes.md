# Kubernetes

## Local cluster

The local cluster is Colima's built-in **k3s**. It runs inside the existing VM and uses the same Docker daemon, so images built by the worker are available to the cluster immediately, with no registry and no `kind load`.

```bash
make k8s-up      # colima start --kubernetes, then wait for the node to be Ready
make k8s-down    # turn Kubernetes off to free about 1 GB of RAM
make test-k8s    # deployer tests against the cluster (context: colima)
make worker      # worker with Docker builds and Kubernetes deploys
```

The worker requires an explicit `KUBE_CONTEXT` when it runs outside a cluster. A worker must never deploy to whatever cluster your shell happened to point at last. It also connects at startup, so a wrong context fails immediately rather than on the first deployment.

## What a deployment creates

For project `P`, environment `E` and namespace `N` (taken from the environment's snapshot):

| Object | Key settings | Why |
|---|---|---|
| `Namespace N` | Labels `managed-by=clouddeploy` and `project-id`; Pod Security `enforce=baseline`, `warn/audit=restricted` | One tenant boundary per environment. Baseline blocks privileged pods and host access. |
| `Deployment P` | `maxUnavailable: 0`, `maxSurge: 1`, `progressDeadlineSeconds: 300`, `revisionHistoryLimit: 5` | Zero-downtime rollouts; Kubernetes marks a rollout stuck after 5 minutes without progress |
| Pod template | Label `clouddeploy.dev/deployment-id`, no service-account token, no service links, seccomp `RuntimeDefault` | A new label on every deployment, so even a redeploy of the same image rolls the pods. There's no API token to steal. |
| Container | Drops all capabilities, no privilege escalation, `imagePullPolicy: IfNotPresent`, requests and limits from the environment | Least privilege. Tags are immutable commit SHAs, so a cached image is always correct. |
| `Service P` | ClusterIP, port 80 → named port `http` | A stable in-cluster address |

Objects are created with **server-side apply** under field manager `clouddeploy`. Kubernetes records which fields CloudDeploy owns, so other controllers, such as an autoscaler later, can own other fields without the two silently overwriting each other.

### Environment variables
- Plain variables are passed as values.
- Secret variables become `secretKeyRef` entries pointing at `<secret>/<key>` in the environment's namespace. The **kubelet** reads the Secret, and the value never passes through CloudDeploy. This is verified by a test that reads the variable inside the pod.
- The platform also injects `PORT`, plus `CLOUDDEPLOY_DEPLOYMENT_ID`, `CLOUDDEPLOY_COMMIT_SHA` and `CLOUDDEPLOY_ENVIRONMENT`. A user-defined `PORT` takes precedence.

### Probes

| Probe | Check | Rationale |
|---|---|---|
| Readiness | `GET <health_check_path>` if one is set; otherwise TCP on the port | Gates traffic. The app decides when it's ready. |
| Liveness | TCP on the port only | It restarts only a wedged process. It never uses app health: if the database is down, restarting the app in a loop fixes nothing and makes recovery slower. |

## Rollout tracking

After applying, the deployer polls every 2 seconds until one of these happens:

1. **Success:** the controller has observed our generation, and every replica is updated, current and available.
2. **Fail fast:** a pod of *this release* (selected by its `deployment-id` label) is stuck in a state that won't fix itself:

| Reason | Typical cause | Detected in (tests) |
|---|---|---|
| `ImagePullBackOff`, `InvalidImageName` | Wrong image or tag | about 14 s |
| `CrashLoopBackOff` | The app exits on start. Its **last output and exit code are copied into the pipeline log.** | about 6 s |
| `CreateContainerConfigError` | A referenced Secret or key is missing | about 2 s |

3. **Progress deadline exceeded:** for example, pods that never become ready.
4. **Context cancelled:** the user cancelled, the lease was lost, the worker is shutting down, or the deploy timed out. This returns within one poll interval.

**What a failed rollout means for traffic:** with `maxUnavailable: 0`, the previous version keeps running at full capacity while the new pods fail. A test verifies this with both old pods staying ready. The Deployment object is left pointing at the failed spec, so the next successful deployment, or a rollback (Phase 13), restores it.

## Safety guards
- **Namespace ownership.** The deployer refuses to deploy into a namespace that already exists unless it carries `managed-by=clouddeploy` and the same `project-id`. It can never take over `kube-system` or another team's namespace. The database already prevents reserved and duplicate names; this is the cluster-side check.
- **Resource bounds** come from the environment's validated limits (Phase 5). Without them, pods would be unbounded.

## Monitoring deployed apps

The API reads live state through `kube.Inspector` (read-only):

| Feature | Source | Notes |
|---|---|---|
| Health | The Deployment plus its pods (selected by `app.kubernetes.io/instance`) | One status. `failing` means pods of the *current* release are in a fatal state, such as `CrashLoopBackOff`. It's reported immediately, rather than showing `progressing` until the 5-minute deadline. |
| Drift | The live pod template compared with the last successful deployment's snapshot | Catches failed rollouts and out-of-band `kubectl` changes |
| Logs | `pods/log` with timestamps | Merged across pods by timestamp, sanitized with `logsafe`, at most 10 pods, 1,000 lines and 256 KB per pod |
| Follow | Streaming `pods/log`, one goroutine per pod, merged into a single SSE writer | Covers the pods present when the stream started |

**The namespace always comes from the database** (the environment the caller is authorized for), never from the request. That, not Kubernetes RBAC, is what isolates tenants from each other's pods.

## Running CloudDeploy itself on Kubernetes (Helm)

The chart is in [deploy/helm/clouddeploy](../deploy/helm/clouddeploy). It installs the API, the worker and the migration Job.

```bash
make helm-lint     # lint with every values file and check the safety guards
make k8s-deploy    # build content-tagged images, start dev deps, create the Secret, run helm upgrade --install (dev values)
make k8s-forward   # API on localhost:8080
make k8s-uninstall
```

| Values file | Use |
|---|---|
| `values.yaml` | Defaults, safe for production on their own (`appEnv=production`, 2 API replicas, a PDB) |
| `values-dev.yaml` | Local k3s: 1 replica each, debug logging, seed data, the Docker-socket worker |
| `values-staging.yaml` | Production topology at a smaller scale: ingress, NetworkPolicy |
| `values-prod.yaml` | 3 to 10 API replicas through an HPA, PDB `minAvailable: 2`, ingress with TLS, NetworkPolicy |

### Design

| Decision | Why |
|---|---|
| **Migrations run as a `pre-install`/`pre-upgrade` hook Job** | The schema is migrated before any new API pod starts. A failed migration fails the release and leaves the old version running. Replicas never race to migrate on boot. |
| **`existingSecret`; the chart contains no secrets** | Secrets come from the platform's secret store (Azure Key Vault through the CSI driver in production). Locally, `make k8s-secrets` pipes values from `.env` without putting them on the command line. |
| **ConfigMap checksum annotation** | A config change rolls the pods, which otherwise wouldn't notice it. |
| **Least-privilege ClusterRoles** | **API** (since Phase 12; before that it had no token): read-only `get` on deployments, `get/list` on pods, `get` on pod logs, for monitoring. **Worker:** only the verbs the deployer uses, `get/create/patch` on namespaces, deployments and services, `get/list` on pods, `get` on pod logs. **Neither can read Secrets** (verified with `kubectl auth can-i`). |
| **Pods meet Pod Security "restricted"** | Non-root UID 65532, read-only root filesystem, all capabilities dropped, seccomp `RuntimeDefault`. The namespace *warns* on restricted, so the dev-only exception is flagged on every install. |
| **`preStop: sleep 5`** + `maxUnavailable: 0` | Endpoint removal propagates asynchronously, so the pod keeps serving briefly after it's scheduled to stop. Measured: **225 of 225 in-cluster requests succeeded** while the only API pod was replaced. |
| **Worker `terminationGracePeriodSeconds` = SHUTDOWN_TIMEOUT + 10 s** | In-flight deployments drain before Kubernetes kills the pod (Phase 8). |
| **Guards in templates (`fail`)** | Rendering fails if the Docker socket is enabled with `appEnv=production`, if `builder=docker` is set without the socket, if there's no `existingSecret`, or if the HPA minimum is below 2. |

### Image tags
`make k8s-images` tags each dev build `dev-<image digest>`. A mutable tag like `:dev` would leave the Deployment spec unchanged after a rebuild, so Kubernetes would never roll the pods. This happened during Phase 12: `helm upgrade` reported success but kept serving the old code. CD will use immutable git SHAs.

### Development-only pieces
- **Dependencies:** PostgreSQL (a StatefulSet with a 1 GiB PVC) and Redis run in the `clouddeploy-deps` namespace from [deploy/kubernetes/dev-deps](../deploy/kubernetes/dev-deps). They're deliberately **outside the chart**: in production these are managed Azure services, and the chart only receives their URLs.
- **In-cluster builds:** the dev worker runs the `worker-builder` image (Alpine with git, docker and buildx) and mounts the node's Docker socket. That's root on the node, which is why the dev namespace is labelled `enforce=privileged` and why the chart refuses this setting in production (ADR-008). Production builds need an isolated remote builder (Phase 18).
- When the cluster stack is running, stop the Compose `deps` profile (`docker compose --profile deps stop`) to save RAM.

### Upgrades and rollbacks
`helm upgrade` rolls pods with zero downtime. `helm rollback <rev>` restores the previous manifests and config; verified by rolling a config change back. Remember that **database migrations aren't rolled back by `helm rollback`**. Migrations must therefore stay backward compatible (expand/contract, see [database.md](database.md)), so that the previous release still works against the newer schema.

## Not yet included
For **CloudDeploy itself**, the chart provides Ingress, HPA, PDB and NetworkPolicy. For **tenant apps**, they're planned for later phases. Apps are reached in-cluster through their Service; locally, use `kubectl -n <ns> port-forward svc/<project> 8888:80`.

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

## Not yet included
Ingress, HorizontalPodAutoscaler, NetworkPolicy and PodDisruptionBudget are planned with Helm and later phases. Apps are reached in-cluster through their Service; locally, use `kubectl -n <ns> port-forward svc/<project> 8888:80`.

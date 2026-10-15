# Demo walkthrough

From a clean checkout to a deployed app, a failed rollout that rolls itself back, and drift detection, in about ten minutes on a laptop.

You need Docker (Colima works), `kubectl`, `helm`, Go and Make, plus a public git repository with a `Dockerfile` at its root that serves HTTP on port 8080. Any small web app will do.

## 1. Start the platform

```bash
cp .env.example .env                 # replace every change-me value
make k8s-up                          # local Kubernetes (Colima k3s)
make k8s-deploy                      # PostgreSQL, Redis and CloudDeploy in the cluster (Helm)
make k8s-forward                     # dashboard on http://localhost:8080
```

`k8s-deploy` builds the images, installs the chart with the development values, runs migrations as a pre-install hook, and seeds three users. Sign in as `admin@clouddeploy.local` with the `SEED_PASSWORD` from `.env`.

## 2. Deploy an application

On the dashboard:

1. **New project.** Give it a name (a DNS label, for example `hello`) and your repository URL.
2. **Add environment** `staging`. In its configuration, set the health check path if your app has one (for example `/healthz`), tick **auto-rollback**, and click **Save configuration**.
3. **Deploy** a commit SHA from that repository.

The deployment page shows the state machine advancing (`QUEUED → BUILDING → DEPLOYING → RUNNING`) and the pipeline log streaming live over server-sent events:
- the clone
- the Docker build
- the server-side apply
- the rollout as pods become ready

The project page then shows live health read from the cluster: desired and ready replicas, pod states, and the image actually running.

## 3. Break it, and watch it recover

Make the next release fail in a realistic way. In the `staging` environment, add a variable, tick **secret reference**, and point it at a Secret that doesn't exist (for example key `DB_PASSWORD`, reference `missing/password`). Then deploy again.

- The new pods hit `CreateContainerConfigError`.
- The deployer recognizes a failure that won't fix itself and fails the rollout **fast**, with the reason in the log, instead of waiting for a timeout.
- The previous version keeps serving the whole time (`maxUnavailable: 0`).
- Auto-rollback creates a new deployment restoring the last good snapshot. Health returns to `healthy` with no drift.

Every step is in the **Audit log** (admins only).

## 4. Detect drift

Change the cluster behind the platform's back:

```bash
kubectl --context colima -n hello-staging scale deployment/hello --replicas=3
```

The project's health now reports **drift**: replicas the cluster wants versus what was deployed. Redeploying or rolling back puts it back.

## 5. Look inside (optional)

```bash
make obs-up      # Prometheus :9091, Grafana :3000, Jaeger :16686
```

Run the API and worker with `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` and deploy again. Jaeger then shows **one trace** for the deployment, crossing from the API request through the PostgreSQL queue into the worker's build and deploy steps. Grafana's dashboard shows request rates and latency, queue depth, deployment outcomes and rate-limit decisions.

## 6. Prove it at scale (optional)

```bash
make db-up && make migrate
DEPLOYMENTS=300000 make perf
```

This seeds an organization with 300,000 deployments and runs the dashboard's request mix at 300 req/s with k6. It reports latency percentiles, a CPU profile and the slowest queries. The [performance write-up](performance.md) explains what this found and how it was fixed.

## Clean up

```bash
make k8s-uninstall   # remove CloudDeploy (keeps the dev database)
make k8s-down        # stop Kubernetes, freeing about 1 GB of RAM
make docker-down     # stop Compose services (data volumes kept)
```

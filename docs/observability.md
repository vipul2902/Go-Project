# Observability

CloudDeploy answers three questions with three signals:

| Question | Signal | Where |
|---|---|---|
| *Is it healthy, and how is it trending?* | **Metrics** (Prometheus) | API `:9090/metrics`, worker `:8081/metrics` (internal ports) |
| *Why was this request or deployment slow, or failing?* | **Traces** (OpenTelemetry) | OTLP to Jaeger, or any OTLP backend |
| *What exactly happened?* | **Structured logs** (JSON, `slog`) | stdout. Lines inside a traced operation carry `trace_id` and `span_id`. |

```bash
docker compose --profile observability up -d   # Prometheus :9091, Grafana :3000, Jaeger :16686 (about 300 MB, opt-in)
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 make dev      # API with tracing
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 make worker   # worker with tracing
docker compose --profile observability stop
```

Grafana logs in as `admin` with `GRAFANA_ADMIN_PASSWORD` from `.env`. The **CloudDeploy overview** dashboard and both data sources are provisioned automatically.

| Grafana | One trace: API request → queue → worker |
|---|---|
| ![](screenshots/grafana-overview.png) | ![](screenshots/jaeger-deployment-trace.png) |

## Metrics

| Metric | Type | Labels | Source |
|---|---|---|---|
| `http_requests_total` | counter | `method`, `route`, `code` | API |
| `http_request_duration_seconds` | histogram | `method`, `route` | API (excludes SSE streams) |
| `http_requests_in_flight` | gauge | | API |
| `ratelimit_decisions_total` | counter | `rule`, `decision` (`allowed`, `denied`, `fail_open`) | API |
| `deployment_total` | counter | `environment`, `outcome` (`succeeded`, `failed`, `cancelled`) | worker |
| `deployment_duration_seconds` | histogram | `environment`, `outcome` | worker |
| `deployment_failures_total` | counter | `environment`, `stage` (`build`, `deploy`, `panic`, `lease_lost`, `interrupted`) | worker |
| `deployment_step_duration_seconds` | histogram | `step`, `result` | worker |
| `deployment_auto_rollbacks_total` | counter | `environment`, `result` | worker |
| `worker_deployments_in_flight` | gauge | | worker |
| `deployment_queue_depth`, `deployments_active`, `deployment_queue_oldest_age_seconds` | gauge | | worker, computed **at scrape time** with one query |
| Go runtime and process metrics | | | both |

Design rules:
- **Bounded label values.** `route` is the matched *pattern* (`GET /api/v1/deployments/{id}`), never the raw path. One series per deployment ID would grow without limit; a test checks that two different IDs produce one series, and fails if the raw path is used.
- **Initialise known series at 0.** A counter that first appears at 1 has no earlier sample, so `increase()` misses the first event. A fresh dashboard showed 0% deployment success after a successful deploy, which is how this was found. All environment × outcome and stage combinations are created at startup.
- **Internal ports only.** Metrics expose routes, error rates and queue depth, so they're never served through the public Service or ingress (`/metrics` on the API port returns 404). In Kubernetes the pods carry `prometheus.io/*` annotations, and with NetworkPolicy on, only the monitoring namespace can reach the metrics ports.
- **A custom registry, not the global one,** so there's no shared mutable state and tests inspect metrics in isolation.
- **Queue gauges are computed when Prometheus scrapes,** not by a background loop polling the database.

## Tracing

Tracing is enabled by the standard `OTEL_EXPORTER_OTLP_ENDPOINT` variable. Without it, the no-op provider stays in place and tracing costs nothing. Sampling uses the standard `OTEL_TRACES_SAMPLER` settings; the default keeps everything, which suits development.

| Span | Created by |
|---|---|
| `GET /api/v1/...` (named by route pattern) | `otelhttp`, renamed with `r.Pattern` by the innermost middleware. Health probes are filtered out. |
| `db select`, `db insert`, … | A pgx `QueryTracer`. It records SQL with placeholders, **never argument values**, which may contain personal data or secrets. |
| `deployment <environment>`, `build`, `deploy` | The worker |

**Across the queue:** `POST /deployments` stores the W3C `traceparent` header on the deployment row (`trace_context`, migration 00009). The worker extracts it and continues the trace. One trace therefore covers the HTTP request, its database writes, the queue wait (`deployment.queue_wait_seconds`), the build and the rollout, across two services. Tested in `TestTraceSpansAPIQueueAndWorker`, and verified in Jaeger: 14 spans, 2 services.

**Logs ↔ traces:** `tracing.LogHandler` adds `trace_id` and `span_id` to every log record written with a context inside a span. Every API request log line carries the trace ID, and each worker job's logger includes it, so a `trace_id` copied from a log opens the matching trace in Jaeger.

## Alerts ([deploy/observability/alerts.yml](../deploy/observability/alerts.yml), checked with `promtool`)

| Alert | Fires when | Why it matters |
|---|---|---|
| `APIHighErrorRate` | More than 5% of requests are 5xx for 10 minutes | Users are seeing failures |
| `APILatencyHigh` | p95 above 500 ms on a route for 10 minutes | A degraded experience |
| `RateLimiterFailingOpen` | Any `fail_open` decision for 5 minutes | Brute-force protection is off (ADR-003) |
| `DeploymentFailureRateHigh` | More than 25% of deployments failed in an hour (with at least 4) | A bad platform change or a broken builder |
| `DeploymentQueueBacklog` | The oldest queued deployment is older than 5 minutes | Workers are down or saturated |
| `DeploymentLeaseLost` | Any `lease_lost` failure | A worker died mid-deployment |
| `APIDown`, `WorkerDown` | A scrape target is down for 5 minutes | |

## Bugs found while building this
- **Tracing crashed at startup when enabled** (`conflicting Schema URL`). The semantic-conventions version didn't match the SDK's default resource. Every test ran with tracing off, so only a real run exposed it. A test now runs `Setup` with an exporter configured.
- **The dashboard showed 0% success after a successful deployment,** because counters weren't initialised (see above).
- **The 5xx ratio showed "No data" on a healthy system.** With zero errors, `sum(rate(...))` returns nothing, so the query now uses `or vector(0)`.
- **Worker log lines lacked trace IDs.** The job's logger now carries the trace ID.

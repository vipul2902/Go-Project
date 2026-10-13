# Testing Strategy

Each kind of test is chosen for the kind of bug it can find. Tests use real infrastructure: real PostgreSQL, Redis, git, Docker and Kubernetes. Test doubles are used only for **external side effects** we need to control: the worker's build and deploy executor, and `fetch` in browser unit tests.

| Layer | Command | What it proves |
|---|---|---|
| Unit | `make test` | Pure logic with `-race`: validation, state machine, tokens, manifests, parsers |
| Integration | `make test-integration` | HTTP API, PostgreSQL constraints and concurrency, Redis rate limits, the worker queue, git and Docker builds |
| Cluster | `make test-k8s` | Rollouts, fail-fast diagnostics, secret resolution, health, logs, SSE, against Colima k3s plus PostgreSQL |
| Frontend | `make web-check` | Type checking, lint, 19 Vitest/RTL tests with coverage, production build |
| Browser | `node web/scripts/screenshots.mjs` | Drives real Chrome through the real UI. Fails on any exception or unexpected HTTP error. |
| Fuzzing | `make fuzz` | 13 targets explore inputs nobody thought to write down |
| Stress | `make test-stress` | The integration suite 3× under `-race`, to surface flaky and timing-dependent tests |
| Benchmarks | `make bench` | Costs of the hot paths (below) |
| Coverage | `make cover`, `cd web && npm run coverage` | Shows where untested code is. The goal isn't 100%. |

## Techniques beyond example-based tests

### Differential fuzzing against Kubernetes
Our validators are fuzzed against Kubernetes' own parsers (`resource.ParseQuantity`, `IsDNS1123Subdomain`, `IsConfigMapKey`, `IsEnvVarName`, `IsDNS1123Label`). Anything we accept, Kubernetes must accept with the same value; otherwise users get an obscure failure at deploy time instead of a clear validation error. **This found two real bugs:**
1. Secret references like `a..b/key` and the keys `.` and `..` were accepted. The fix is to call Kubernetes' validators directly, and migration 00008 tightens the database CHECK to match.
2. Memory `100000K` was accepted, but Kubernetes' decimal kilo suffix is lowercase `k`. The failing input is kept in `internal/environments/testdata/fuzz/` as a permanent regression case.

### Invariant fuzzing
- `VerifyPassword` must never panic, hang or exhaust memory, whatever stored hash it's given. The argon2 cost parameters read from a stored hash are now bounded: before this, a tampered `m=4294967295` would have tried to allocate 4 TB per login.
- `X-Forwarded-For`: an untrusted peer can never choose its own IP.
- `logsafe.Line`: the output never contains control characters.
- Log truncation never splits a UTF-8 character.
- Cursors and ETags supplied by attackers never panic.

### Model-based testing
`TestStateMachineModelAgainstDatabase` applies 200 random (seeded, so reproducible) transitions to real deployments in PostgreSQL. After every step it checks the database against the Go model, plus invariants such as "terminal ⇔ `completed_at` is set" and "never completed before started".

### The "no accidentally public endpoint" sweep
`TestNoRouteIsAccidentallyPublic` takes **every** route the production wiring registers (recorded through `httpx.Router`). It requires each one to reject a missing token, an invalid token and Basic auth with `401 UNAUTHORIZED`. Only an explicit, reviewed allowlist (health checks and login/registration) may answer without a token. Registering a new route without `RequireAuth` fails the build.

### Mutation checks
Important tests were checked by deliberately breaking the code they protect (for example: removing `FOR UPDATE`, `SKIP LOCKED`, single-flight refresh, idempotency-key reuse, `RequireAuth`, or microsecond precision) and confirming the test fails. Twice this showed a test that **couldn't** catch its bug (the `SKIP LOCKED` test and the first sub-millisecond rate-limit test), and both were rewritten until they did.

## Bugs found in this phase
| Found by | Bug | Fix |
|---|---|---|
| Differential fuzzing | Invalid secret references accepted | Use Kubernetes validators; tighten the CHECK (migration 00008) |
| Differential fuzzing | Memory suffix `K` accepted | Correct the unit table (`k`) |
| Fuzzing (by design) | Unbounded argon2 parameters from stored hashes | Bound memory, time, threads, salt and key length |
| Benchmark edge case | Rate limits faster than 1 per ms computed an interval of 0, so the limiter **failed open** | GCRA in microseconds; `Rule.Validate` rejects unrepresentable rules |
| Stress run under `-race` | Rate-limit test compared Redis's clock with the host's | Measure on the limiter's clock (Redis `TIME`) |
| Unit test of `StartSSE` | Streams failed entirely where deadlines aren't supported | Tolerate `http.ErrNotSupported` |

## Benchmarks (M1 MacBook Air)
| Path | Cost | Note |
|---|---|---|
| `HashPassword` (argon2id) | ~25 ms, 19 MB per call | Deliberately expensive. It's why login is rate-limited: each guess costs the server this much. |
| `VerifyAccessToken` (JWT) | ~3.2 µs | Paid on every authenticated request |
| Client IP behind a proxy | ~158 ns | Every request |
| `logsafe.Line` | ~215 MB/s | Every line of build and container output |
| Render a Deployment manifest | ~3.2 µs | Per deployment |
| Rate limit check (GCRA via Redis) | ~430 µs | Dominated by the Mac-to-VM network hop. In-cluster it's sub-millisecond. |

## Coverage
| Area | Coverage | Notes |
|---|---|---|
| Go (unit + integration, before this phase's additions) | 72.9% of statements | `cmd/*` (wiring in `main()`) is 0% by design: it's verified by running the binaries locally, in Compose and in the cluster. |
| `internal/kube` (with cluster tests) | 91% | |
| Frontend | 55% of statements | API client, SSE parser, log viewer and auth are well covered. The remaining gaps are data-rendering pages, which the browser run exercises. |

## Scope
- Auto-rollback crash recovery behaviour is described in [deployment.md](deployment.md).
- Load testing is covered in the performance phase (Phase 22).

# Continuous integration

GitHub Actions runs every check on every pull request. On pushes to `main` and version tags, the CD pipeline ([cd.md](cd.md)) calls the same workflow first, so nothing is published without passing it. CI itself doesn't deploy anything and needs no cloud credentials.

## Workflows

**[`ci.yml`](../.github/workflows/ci.yml)** runs on pull requests and manual dispatch, and as the first stage of CD. Its jobs run in parallel:

| Job | What it runs | Local equivalent |
|---|---|---|
| Go lint | golangci-lint v2 ([`.golangci.yml`](../.golangci.yml)): standard linters plus bodyclose, errorlint, gosec, nilerr, rowserrcheck, sqlclosecheck, unconvert, misspell | `make lint` |
| Go unit tests | `go vet`, `go test -race` with coverage (artifact) | `make test` |
| Go integration tests | PostgreSQL 17 and Redis 7.4 service containers, Docker builder tests on the runner's Docker | `make test-integration` |
| Kubernetes tests | k3s in `--docker` mode on the runner, so test images are runnable without a registry (the same model as Colima locally) | `make test-k8s` |
| Dashboard | `npm ci`, typecheck, ESLint, Vitest with coverage, production build | `make web-check` |
| Helm chart | Lint with every values file, including Azure mode, plus the safety guards | `make helm-lint` |
| Terraform (offline) | `fmt`, `validate`, `terraform test` with a mocked azurerm provider | `make terraform-check` |
| Images | Build the app and web images with BuildKit (GitHub Actions cache), then scan with Trivy. The job fails on HIGH or CRITICAL vulnerabilities that have a fix available. | `docker build` + `trivy image` |
| Security scans | govulncheck (reachable Go vulnerabilities), gitleaks (full git history), `npm audit` (production dependencies) | `make vulncheck`, `gitleaks git .` |

**[`nightly.yml`](../.github/workflows/nightly.yml)** runs at 03:00 IST and on demand. It covers the slower checks:
- every fuzz target (60s each by default), with any failing input uploaded as an artifact
- the integration suite three times under the race detector, to surface flaky tests

**[`dependabot.yml`](../.github/dependabot.yml)** opens weekly update PRs for:
- Go modules, npm, GitHub Actions, Dockerfiles and Terraform providers
- minor and patch updates grouped per ecosystem

## Design choices

- **Pinned actions.** Every third-party action is pinned to a full commit SHA, with the version as a comment. A tag can be moved to point at different code, but a SHA can't. Dependabot updates the SHA and the comment together.
- **Least privilege.** The workflow token is `contents: read` only. No job can push, comment or publish.
- **No secrets.** The test databases use throwaway credentials that exist only for the length of the job. No repository secrets are defined, so pull requests from forks run the full suite safely.
- **Same tools and versions as local.** golangci-lint v2.14.0, Terraform 1.16.5, Helm v4.3.0, Node 24. Go comes from `go.mod`. Each job maps to a Make target, so a CI failure can be reproduced with one local command.
- **CI builds images but never pushes them.** Publishing is CD's job, after CI passes.
- **Concurrency.** A new push to a pull request cancels its superseded run. Runs on `main` always finish.

## Findings from setting up CI

Adding the scanners surfaced real issues, all fixed before the first CI run:

| Scanner | Finding | Fix |
|---|---|---|
| govulncheck | 11 standard-library vulnerabilities reachable from our code (for example `html/template` via `net/http`) in Go 1.27.1 | `go 1.27.2` in `go.mod`; builder image pinned to `golang:1.27.2-alpine` |
| govulncheck | `golang.org/x/net` v0.59.0 advisories | Upgraded to v0.61.0 (and x/crypto, x/sys, x/text, x/term, x/sync) |
| Trivy | 43 HIGH vulnerabilities (curl, OpenSSL, c-ares) in the web image's base, `nginx-unprivileged:1.29-alpine`, an nginx branch that no longer gets rebuilt | Moved to the stable branch, `1.30-alpine`: 0 findings |
| golangci-lint | 20 findings | Each fixed or justified, with nothing disabled globally (see below) |

The golangci-lint findings:
- **Unbounded `DB_MAX_CONNS`** could overflow pgx's `int32` pool size. It is now validated to at most 1000, with a test.
- **Wrapped-error comparisons** now use `errors.Is`.
- **Ignored errors** from `Rollback` and `Close` are made explicit.
- **A test helper** now returns its error last.
- **`int32` conversions of values that are already range-validated** (replicas, port, worker concurrency, Argon2 key length) carry a `//nolint:gosec` comment naming the validation.

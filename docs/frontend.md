# Dashboard (web/)

React 19 + TypeScript + Vite. Server state is managed by TanStack Query, routing by React Router. The styling is hand-written CSS with design tokens and light and dark themes that follow the OS.

```bash
make web-dev     # http://localhost:5173 (proxies /api to the Go API on :8080)
make web-check   # type-check, lint, unit tests, production build
make k8s-deploy && make k8s-forward   # the in-cluster dashboard on http://localhost:8080
```

## Pages

| Route | What it shows |
|---|---|
| `/login` | Sign in |
| `/dashboard` | Project count, active, successful and failed deployments (30 days), success rate, recent activity |
| `/projects` | Project list and creation |
| `/projects/:id` | Live health per environment (pods, drift), a Deploy form, recent deployments |
| `/projects/:id/deployments` | Full deployment history |
| `/projects/:id/environments` | Configuration (replicas, resources, port, health path, auto-rollback) and variables |
| `/projects/:id/logs` | Live application logs (developer role) |
| `/deployments/:id` | Status timeline, details, configuration snapshot, live pipeline log, cancel, roll back |
| `/infrastructure` | Every environment across projects, with health |
| `/audit` | The organization's audit trail (admins) |

The UI hides actions the user's role doesn't allow. **The API enforces every rule anyway**; the UI checks are only for convenience.

## Design decisions

| Decision | Why |
|---|---|
| **Access token in memory only. Refresh token in an `HttpOnly; Secure; SameSite=Strict` cookie** (`X-Auth-Mode: cookie`) | Script injected through XSS can't read either token from storage. A page reload restores the session silently through the cookie. |
| **Single-flight refresh** | When several requests get 401 at once, they share *one* refresh. Two refreshes with the same cookie would look like token theft to the reuse detection (Phase 3) and revoke the session. Tested with 5 concurrent 401s producing exactly 1 refresh. |
| **SSE over `fetch`, with our own parser** | `EventSource` can't send an `Authorization` header. The parser is tested against every possible split point in the byte stream. The viewer reconnects with `Last-Event-ID` and stops at the `end` event. |
| **One `Idempotency-Key` per deploy intent** | Retrying the same deploy resends the same key, and a new commit or environment gets a new key. A double click or a retry can't start two deployments. Tested. |
| **Confirmation dialogs** for rollback, cancel and variable deletion | Uses the native `<dialog>` with `showModal()` for a focus trap, inert background and Escape to close |
| **TanStack Query** | Caching, background refetch, faster polling while a deployment is active, and invalidation after mutations, without hand-rolled `useEffect` fetching |
| **Same origin** (Vite proxy in dev, nginx `/api` proxy in the cluster) | No CORS at all, and the `SameSite=Strict` cookie just works |

## Serving (web/Dockerfile, web/nginx)
- The image is unprivileged nginx (uid 101, port 8080), at 84 MB. The pod runs with a read-only root filesystem.
- `/api/*` is proxied to the API service with `proxy_buffering off`, so SSE events arrive immediately. Verified: events streamed over 0–9 seconds rather than in one batch at the end.
- Security headers on every response: a strict CSP (`default-src 'self'`, no inline scripts), `X-Frame-Options: DENY`, `nosniff`, `no-referrer` and a Permissions-Policy.
  - nginx pitfall: an `add_header` in a `location` block discards the server-level ones. The headers therefore live in a snippet included in every location, and they're verified on both HTML and asset responses.
- Fingerprinted assets are cached for a year (`immutable`); `index.html` is served with `no-cache`, so a new release is picked up on the next load.
- The API trusts `X-Forwarded-For` only from the pod network (`TRUSTED_PROXY_CIDRS`), so rate limits and audit records see the real client IP.

## Testing
- **Unit and component tests (Vitest and React Testing Library, 18 tests):**
  - the SSE parser, single-flight refresh, error envelopes and formatting
  - the timeline, and dialog modality and busy state
  - the login flow
  - deploy idempotency (key reuse on retry, a new key for a new intent)
- **Browser run:** `web/scripts/screenshots.mjs` drives the locally installed Chrome (playwright-core) through the real UI, including the login form. It fails on any uncaught exception or unexpected HTTP error. It caught two bugs:
  - a request to `/projects/` with an empty ID
  - a dialog rendered non-modally

  It also produces the screenshots in [screenshots/](screenshots/).

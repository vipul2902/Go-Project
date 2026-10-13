# Security

This document is updated as each phase lands. A full review will be in `security-review.md` (Phase 21).

## Authentication

| Mechanism | Implementation |
|---|---|
| Password storage | argon2id with m=19 MiB, t=2, p=1, following the OWASP recommendations. Each password gets its own 16-byte random salt, and the hash is stored in PHC format so the parameters can be raised later. |
| Password policy | 12 to 128 characters. The upper bound prevents argon2 CPU-exhaustion attacks. |
| Access token | A JWT signed with HS256, valid for 15 minutes. It carries `sub` (the user ID), `iss`, `iat`, `nbf` and `exp`. **Roles are not included.** |
| Token verification | The algorithm is pinned to HS256 (this blocks `alg: none` and key-confusion attacks). Issuer and expiry are required, with 30 seconds of allowed clock skew. |
| Refresh token | 256 random bits, opaque, valid for 30 days. Only the SHA-256 hash is stored. |
| `JWT_SECRET` | Must be at least 32 bytes. It comes from the environment, and the app refuses to start without it. |

### Refresh-token rotation and reuse detection

```
login ──► R0 (family F)
refresh(R0) ──► R1   [R0 revoked, R0.replaced_by = R1]
refresh(R0) again ──► 401 + revoke ALL of family F   ← stolen R0 detected
refresh(R1) ──► 401  (family revoked; user must log in again)
```

Each refresh token can be used once. If a token that has already been rotated is presented again, two parties hold the same token, so one of them is an attacker. Revoking the whole family logs out both the attacker and the victim, and the victim's next login creates a fresh family. Other sessions, such as another device, belong to different families and are unaffected.

Rotation runs inside a single transaction using `SELECT … FOR UPDATE`. When two refreshes of the same token race, the second one waits and then sees the token as revoked. `TestConcurrentRefreshOnlyOneSucceeds` proves this. The trade-off is that a client which accidentally sends two refreshes at once gets logged out; the client must serialize its refreshes.

### Why roles are not in the JWT

Roles are per organization and can change at any time. A role embedded in the token would stay valid for the token's whole lifetime, up to 15 minutes after an admin removed it. Instead, `Service.Authorize` reads `organization_members` on each request. Phase 7 adds a short-lived Redis cache that is invalidated when memberships change.

### Logout

Logout revokes the refresh token's family. Access tokens are stateless and **remain valid until they expire**, at most 15 minutes. This is the standard trade-off of JWTs. If immediate revocation is ever required, the options are a Redis denylist of token IDs or shorter access-token lifetimes.

### Anti-enumeration

- Login returns the same 401 response for an unknown email and for a wrong password. An unknown email is still checked against a dummy argon2 hash, so both paths take the same time (measured at 27.5 ms vs 27.8 ms).
- `Authorize` returns *forbidden* for non-members rather than *not found*, so callers can't probe which organizations exist.
- Registration does reveal whether an email is already taken (409). This is unavoidable for a sign-up form and will be mitigated by rate limiting in Phase 7.

### Token transport

Tokens are returned in the JSON body and sent back as `Authorization: Bearer`. This works for the API, the CLI and the SPA. Responses carrying tokens set `Cache-Control: no-store`. Phase 14 will decide on browser storage: keeping the access token in memory and the refresh token in an `HttpOnly; Secure; SameSite=Strict` cookie is the preferred option.

## RBAC

Roles are hierarchical, `admin ⊃ developer ⊃ viewer`, and unknown roles are denied (fail closed).

| Action | viewer | developer | admin |
|---|:-:|:-:|:-:|
| Read projects, deployments and logs | ✓ | ✓ | ✓ |
| Create or update projects; deploy, roll back or cancel | | ✓ | ✓ |
| Manage members, delete projects, manage infrastructure | | | ✓ |

## Secrets and configuration

- **Secret values never enter CloudDeploy.** A secret variable stores only a reference, `<secret-name>/<key>`, to a Kubernetes Secret in the environment's own namespace. Because the format has no namespace component, a project can't reference another tenant's secrets. The database enforces the format with a CHECK constraint, and a second CHECK ensures a row holds either a value or a reference, never both.
- **Guardrail:** a plain value is refused if its key looks like a credential, such as `DB_PASSWORD`, `GITHUB_TOKEN` or `STRIPE_API_KEY`.
- **Change control:** changes to production configuration (the environment itself and its variables) require the admin role. Developers manage development and staging.
- **Cluster protection:**
  - Namespaces are unique across the platform (enforced by a unique index), so one project can't overwrite another's workloads.
  - The `default` and `clouddeploy` namespaces, and any namespace starting with `kube-`, are reserved.
  - CPU, memory and replica counts are capped so one tenant can't exhaust the cluster.
- Repository URLs with embedded credentials are rejected (Phase 4).

## Rate limiting

The limiter uses GCRA in Redis; the design is in [ADR-003](adr/ADR-003-redis.md). Limits apply per subject:

| Rule | Limit | Key |
|---|---|---|
| Login, per IP | 20 per minute | Client IP |
| Login, per account | 10 per 15 minutes; **reset by a successful login** | SHA-256 of the email (no raw personal data in Redis) |
| Registration | 5 per hour | Client IP |
| Token refresh | 60 per minute | Client IP |
| Authenticated API | 600 per minute | User ID (applied after authentication) |

How the limits behave:
- A denied request gets `429 RATE_LIMITED` with `Retry-After`. Every limited response carries `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`.
- The login limit is checked **before** the password, so a locked-out attacker can't use the endpoint to test whether a guessed password is correct.
- Known trade-off: the per-account limit lets an attacker lock a victim out for up to 15 minutes. That's accepted because it's bounded and visible, and far better than unlimited guessing.
- If Redis is down, rate limiting **fails open**, which is logged and reported as `degraded` by `/readyz`.

## Workloads in Kubernetes

Details are in [kubernetes.md](kubernetes.md):
- Each environment gets its own namespace, which enforces the Pod Security **baseline** profile and warns on **restricted**.
- Containers drop all capabilities, can't escalate privileges, run under seccomp `RuntimeDefault`, and **don't mount service-account tokens**.
- Secrets are resolved by the kubelet from `secretKeyRef`, so values never pass through CloudDeploy, its database or its logs.
- The deployer refuses namespaces it doesn't manage, and the worker requires an explicit `KUBE_CONTEXT`.
- CloudDeploy's own pods (from the Helm chart) meet the restricted profile. The API has no service-account token. The worker's ClusterRole can't read Secrets, exec into pods or change RBAC.
- Container output copied into the pipeline log (from crash loops) is sanitized like build output.

## Client IP and trusted proxies

The client IP drives per-IP rate limits and audit records. By default it's the **TCP peer**. When `TRUSTED_PROXY_CIDRS` lists the ingress controller's addresses and the peer is one of them, the API reads `X-Forwarded-For` **from the right**, taking the first address that isn't a trusted proxy. Everything further left was supplied by the client and can be forged; trusting the leftmost entry would let anyone choose their IP and bypass rate limits. If the chain is malformed, the API falls back to the peer. Unit tests cover spoofing, proxy chains and malformed headers.

## Request handling

- Request bodies are limited to 1 MiB. Unknown JSON fields are rejected, which prevents mass-assignment tricks such as `"is_admin": true`, and the body must contain exactly one JSON object.
- All SQL is parameterized (`$1` placeholders), and no SQL is built from user input.
- Internal errors are logged server-side with the request ID; clients receive only a generic 500.
- Passwords and tokens are never logged. This was verified by searching the server logs after live requests.

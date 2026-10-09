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

## Request handling

- Request bodies are limited to 1 MiB. Unknown JSON fields are rejected, which prevents mass-assignment tricks such as `"is_admin": true`, and the body must contain exactly one JSON object.
- All SQL is parameterized (`$1` placeholders), and no SQL is built from user input.
- Internal errors are logged server-side with the request ID; clients receive only a generic 500.
- Passwords and tokens are never logged. This was verified by searching the server logs after live requests.

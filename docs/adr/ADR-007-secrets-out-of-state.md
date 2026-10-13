# ADR-007: Keep secrets out of Terraform state

**Status:** Accepted (Phase 17)

## Context

Terraform state records every attribute of every managed resource. A `random_password` plus `administrator_password = random_password.x.result` puts the database password in plain text in state, and in every plan file. Anyone who can read the state bucket can read production credentials.

## Decision

- **Generate secrets as ephemeral resources.** `ephemeral "random_password"` produces values that exist only for the duration of a run.
- **Send them through write-only arguments.**
  - `administrator_password_wo` on PostgreSQL
  - `value_wo` on Key Vault secrets

  Azure receives the value; Terraform persists neither it nor a hash.
- **Version the write-only arguments.** A `*_wo_version` argument (from `secrets_version`) controls when a value is re-sent. Bumping it rotates every generated secret in one apply.
- **Use identities, not credentials.**
  - Workload identity (federated, scoped to exact service accounts) for Key Vault reads.
  - The kubelet identity for ACR pulls.
  - Entra ID only for AKS: local accounts are disabled.
  - OIDC federation for CI (Phase 19).

  No client secrets, registry passwords or static kubeconfigs are created.

## Consequences

- Terraform refuses, at validation, any attempt to assign an ephemeral value to a normal argument. The rule is enforced by the language, not by convention.
- Terraform can't detect drift in write-only values: a password changed in the portal won't show in a plan. Rotation is explicit, through `secrets_version`.
- The applying principal needs `Key Vault Secrets Officer` on the vault and network access to it. That's `operator_ip_ranges` for a laptop, or a runner inside the VNet later.
- The Redis access key is a computed attribute of `azurerm_managed_redis`, so state lives in an encrypted remote backend with Entra ID auth and restricted access. Entra ID token auth for Redis would remove the key entirely.

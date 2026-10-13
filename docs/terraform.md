# Terraform: Azure infrastructure

CloudDeploy's Azure footprint is defined in [`infra/terraform`](../infra/terraform). It is **planned and tested, never applied automatically**. Applying it creates billable resources, so it is a deliberate, manual step and has no Make target.

## What it creates (per environment)

```
rg-clouddeploy-<env>
├── vnet (one /18)
│   ├── snet-aks                 AKS nodes (pods use the CNI overlay, not VNet IPs)
│   ├── snet-postgres            delegated to PostgreSQL Flexible Server
│   └── snet-private-endpoints   Redis and Key Vault private endpoints
├── private DNS zones            postgres, redis, vault (linked to the VNet)
├── AKS                          Entra ID only, workload identity, Cilium network policy, Container Insights
├── ACR                          admin user disabled; AKS pulls via kubelet identity (AcrPull)
├── PostgreSQL Flexible Server   no public access, TLS enforced, write-only admin password
├── Azure Managed Redis          no public access, TLS only
├── Key Vault                    RBAC, deny-by-default network, database-url / redis-url / jwt-secret
└── (production) CanNotDelete lock on the resource group
```

## Layout

| Path | Purpose |
|---|---|
| `modules/{network,acr,aks,postgres,redis,keyvault}` | One concern each, with validated inputs |
| `modules/platform` | Wires the modules together and generates the secrets. It is the only place environments differ, through inputs. |
| `modules/platform/tests` | `terraform test` suite (13 runs) against a mocked provider |
| `envs/{dev,staging,prod}` | Thin roots: provider, backend and sizing. Each has its own state. |

## Environment differences

| | dev | staging | prod |
|---|---|---|---|
| AKS tier / nodes | Free, 1–2 × B2s_v2 | Free, 2–3 × D2s_v5 | Standard (SLA), 3–6 × D4s_v5 over 3 zones |
| PostgreSQL | Burstable B1ms, 7-day backups | GP D2ds_v5, 14 days | GP D2ds_v5, zone-redundant HA, geo-redundant backups, 35 days |
| Redis | Balanced_B0 | Balanced_B0 | Balanced_B1, HA |
| ACR | Basic | Standard | Premium (zone-redundant, untagged-manifest retention) |
| Key Vault purge protection | off | off | on (irreversible) |
| Delete lock | none | none | `CanNotDelete` |

A `check` block warns if `production` is planned without HA, geo-redundant backups, the Standard AKS tier and two or more zones.

I have not included cost figures. Azure prices vary by region and change over time. Before applying anything, use the [Azure pricing calculator](https://azure.microsoft.com/pricing/calculator/) or `infracost`. Even dev is not free: AKS nodes, PostgreSQL and Redis bill per hour while they exist.

## Secrets never enter the state file

Terraform state usually contains every attribute of every resource, including passwords. This configuration avoids that (see [ADR-007](adr/ADR-007-secrets-out-of-state.md)):

1. **Ephemeral generation.** `ephemeral "random_password"` produces the PostgreSQL password and the JWT secret during a run and never persists them.
2. **Write-only arguments.** The values reach Azure only through `administrator_password_wo` (PostgreSQL) and `value_wo` (Key Vault secrets). Azure receives them, but Terraform stores neither the value nor a hash.
3. **Rotation.** Write-only values are sent only when their `*_wo_version` changes, so rotating means `secrets_version = 2`. The next apply generates new values and writes the password and the `database-url` secret together. Note that the running pods keep the old `DATABASE_URL` until they restart.
4. **No credentials in the cluster spec.**
   - The API and worker read Key Vault through **workload identity**: a user-assigned identity federated with exactly `system:serviceaccount:clouddeploy:clouddeploy-api` (and `-worker`).
   - That identity holds only the `Key Vault Secrets User` role.
   - AKS pulls images with the kubelet identity (`AcrPull`).
   - No client secret, registry password or static kubeconfig exists.

Terraform itself enforces this. An ephemeral value assigned to a normal argument is a validation error, not just a failing test (verified by mutation in the test suite).

The Redis access key is reported by Azure as a computed attribute, so state is always stored in an encrypted remote backend with Entra ID access control (see below).

The database role setup (a dedicated application role with least-privilege grants) is handled in the security hardening phase, from inside the VNet.

## Remote state

Each environment uses the `azurerm` backend with Entra ID auth (`use_azuread_auth`, no storage account keys). Azure Storage provides blob-lease locking, so two concurrent applies can't corrupt state. Backend settings live in a git-ignored `backend.hcl` (see `backend.hcl.example`).

The storage account is bootstrapped once, outside this configuration: a backend cannot store its own state.

## Commands

| Command | What it does | Needs Azure? |
|---|---|---|
| `make terraform-check` | `fmt -check`, `validate` for every root, `terraform test` (mocked provider) | No |
| `make terraform-plan ENV=dev` | Real plan against your subscription | `az login`, `backend.hcl`, `terraform.tfvars` |
| *(none)* | apply | Manual and deliberate. Run `terraform apply` in `envs/<env>` only when you intend to pay for the resources. |

## How the tests work offline

`mock_provider "azurerm"` replaces the real provider. Plans are computed against the real schema, but no API calls happen and no credentials are needed. The tests assert what we would ask Azure for:
- ACR admin disabled
- PostgreSQL and Redis private
- TLS enforced
- secrets write-only
- AKS identity-only with a network policy engine
- least-privilege role assignments
- the production delete lock

They also check that bad input is rejected:
- an unknown environment
- HA on a Burstable SKU
- an invalid ACR name
- min nodes greater than max
- fragile production

Two mock behaviours shaped the code:
- **The provider's validators still run.** So test inputs use realistic Azure resource IDs, and computed IDs come from mock defaults (with `override_during = plan`).
- **A mock can't add elements to a computed block that isn't in the configuration.** Instead of reading `kubelet_identity[0]` from the cluster, the kubelet identity is created explicitly and assigned `AcrPull` *before* the cluster exists. That is also the better production design: the first image pull can't race the role assignment, and the grant survives rebuilding the cluster.

What mocks can't tell you is whether Azure *accepts* the plan: quotas, SKU availability in a region, or API-level constraints. Only `make terraform-plan` against a real subscription checks those, and only an apply proves them.

## Wiring into Helm (Phase 18)

Outputs carry names and endpoints only, never secret values:
- `acr_login_server`
- `aks_name`
- `key_vault_uri`
- `app_identity_client_id` (annotated onto the service accounts)
- `key_vault_secret_names`

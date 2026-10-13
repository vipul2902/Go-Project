# Running CloudDeploy on Azure (AKS)

This is the path from an empty subscription to CloudDeploy running on AKS. The infrastructure is defined in [terraform.md](terraform.md). Every step that creates billable resources is manual and deliberate.

## How it fits together

```
your laptop ── az login ─────────────► Terraform ──► AKS, ACR, PostgreSQL, Redis, Key Vault
            ── make azure-images ────► ACR Tasks builds clouddeploy + web (linux/amd64)
            ── make azure-deploy ────► Helm ──► AKS

in AKS:
  api / worker / migrate pods
    ├─ workload identity (one Azure identity per service account, no secrets)
    ├─ Secrets Store CSI driver ──► Key Vault ──► <component>-kv Secret (only that component's keys)
    └─ worker ── ACR Tasks API ──► tenant image built on Azure's isolated agents ──► ACR
                                         └─ kubelet identity (AcrPull) pulls it into tenant namespaces
```

### Tenant builds: ACR Tasks

On Azure the worker uses `WORKER_BUILDER=acr` (see [ADR-008](adr/ADR-008-image-builds.md)). Building a tenant's image is running untrusted code, so the cluster never does it. The worker asks ACR Tasks to:

1. clone `repository_url#commit`,
2. run `docker build` on an ephemeral Azure-managed agent,
3. push `clouddeploy/<project>-<id>:<commit>`.

The worker then:
- polls the run,
- copies the build log into the deployment log (the log's SAS URL is never logged),
- cancels the remote run if the deployment is cancelled.

The worker's identity has a custom role with exactly five actions (read registry, schedule, read runs, read logs, cancel), not Contributor.

### Secrets: Key Vault through the CSI driver

| Component | Keys it receives |
|---|---|
| api | `DATABASE_URL`, `REDIS_URL`, `JWT_SECRET` |
| worker | `DATABASE_URL` |
| migrate (pre-install hook) | `DATABASE_URL` |

Each component authenticates as its own identity. The chart refuses to render Azure mode unless every ID is set, and refuses `worker.builder=acr` outside Azure mode.

## Prerequisites (installed in Phase 18)

| Tool | Purpose |
|---|---|
| `az` (Azure CLI) | Login, ACR builds, AKS credentials |
| `kubelogin` | Entra ID authentication to AKS. Local accounts are disabled, so there is no static admin kubeconfig. |
| `terraform`, `helm`, `jq` | Infrastructure, release, output parsing |

## Steps

Each step lists what it does and whether it costs money.

| # | Command | Effect | Billable? |
|---|---|---|---|
| 1 | `az login`, then `make azure-check` | You sign in in your browser. `azure-check` prints the account and subscription only. | No |
| 2 | Create the state storage account once (see [terraform.md](terraform.md#remote-state)), then copy `backend.hcl.example` to `backend.hcl` and `terraform.tfvars.example` to `terraform.tfvars` | Remote state | A few cents per month |
| 3 | `make terraform-plan ENV=dev` | Shows exactly what would be created. Nothing changes. | No |
| 4 | `terraform -chdir=infra/terraform/envs/dev apply` | **Creates the resources.** Run it only after reviewing the plan. | **Yes, hourly** |
| 5 | `make azure-images` | ACR Tasks builds the app and web images, tagged with the git commit | Per build minute |
| 6 | `make azure-credentials` then `make azure-deploy` | Helm release on AKS. Migrations run as a pre-install hook. | Included in 4 |
| 7 | `make azure-forward` | Dashboard on http://localhost:8080 | No |
| 8 | `make azure-stop` / `make azure-start` | Pause and resume AKS and PostgreSQL compute. Data is kept. | Storage only while stopped |
| 9 | `terraform -chdir=infra/terraform/envs/dev destroy` | Removes everything in the environment | Ends billing |

`ENV=dev` deploys with `values-staging.yaml` as the base, which means real ACR builds and no seed data. Seeding (`make seed` style) is local-only, because the seed binary refuses anything except `APP_ENV=development`. To create the first admin on Azure, use the API's register endpoint through `make azure-forward`.

## Before you apply

- **Costs.** Check them for your region with the [pricing calculator](https://azure.microsoft.com/pricing/calculator/). Set a **budget alert** on the subscription before step 4.
- **Quotas.** A new subscription may have a vCPU quota of zero for some VM families, or no access to some SKUs in a region. Step 3 doesn't detect this; step 4 fails with a quota error, and nothing half-built is left that `destroy` can't remove.
- **Untested against a live subscription.** Not yet verified (they become verified in step 4):
  - that ACR Tasks accepts a git context pinned to a commit SHA (`repo#<sha>`)
  - that the custom role's action names are exactly right

  The ACR builder is covered by unit tests against a fake API, and the Terraform by mocked tests.

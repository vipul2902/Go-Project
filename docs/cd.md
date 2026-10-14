# Continuous delivery

Every push to `main` and every version tag runs the [`cd.yml`](../.github/workflows/cd.yml) pipeline. Nothing is published unless CI passed, and no release is created unless the published artifacts passed an end-to-end deployment.

```
push to main / tag v1.2.3
        │
        ▼
   CI (ci.yml)  ── lint, unit, integration, k3s, dashboard, Helm, Terraform, image scans
        │
        ▼
   publish      ── multi-arch images → GHCR, SBOM + provenance, cosign signature
   chart (tags) ── Helm chart → GHCR (OCI), cosign signature
        │
        ▼
   e2e          ── verify signatures → fresh k3s → helm install → real deployment via the API
        │
        ▼
   release (tags) ── GitHub Release with artifact references and verify commands
        │
        ▼
   deploy-azure ── OFF by default; when enabled, waits for manual approval
```

## Artifacts

| Artifact | Where | Tags |
|---|---|---|
| API, worker, migrate image | `ghcr.io/vipul2902/clouddeploy` | `sha-<commit>` (always), `main`, `1.2.3` and `1.2` on releases |
| Dashboard image | `ghcr.io/vipul2902/clouddeploy-web` | Same |
| Helm chart | `oci://ghcr.io/vipul2902/charts/clouddeploy` | `1.2.3` (releases only). Chart version and appVersion both follow the git tag. |

There is no `latest` tag: every deployment names an exact version or commit.

- **Multi-arch (`linux/amd64`, `linux/arm64`).** The Go and Node stages run on the build machine's own architecture and cross-compile. No CPU emulation is involved, so the second architecture costs only compile time.
- **Supply chain.** Each image carries an SBOM and SLSA provenance (BuildKit attestations), plus a GitHub build-provenance attestation.

## Signing (cosign, keyless)

There is no signing key to store, rotate or leak. During the run:
1. GitHub issues the workflow a short-lived OIDC token.
2. Sigstore's certificate authority exchanges it for a certificate naming *this workflow in this repository*.
3. The signature, linked to that certificate, is recorded in the public transparency log.

Signatures are made over the **digest**, because a tag can be moved but a digest can't.

The e2e job only deploys what verifies. Anyone can check an image the same way:

```bash
cosign verify ghcr.io/vipul2902/clouddeploy:1.2.3 \
  --certificate-identity-regexp '^https://github\.com/vipul2902/Go-Project/\.github/workflows/cd\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## End-to-end test

[`scripts/e2e.sh`](../scripts/e2e.sh) installs the published images into a fresh k3s cluster (for releases, the published OCI chart too) and drives the product through its real API:

1. In-cluster PostgreSQL and Redis. A `restricted` Pod Security namespace. Generated credentials that are never printed.
2. `helm install` with the staging values (NetworkPolicies on) and `--wait`. Migrations run as the pre-install hook.
3. Through the dashboard's nginx:
   1. register a user
   2. create a project and an environment
   3. trigger a deployment
4. The worker claims the deployment from the PostgreSQL queue and deploys a real workload into a tenant namespace with the Kubernetes deployer. The build step is the dry-run builder, and the script pre-builds the tiny image it names.
5. The run passes only if the deployment ends in `RUNNING` **and** the environment's live health is `healthy`.

The same script runs locally against Colima, alongside a dev install, and cleans up after itself:

```bash
make e2e
```

## Releasing

```bash
git tag -a v0.2.0 -m "v0.2.0" && git push origin v0.2.0
```

The tag runs the whole pipeline. The GitHub Release appears only if e2e passes.

## Deploying to Azure (gated)

`deploy-azure` runs only for tags and only when the repository variable `AZURE_DEPLOY_ENABLED` is `true`. It then waits for a reviewer to approve the **production** environment. It:

1. signs in with **OIDC federation**: the IDs are repository variables; the trust is a federated credential in Azure scoped to this repository's `production` environment; no client secret exists
2. reads the infrastructure outputs from remote Terraform state
3. imports the release images from GHCR into ACR with `az acr import` (digests unchanged)
4. deploys with `make azure-deploy`

| Repository variable | Value |
|---|---|
| `AZURE_DEPLOY_ENABLED` | `true` |
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` | The deploy identity |
| `TFSTATE_RESOURCE_GROUP`, `TFSTATE_STORAGE_ACCOUNT` | The remote state backend |

The production environment also needs required reviewers (**Settings → Environments → production**).

## Permissions

| Job | Token permissions |
|---|---|
| CI | `contents: read` |
| publish | `contents: read`, `packages: write`, `id-token: write`, `attestations: write` |
| chart | `contents: read`, `packages: write`, `id-token: write` |
| e2e | `contents: read`, `packages: read` |
| release | `contents: write` |
| deploy-azure | `contents: read`, `id-token: write` |

Each job gets only what it uses. Pull requests run CI only; nothing in CD runs for pull requests, so code from forks never gets publishing rights.

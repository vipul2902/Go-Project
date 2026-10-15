# ADR-006: Terraform structure (composition module, per-environment roots)

**Status:** Accepted (Phase 17)

## Context

CloudDeploy needs three Azure environments: dev, staging and prod. They have the same shape and different sizes. The common options:

1. **One root with workspaces.** One configuration, selected with `terraform workspace`. Workspaces share the backend and provider settings, and the active environment is invisible in code review. A forgotten `workspace select` applies to the wrong environment.
2. **Copy-pasted roots.** Each environment repeats every resource, so the environments drift apart over time.
3. **Per-environment roots calling one composition module.** Each `envs/<env>` is a thin root (provider, backend, sizing) that calls `modules/platform`.

## Decision

Option 3. Single-concern modules (`network`, `acr`, `aks`, `postgres`, `redis`, `keyvault`) are composed by `modules/platform`, and each environment root passes only sizing inputs.

- **Separate state per environment.** It sits in a separate backend key, so a dev apply can't touch prod state and the blast radius is one environment.
- **Validation lives in the modules.** Variable `validation`, resource `precondition` and a `check` block for production resilience mean bad input fails at plan time, wherever it comes from.
- **Tests run offline.** `terraform test` uses `mock_provider` and runs in CI without cloud credentials.
- **No apply target.** Applying stays a deliberate, reviewed step. The CD pipeline's Azure job deploys releases only, behind a manual environment approval ([cd.md](../cd.md)).

## Consequences

- Adding a resource means editing a module once; all environments pick it up on their next plan.
- Environment differences are visible in one file per environment, which is easy to review.
- Module sources are local paths, not versioned registry modules. That's fine for one repository. A multi-repo setup would publish versioned modules so environments can upgrade independently.

# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: **Security → Report a vulnerability** on this repository. Don't open a public issue.

Include what you found, how to reproduce it, and the impact you expect. You'll get an acknowledgement, and the fix will be credited in the release notes unless you prefer otherwise.

## Supported versions

Only the latest release receives fixes.

## Verifying releases

Release images and the Helm chart are signed with cosign (keyless) by this repository's CD workflow. See [docs/cd.md](docs/cd.md#signing-cosign-keyless) for the verify command.

## Design

The threat model and the controls behind it are in [docs/threat-model.md](docs/threat-model.md). Authentication, tokens, RBAC and database privileges are covered in [docs/security.md](docs/security.md).

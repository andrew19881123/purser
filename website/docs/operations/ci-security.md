# CI/CD Supply Chain Security

This page describes the supply chain hardening measures applied to Purser's GitHub
Actions pipelines as of v0.3.

---

## SHA-pinned GitHub Actions

All `uses:` references in `.github/workflows/` are pinned to **immutable commit SHAs**
rather than mutable version tags.

### Why tags are dangerous

A version tag like `actions/checkout@v4` is a *mutable pointer* — the repository owner
(or an attacker who has compromised the owner's account) can silently move that tag to a
different commit containing malicious code. Because CI runners pull the action at
execution time, the next pipeline run would execute the tampered code with full access
to your repository secrets and environment.

The [tj-actions/changed-files incident (March 2025)](https://www.stepsecurity.io/blog/harden-runner-detection-github-actions-tj-actions-changed-files-supply-chain-attack)
is a real-world example: a compromised maintainer account was used to overwrite a
widely-used action's tag, leaking CI secrets from thousands of repositories.

### What we do instead

Every action is referenced by the 40-character commit SHA it resolves to at pin time,
with a human-readable version comment:

```yaml
# Before (INSECURE — tag is mutable):
- uses: actions/checkout@v4

# After (SECURE — SHA is immutable):
- uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262  # v4.4.0
```

A SHA cannot be moved. Even if the upstream repository is compromised and the tag is
overwritten, the pinned SHA will either continue to resolve to the original (safe)
commit, or fail to resolve entirely — both outcomes are safe.

---

## Keeping pins up to date with Dependabot

Pinning to a SHA can cause pins to go stale as upstream actions ship security fixes.
Purser's `.github/dependabot.yml` includes a `github-actions` entry that runs weekly
and opens pull requests whenever a pinned SHA has been superseded by a newer release:

```yaml
- package-ecosystem: "github-actions"
  directory: "/"
  schedule:
    interval: "weekly"
    day: "monday"
  groups:
    github-actions:
      patterns: ["*"]
  commit-message:
    prefix: "chore(ci)"
  labels: ["dependencies", "ci"]
```

Dependabot automatically updates the SHA *and* the version comment. Review these PRs
before merging — read the action's changelog to confirm the new version is safe.

---

## Container CVE scanning (Trivy)

Every CI run includes a `security-scan` job that builds the gateway and control-plane
container images locally (no push) and scans them with
[Aqua Security Trivy](https://trivy.dev/):

- Scope: `CRITICAL` and `HIGH` severity CVEs only.
- `ignore-unfixed: true` — ignores CVEs for which no patch is yet available, reducing
  noise without hiding actionable findings.
- `exit-code: '1'` — the job fails if any patchable CRITICAL/HIGH CVE is found.
- Results are uploaded in SARIF format to the **GitHub Security** tab
  (`/security/code-scanning`) so they appear alongside CodeQL findings.

The scan runs in parallel with the `rust`, `go`, `ui`, and `proto` jobs. It does **not**
gate other jobs — `security-scan` is an independent quality signal, not a build
dependency.

### Updating the Trivy action

Trivy releases new CVE database snapshots frequently. The action version is pinned like
all others. Dependabot will open a PR when a new version is available. Merging the PR
also picks up the latest CVE database at the time the action was released.

---

## SLSA L2 Provenance

Release builds use the
[`slsa-framework/slsa-github-generator`](https://slsa.dev/spec/v1.0/levels) reusable
workflow (pinned to SHA) to generate SLSA Level 2 provenance attestations for all
binary artifacts. Provenance files are attached to every GitHub Release and can be
verified with the [slsa-verifier](https://github.com/slsa-framework/slsa-verifier) tool.

---

## Cosign image signing

Every container image pushed to GHCR during a release is signed with
[Sigstore Cosign](https://docs.sigstore.dev/) using the GitHub Actions OIDC token —
no long-lived keys or secrets required. Signatures are stored in the same GHCR namespace.

Verify an image signature:

```bash
cosign verify \
  --certificate-identity-regexp 'https://github.com/andrew19881123/purser/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/andrew19881123/purser-gateway:<TAG>
```

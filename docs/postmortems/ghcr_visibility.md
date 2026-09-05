# Post-mortem: GHCR packages start private; visibility API broken for user packages

## Symptom
After `docker push ghcr.io/<user>/<image>:tag`, anonymous `docker pull` and
`helm pull oci://ghcr.io/…` return 401 / "not found" even though the push
succeeded and the package appears in GitHub UI.

## Root cause
GitHub Container Registry creates new packages as **private** by default.
The documented API to change visibility:
```
PATCH /user/packages/container/<pkg>/visibility
```
returns **404** for user-owned (non-org) packages. This is a known GitHub API
inconsistency — the endpoint works for org packages but silently 404s for user
packages, giving no indication that it failed.

## Fix applied (manual, one-time per new package)
Set visibility to **Public** via the GitHub UI:
```
https://github.com/users/andrew19881123/packages/container/<pkg>/settings
→ "Change package visibility" → Public
```
This is a one-time action per package name. Once public, all future tags
pushed to the same package name are immediately publicly pullable.

## Packages currently public (no pull secret needed)
- `ghcr.io/andrew19881123/purser-control-plane`
- `ghcr.io/andrew19881123/purser-gateway`
- `ghcr.io/andrew19881123/purser-ui`
- `ghcr.io/andrew19881123/charts/purser` (OCI Helm chart)

## Checklist for a new image/chart
1. `docker push` or `helm push` succeeds.
2. Go to the package settings URL above and set **Public**.
3. Verify with an anonymous pull (unset `DOCKER_CONFIG` / `HELM_REGISTRY_CONFIG`).
4. Only then update the docs / README to reference the new tag.

## Automated release pipeline note
`.github/workflows/release.yml` uses `GITHUB_TOKEN` for GHCR push (write:packages
scope is granted automatically by GitHub Actions). The token is sufficient for
pushing to existing public packages. For a **brand-new** package name created by
the release pipeline, a human must set it public via the UI before the first
`helm install oci://…` can work anonymously.

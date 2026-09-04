# Licensing — Purser MIT core + source-available enterprise

Purser follows the licensing model popularized by LiteLLM: a permissive,
MIT-licensed core, plus a source-available `enterprise/` directory whose
features are gated behind a commercial license key.

## Core — MIT

Everything in this repository **outside** the `enterprise/` directory is free
and open source under the **MIT License** (see [LICENSE](LICENSE)). You may run,
study, modify, and redistribute it — including for commercial purposes — with no
copyleft obligations.

The MIT-licensed core includes the full single-cluster orchestration stack:
Agent, Engine Adapter (mock + llama.cpp), Planner, Control Plane (registry,
orchestrator, reconciler, internal PKI), API Gateway, and the Dashboard.

## Enterprise — source-available, key-gated

The `enterprise/` directory is **source-available** under the **Purser
Enterprise License** (see [enterprise/LICENSE](enterprise/LICENSE)).

Unlike a classic open-core split, the enterprise code is **public**: you can
view, compile, modify, and use it for development, evaluation, and testing.
However, **use in production or for commercial purposes requires a valid
commercial license**, activated at runtime with a `PURSER_LICENSE_KEY`. This is
the same public-but-key-gated model used by LiteLLM — the code ships in the
open, but the license check enforces commercial terms for production use.

Enterprise features:

- **High availability** — leader election (Raft) + replicated registry; Gateway
  HA behind a VIP.
- **Identity & access** — RBAC, SSO/SAML/OIDC, LDAP/AD integration.
- **Compliance** — tamper-evident audit log, strong per-tenant isolation,
  chargeback/usage accounting.
- **Fleet at scale** — MDM/Ansible/golden-image enrollment, signed air-gap
  bundles, enterprise CA integration, offline license validation.

## Commercial licensing & contributions

- For a commercial/enterprise license (a valid `PURSER_LICENSE_KEY`), contact:
  **andrew19881123@gmail.com**.
- Contributions are accepted under the Developer Certificate of Origin (see
  [CONTRIBUTING.md](CONTRIBUTING.md)). Contributions to the core are licensed
  under the MIT License; contributions to the `enterprise/` directory are
  licensed under the Purser Enterprise License.

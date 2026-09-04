# Licensing — Purser open-core model

Purser follows an **open-core** model.

## Community Edition (this repository) — AGPL-3.0

The core of Purser is free and open source under the **GNU Affero General Public
License v3.0** (see [LICENSE](LICENSE)). You may run, study, modify and
redistribute it. Note the AGPL's network clause: if you run a modified version to
provide a service over a network, you must make your modified source available to
the users of that service.

The Community Edition includes the full single-cluster orchestration stack:
Agent, Engine Adapter (mock + llama.cpp), Planner, Control Plane (registry,
orchestrator, reconciler, internal PKI), API Gateway, and the Dashboard.

## Enterprise Edition — commercial license

Some capabilities aimed at larger or regulated organizations are **not** covered
by the AGPL and require a separate commercial agreement. These are kept behind
clean module boundaries, enabled by license, and are **not** part of this
repository:

- **High availability** — leader election (Raft) + replicated registry; Gateway
  HA behind a VIP.
- **Identity & access** — RBAC, SSO/SAML/OIDC, LDAP/AD integration.
- **Compliance** — tamper-evident audit log, strong per-tenant isolation,
  chargeback/usage accounting.
- **Fleet at scale** — MDM/Ansible/golden-image enrollment, signed air-gap
  bundles, enterprise CA integration, offline license validation.

## Commercial licensing & contributions

- For a commercial/enterprise license, contact: **andrew19881123@gmail.com**.
- Contributions to the Community Edition are accepted under the Developer
  Certificate of Origin (see [CONTRIBUTING.md](CONTRIBUTING.md)); by contributing
  you license your work under AGPL-3.0.

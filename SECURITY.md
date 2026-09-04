# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Instead, report privately via GitHub's **"Report a vulnerability"** (Security →
Advisories) on this repository, or email **andrew19881123@gmail.com**. We aim to
acknowledge reports within a few business days.

## Security model & assumptions

Purser is designed to run on a **trusted LAN**, and its deployment must respect
that boundary:

- **No data leaves the perimeter by design.** The control plane performs no
  outbound telemetry and can run fully air-gapped.
- **Control-plane traffic is mTLS** (Control Plane ↔ Agents), with an internal CA
  that issues and rotates per-node certificates.
- **The inference engine's RPC worker is not sandboxed.** It must be bound only
  to a trusted subnet interface and must **never** be exposed to the public
  internet. The llama.cpp adapter enforces a bind-address safety check before
  launching a worker.
- **Secrets** (join tokens, private keys) are never logged and must never be
  committed. Runtime state (`*.db`, `pki-state/`) is git-ignored.

Reports that depend on violating these assumptions (e.g. exposing the engine
worker publicly) may be treated as configuration issues rather than
vulnerabilities, but we still welcome the report.

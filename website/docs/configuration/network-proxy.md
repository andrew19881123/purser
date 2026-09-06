# Network Proxy and Custom CA Bundle

Purser supports operation in enterprise networks where outbound HTTPS traffic is
routed through a corporate proxy and where internal services use certificates
signed by a private Certificate Authority (CA).

---

## How it works

| Component | Proxy mechanism | CA bundle |
|---|---|---|
| **Agent** | `PURSER_AGENT_HTTP(S)_PROXY` / `PURSER_AGENT_NO_PROXY` | `PURSER_AGENT_CA_BUNDLE` |
| **Gateway** | `PURSER_GATEWAY_HTTP(S)_PROXY` / `PURSER_GATEWAY_NO_PROXY` | `PURSER_GATEWAY_CA_BUNDLE` |
| **Control plane** | `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` (standard Go) | `PURSER_CA_BUNDLE` |

The control plane honours the conventional `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`
environment variables automatically via Go's standard `net/http` proxy detection.
You only need to set the Purser-specific variables for the agent and gateway.

---

## Environment variables

### Agent (`purser-agent`)

| Variable | Description |
|---|---|
| `PURSER_AGENT_HTTP_PROXY` | HTTP proxy URL for outbound plain-HTTP traffic (e.g. `http://proxy.corp:3128`). Used as a fallback when `PURSER_AGENT_HTTPS_PROXY` is not set. |
| `PURSER_AGENT_HTTPS_PROXY` | HTTPS proxy URL for outbound TLS traffic. Takes precedence over `PURSER_AGENT_HTTP_PROXY` for HTTPS destinations. |
| `PURSER_AGENT_NO_PROXY` | Comma-separated list of hosts, domains, or CIDR ranges to bypass the proxy (e.g. `localhost,10.0.0.0/8,.internal`). |
| `PURSER_AGENT_CA_BUNDLE` | Absolute path to a PEM file containing one or more CA certificates to trust in addition to the system root store. Required when model mirrors or the control plane use private-CA-signed certificates. |

### Gateway (`purser-gateway`)

| Variable | Description |
|---|---|
| `PURSER_GATEWAY_HTTP_PROXY` | HTTP proxy URL for outbound plain-HTTP traffic. |
| `PURSER_GATEWAY_HTTPS_PROXY` | HTTPS proxy URL for outbound TLS traffic. Takes precedence over `PURSER_GATEWAY_HTTP_PROXY`. |
| `PURSER_GATEWAY_NO_PROXY` | Comma-separated proxy bypass list. |
| `PURSER_GATEWAY_CA_BUNDLE` | Absolute path to a PEM file with additional trusted CA certificates. |

### Control plane (`purser-controlplane`)

| Variable | Description |
|---|---|
| `HTTP_PROXY` | HTTP proxy URL (standard Go convention). |
| `HTTPS_PROXY` | HTTPS proxy URL (standard Go convention). Takes precedence over `HTTP_PROXY` for TLS destinations. |
| `NO_PROXY` | Comma-separated proxy bypass list (standard Go convention). |
| `PURSER_CA_BUNDLE` | Absolute path to a PEM file with additional trusted CA certificates. Applied to all outbound HTTP connections in the process, including OIDC discovery and HuggingFace model imports. |

---

## Preparing a CA bundle PEM file

Obtain the root (and any intermediate) CA certificates from your IT/security team in
PEM format. Concatenate them into a single file:

```bash
cat corp-root-ca.pem corp-intermediate-ca.pem > /etc/purser/ca-bundle.pem
```

Each certificate block must begin with `-----BEGIN CERTIFICATE-----` and end with
`-----END CERTIFICATE-----`.

---

## Examples

### Squid proxy with a private CA

```bash
# Agent
export PURSER_AGENT_HTTPS_PROXY=http://squid.corp.example.com:3128
export PURSER_AGENT_NO_PROXY=localhost,127.0.0.1,10.0.0.0/8
export PURSER_AGENT_CA_BUNDLE=/etc/purser/corp-ca.pem

# Gateway
export PURSER_GATEWAY_HTTPS_PROXY=http://squid.corp.example.com:3128
export PURSER_GATEWAY_NO_PROXY=localhost,127.0.0.1,10.0.0.0/8
export PURSER_GATEWAY_CA_BUNDLE=/etc/purser/corp-ca.pem

# Control plane
export HTTPS_PROXY=http://squid.corp.example.com:3128
export NO_PROXY=localhost,127.0.0.1,10.0.0.0/8
export PURSER_CA_BUNDLE=/etc/purser/corp-ca.pem
```

### mitmproxy for debugging

```bash
export PURSER_AGENT_HTTPS_PROXY=http://localhost:8080
export PURSER_AGENT_CA_BUNDLE=/path/to/mitmproxy/mitmproxy-ca-cert.pem
```

---

## docker-compose example

```yaml
services:
  purser-agent:
    image: ghcr.io/purser/purser-agent:latest
    environment:
      PURSER_AGENT_HTTPS_PROXY: "http://squid.corp.example.com:3128"
      PURSER_AGENT_NO_PROXY: "localhost,10.0.0.0/8"
      PURSER_AGENT_CA_BUNDLE: "/etc/purser/corp-ca.pem"
    volumes:
      - ./corp-ca.pem:/etc/purser/corp-ca.pem:ro

  purser-gateway:
    image: ghcr.io/purser/purser-gateway:latest
    environment:
      PURSER_GATEWAY_HTTPS_PROXY: "http://squid.corp.example.com:3128"
      PURSER_GATEWAY_NO_PROXY: "localhost,10.0.0.0/8"
      PURSER_GATEWAY_CA_BUNDLE: "/etc/purser/corp-ca.pem"
    volumes:
      - ./corp-ca.pem:/etc/purser/corp-ca.pem:ro

  purser-controlplane:
    image: ghcr.io/purser/purser-controlplane:latest
    environment:
      HTTPS_PROXY: "http://squid.corp.example.com:3128"
      NO_PROXY: "localhost,10.0.0.0/8"
      PURSER_CA_BUNDLE: "/etc/purser/corp-ca.pem"
    volumes:
      - ./corp-ca.pem:/etc/purser/corp-ca.pem:ro
```

---

## Air-gap deployment (no outbound internet)

In a fully air-gapped environment you typically want Purser to use internal services
only and never attempt to reach the internet:

1. Set `NO_PROXY` (or `PURSER_AGENT_NO_PROXY`) to `*` to bypass the proxy for all
   hosts, or list every internal hostname/CIDR:

    ```bash
    export PURSER_AGENT_NO_PROXY="*"
    export NO_PROXY="*"
    ```

2. Supply `PURSER_AGENT_CA_BUNDLE` / `PURSER_CA_BUNDLE` so that internal TLS
   endpoints (model mirrors, OIDC IdP, HuggingFace mirror) are trusted.

3. Point `PURSER_MODEL_MIRROR_URL` (agent) at your internal model registry instead
   of the public HuggingFace hub.

4. Point `PURSER_OIDC_ISSUER` (control plane) at your internal identity provider.

The combination of a private CA bundle and `NO_PROXY=*` ensures that all traffic
stays within your network while certificates remain trusted.

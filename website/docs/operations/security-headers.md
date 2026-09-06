# Security Headers

Purser's nginx configuration ships with a modern HTTP security-header stack.
This page explains each header, the rationale behind the chosen values, and
the caveats you need to be aware of before deploying.

---

## Header reference

### Strict-Transport-Security (HSTS)

```
Strict-Transport-Security: max-age=31536000; includeSubDomains; preload
```

Instructs browsers to connect to this origin exclusively over HTTPS for the
next 365 days. `includeSubDomains` extends the policy to every sub-domain of
the same hostname; `preload` opts the domain into the browser HSTS preload
list so first-time visitors also get HTTPS enforcement before the first
response arrives.

**Critical caveat:** HSTS must only be served when nginx is running behind a
TLS-terminating ingress or load balancer. If you accidentally send this header
over plain HTTP, browsers will refuse to connect to the site over HTTP for up
to a year. The `demo-nginx.conf` configuration intentionally omits HSTS for
this reason.

---

### Content-Security-Policy (CSP)

```
Content-Security-Policy: default-src 'self'; script-src 'self' 'unsafe-inline';
  style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:;
  connect-src 'self'; font-src 'self'; object-src 'none';
  frame-ancestors 'none'; base-uri 'self'; form-action 'self';
  upgrade-insecure-requests
```

CSP is the primary defence against Cross-Site Scripting (XSS). Each directive
restricts where the browser is allowed to load a certain type of resource.

| Directive | Value | Why |
|---|---|---|
| `default-src` | `'self'` | Baseline: only same-origin resources allowed |
| `script-src` | `'self' 'unsafe-inline'` | Required by React (see note below) |
| `style-src` | `'self' 'unsafe-inline'` | Required by React CSS-in-JS / Vite |
| `img-src` | `'self' data: blob:` | Allows inline SVG/canvas snapshots |
| `connect-src` | `'self'` | Restricts `fetch`/XHR/WebSocket to same origin |
| `font-src` | `'self'` | Fonts from the bundle only |
| `object-src` | `'none'` | Disables plugins (Flash, PDF viewers) |
| `frame-ancestors` | `'none'` | Equivalent to `X-Frame-Options: DENY` |
| `base-uri` | `'self'` | Prevents `<base>` tag injection attacks |
| `form-action` | `'self'` | Restricts form `POST` targets |
| `upgrade-insecure-requests` | — | Upgrades sub-resource HTTP requests to HTTPS |

**Why `unsafe-inline` for scripts?**

Vite's production build inlines a small bootstrap snippet directly in
`index.html`. React also sets inline event handlers in some environments.
Without `unsafe-inline`, the app fails to start. The correct long-term fix is
to add a per-request cryptographic `nonce` to every inline script tag and to
the CSP header — this requires server-side rendering or a Vite plugin
(e.g. `vite-plugin-csp`). Until that work is done, `unsafe-inline` is the
pragmatic choice.

---

### X-Content-Type-Options

```
X-Content-Type-Options: nosniff
```

Prevents the browser from MIME-sniffing a response away from the declared
`Content-Type`. Stops a stored XSS vector where an attacker uploads a file
that the browser mistakenly executes as JavaScript.

---

### X-Frame-Options

```
X-Frame-Options: DENY
```

Prevents the UI from being embedded in `<iframe>`, `<frame>`, or `<object>`
elements on other origins. Defends against clickjacking. The `frame-ancestors
'none'` directive in the CSP header provides the same protection for modern
browsers; `X-Frame-Options` is kept for legacy browser compatibility.

---

### Referrer-Policy

```
Referrer-Policy: strict-origin-when-cross-origin
```

On same-origin navigations the full URL is sent as `Referer`. On cross-origin
navigations only the origin (scheme + host) is sent, and nothing at all when
downgrading from HTTPS to HTTP. This prevents leaking path and query-string
tokens to third-party services (analytics, CDNs, external APIs).

The `demo-nginx.conf` uses `no-referrer` because the demo runs on plain HTTP
and there is no origin worth preserving.

---

### Permissions-Policy

```
Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(),
  usb=(), bluetooth=(), serial=(), hid=()
```

Explicitly disables browser features that the Purser UI never uses. An empty
allowlist `()` means no origin — including the page itself — may request the
permission. This limits the blast radius if an XSS vulnerability is ever
exploited: the attacker cannot silently activate the camera or request the
user's location.

---

### Cross-Origin-Opener-Policy (COOP)

```
Cross-Origin-Opener-Policy: same-origin
```

Isolates the browsing context group so that cross-origin windows opened by
the page cannot share a JavaScript handle back to the Purser tab. This
mitigates Spectre-class side-channel attacks that can read cross-origin
memory via `SharedArrayBuffer`.

---

### Cross-Origin-Resource-Policy (CORP)

```
Cross-Origin-Resource-Policy: same-origin
```

Prevents other origins from loading Purser's sub-resources (scripts, images,
fonts) via `<img>`, `<script>`, etc. Without this header, a malicious page on
a different origin could include Purser's bundle and extract timing
information from it.

---

## Removed header: X-XSS-Protection

The old configuration shipped `X-XSS-Protection: 1; mode=block`. This header
is deprecated and has been removed for two reasons:

1. Modern browsers (Chrome 78+, Firefox, Safari) have already removed their
   built-in XSS auditors.
2. The auditor in older Internet Explorer versions contained bugs that could
   be exploited to *introduce* XSS rather than prevent it.

The CSP `script-src` directive is the correct modern replacement.

---

## Demo / development config

`deploy/docker/demo-nginx.conf` is used for the local `docker compose` demo
stack. It carries only a minimal subset of headers:

- `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` (no-referrer)
- **No HSTS** — the demo runs on plain HTTP; sending HSTS would brick the
  browser connection for up to a year.
- **No CSP** — the demo proxy sits in front of the UI container which already
  serves its own CSP from `nginx.conf`.

---

## Verifying headers in production

Use [Mozilla Observatory](https://observatory.mozilla.org/) to grade your
deployment's header configuration. A correctly deployed Purser instance should
score **A** or above.

For local testing without a public URL, `curl -I https://your-host/` and
inspect the response headers, or use the browser DevTools Network tab.

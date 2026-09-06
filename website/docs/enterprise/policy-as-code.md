# Policy-as-Code (OPA/Rego)

Purser embeds [Open Policy Agent](https://www.openpolicyagent.org/) so operators
can write governance rules as versioned Rego code instead of hard-coding access
logic into the platform. Policies live in the database, survive restarts, and take
effect without redeploying Purser.

!!! enterprise
    The `policy_engine` feature requires an Enterprise license.
    See [Licensing](licensing.md).

---

## How it works

1. Operators push Rego policies via `PUT /api/v1/policies/{name}`.
2. Purser compiles every enabled policy and loads them into the embedded OPA
   engine (no sidecar, no network calls to an OPA server).
3. Before any **deploy** action, the engine evaluates the request against all
   active policies. All policies must return `allow = true`; a single `false`
   denies the request (default-deny per policy, open-by-default when no policies
   exist).
4. Policies are reloaded atomically on every `PUT` or `DELETE`.

**Input document** available in every policy:

| Field | Type | Description |
|-------|------|-------------|
| `input.action` | string | `"deploy"`, `"infer"`, `"read_audit"` |
| `input.model_id` | string | Model being acted on |
| `input.tenant_id` | string | Caller's tenant (from API key) |
| `input.key_hash` | string | SHA-256 hex of the API key |
| `input.claims` | object | Extra OIDC claims (string→string map) |

---

## Example policies

### Allow only approved models

```rego
package purser

default allow = false

allow if {
    input.action == "deploy"
    input.model_id == "qwen3-moe-235b"
}

allow if {
    input.action == "infer"
}
```

### Restrict large models to a specific team

```rego
package purser

default allow = false

large_models := {"llama3-405b", "mistral-22b"}

# Small models: anyone can deploy.
allow if {
    input.action == "deploy"
    not large_models[input.model_id]
}

# Large models: only the ml-platform-team tenant.
allow if {
    input.action == "deploy"
    large_models[input.model_id]
    input.tenant_id == "ml-platform-team"
}

allow if { input.action == "infer" }
allow if { input.action == "read_audit" }
```

### Require specific key hash (break-glass controls)

```rego
package purser

default allow = false

approved_keys := {
    "a1b2c3d4..."   # ops-team key
}

allow if {
    approved_keys[input.key_hash]
}
```

---

## API reference

### List policies

```
GET /api/v1/policies
```

Returns all policies (enabled and disabled).

**Response 200:**
```json
{
  "policies": [
    {
      "id": 1,
      "name": "model-allowlist",
      "rego": "package purser\n...",
      "enabled": true,
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  ]
}
```

---

### Create or update a policy

```
PUT /api/v1/policies/{name}
```

**Request body:**
```json
{
  "rego": "package purser\ndefault allow = false\nallow = true",
  "enabled": true
}
```

- `rego` — Rego source code (required). Purser compiles it before storing; an
  invalid policy returns **400 Bad Request** with the compilation error.
- `enabled` — whether the policy is loaded into the engine (default: `true`).

**Responses:**

| Code | Meaning |
|------|---------|
| 200 | Policy stored and engine reloaded |
| 400 | Invalid Rego or missing fields |
| 402 | Enterprise license required |

---

### Delete a policy

```
DELETE /api/v1/policies/{name}
```

Removes the policy and reloads the engine immediately.

**Responses:**

| Code | Meaning |
|------|---------|
| 204 | Deleted |
| 404 | Policy not found |
| 402 | Enterprise license required |

---

### Dry-run evaluation

```
POST /api/v1/policies/eval
```

Evaluate a hypothetical request against the current engine without affecting
real traffic. Useful for testing policies before they gate production deploys.

**Request body:**
```json
{
  "action":    "deploy",
  "model_id":  "llama3-405b",
  "tenant_id": "ml-platform-team",
  "key_hash":  "",
  "claims":    {}
}
```

**Response 200:**
```json
{ "allowed": false, "reason": "denied by policy \"model-allowlist\"" }
```

---

## Open-by-default semantics

When **no policies are stored** (or all are disabled) the engine allows all
requests — the same behaviour as when the feature is not licensed. This means:

- Removing the last policy never locks out the operator.
- Disabling and re-enabling the `policy_engine` feature is safe at any time.

---

## Operational notes

- Policies are compiled at `PUT` time; a bad compile never overwrites the
  existing engine state.
- Policy evaluation is synchronous and in-process (no network hop).
- Each policy is compiled independently; a single failing policy is rejected
  at upload, not at evaluation time.
- The `name` field is the upsert key — re-PUT with the same name to update.
- All policy mutations are recorded in the administrative audit log.

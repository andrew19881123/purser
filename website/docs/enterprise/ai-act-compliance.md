# AI Act & GDPR Compliance

Purser ships machine-readable compliance documents that help regulated organisations
meet EU AI Act and GDPR obligations without custom tooling.

!!! note "Enterprise feature"
    The compliance reporting endpoints require an enterprise license.
    `GET /api/v1/compliance/ai-act/technical-doc` requires the `ai_act_compliance`
    or `inference_audit` feature.
    `GET /api/v1/compliance/gdpr/record-of-processing` requires the `inference_audit`
    feature.

---

## AI Act obligations covered

### Art.12(1)(a) — System identity and version traceability

Every inference event logged by Purser now carries four additional fields that
uniquely identify the model checkpoint that produced the output:

| Field | Description |
|---|---|
| `model_revision` | HuggingFace commit hash or checkpoint digest |
| `model_quantization` | Quantization format, e.g. `q4_k_m`, `fp16`, `awq` |
| `node_id` | Opaque identifier of the agent node that served the request |
| `inference_engine` | Runtime and version, e.g. `llama.cpp/b3497` |

These fields appear in every record returned by
`GET /api/v1/inference-audit` and in the hash-chain verification performed by
`GET /api/v1/inference-audit/verify`. Earlier records written before this feature
was introduced carry empty strings for these fields — the hash chain remains intact
because empty strings are valid field values.

### Art.11 — Technical documentation

`GET /api/v1/compliance/ai-act/technical-doc` returns a JSON document conforming to
AI Act Art.11 and Annex IV requirements. Generate it at any time as evidence for a
conformity assessment or post-incident audit:

```bash
curl -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
     https://<control-plane>/api/v1/compliance/ai-act/technical-doc | jq .
```

Example response:

```json
{
  "generated_at": "2026-09-05T12:00:00Z",
  "system_name": "Purser AI Inference Gateway",
  "provider": "Acme Corp",
  "version": "v0.3.0",
  "deployed_models": [
    { "model_id": "qwen3-moe-235b", "deployed_at": "2026-08-01T09:00:00Z" }
  ],
  "human_oversight_measures": {
    "deployment_approvals": true,
    "inference_audit":      true,
    "policy_engine":        true
  },
  "data_governance": {
    "prompt_content_stored": false,
    "pii_fields": [
      "api_key_hash (pseudonymised SHA-256)",
      "client_ip_prefix (/24 CIDR only)"
    ],
    "retention_configurable": true
  },
  "audit_log": {
    "tamper_evident":  true,
    "algorithm":       "SHA-256 hash chain",
    "inference_chain": true
  },
  "total_api_keys": 12,
  "conformity_basis": "AI Act Art.11, Annex IV"
}
```

`prompt_content_stored` is always `false` — Purser records token counts and
metadata only, never the prompt or completion text (GDPR Art.5 data minimisation).

### Art.14 — Human oversight

Deployment approval gates (the `deployment_approvals` enterprise feature) implement
the human oversight requirement. Every model rollout requires an explicit approval by
an admin before traffic is routed to the new deployment. See
[Deployment Approval Gates](deployment-approvals.md).

---

## GDPR Art.30 — Record of processing activities

`GET /api/v1/compliance/gdpr/record-of-processing` returns a structured record of
all processing activities performed by Purser, ready to include in your
organisation's Art.30 register:

```bash
curl -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
     https://<control-plane>/api/v1/compliance/gdpr/record-of-processing | jq .
```

Example response:

```json
{
  "generated_at": "2026-09-05T12:00:00Z",
  "controller":   "Acme Corp",
  "processing_activities": [
    {
      "name":          "Inference Audit Logging",
      "legal_basis":   "GDPR Art.6(1)(c) - legal obligation (AI Act Art.12)",
      "data_subjects": ["API users", "end users of deployed AI systems"],
      "data_categories": [
        "usage patterns",
        "network identifiers (pseudonymised)"
      ],
      "retention_period": "730 days (configurable)",
      "recipients":       ["internal compliance team"],
      "third_country_transfers": false,
      "technical_measures": [
        "pseudonymisation (SHA-256 hash of API key)",
        "hash chain integrity (SHA-256)",
        "TLS in transit",
        "IP prefix truncation (/24)"
      ]
    },
    {
      "name":          "API Key Management",
      "legal_basis":   "GDPR Art.6(1)(b) - contract performance",
      "data_categories": [
        "API key hash (not reversible)",
        "team identifier"
      ],
      "retention_period": "until revoked",
      "technical_measures": ["SHA-256 hash only (no plaintext stored)"]
    }
  ]
}
```

---

## Model version traceability — querying audit records

To find all inference events served by a specific model version, filter the audit
log by `model_id` and inspect the version fields:

```bash
curl -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
     "https://<control-plane>/api/v1/inference-audit?model_id=qwen3-moe-235b" | jq \
  '[.events[] | {request_id, model_revision, model_quantization, node_id, inference_engine}]'
```

This enables post-incident reconstruction of which checkpoint and runtime produced
a contested output, satisfying Art.12(1)(a) documentation requirements.

---

## License requirements

| Endpoint | Required feature |
|---|---|
| `GET /api/v1/compliance/ai-act/technical-doc` | `ai_act_compliance` OR `inference_audit` |
| `GET /api/v1/compliance/gdpr/record-of-processing` | `inference_audit` |
| `GET /api/v1/inference-audit` | `inference_audit` |
| `GET /api/v1/inference-audit/verify` | `inference_audit` |

See [Licensing & Key Management](licensing.md) for how to obtain and activate an
enterprise license.

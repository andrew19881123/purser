# Purser — Stato del progetto

> **Purser** — "il Kubernetes dei layer LLM": orchestratore zero-config per inferenza LLM
> distribuita su LAN. Prende una flotta di macchine eterogenee, spezza un modello **per layer**
> (pipeline parallelism) su più nodi, e lo espone dietro un unico endpoint OpenAI-compatibile.
> Usa motori esistenti (llama.cpp, DwarfStar) via un Engine Adapter — non riscrive l'inferenza.

Data snapshot: 2026-09-04.

## Architettura & linguaggi (ADR-1)

| Componente | Linguaggio | Cartella | Ruolo |
|---|---|---|---|
| Contratti gRPC | `.proto` → Go+Rust | `proto/` | Sorgente di verità condivisa (keystone) |
| Agent | Rust | `rust/crates/agent` | Demone su ogni nodo: probe HW, benchmark rete, supervisor engine, model cache, mDNS, self-healing, **mock inference server** |
| Engine Adapter | Rust | `rust/crates/engine-adapter` (+ `adapter-llamacpp`) | Trait `EngineBackend`: mock / llama.cpp |
| Planner | Go | `go/planner/plan` | **Il fossato**: algoritmo DP A→F di split ottimale |
| Control Plane | Go | `go/controlplane` | Registry SQLite, orchestrator, reconciler, PKI, RegistrationService, API `/api/v1` |
| API Gateway | Rust | `rust/crates/gateway` | Ingresso OpenAI `/v1`, SSE, auth, quota, route-sync |
| UI Dashboard | TS/React | `ui/` | SPA fleet/catalogo/deploy/playground |

Toolchain **project-local** in `.toolchain/` (Rust 1.98, Go 1.27, buf) — nulla di globale.

## Stato: MVP verticale COMPLETO e dimostrato E2E

Tutti i componenti implementati e verdi (~300 test complessivi tra i moduli):
proto+codegen · engine-adapter+mock (conformance suite) · planner **DP==brute-force + property-based (2000 casi)** ·
control-plane (registry/orchestrator/reconciler/pki/fleet, 56 test) · gateway (routing/auth/quota/route-sync, 16 test) ·
agent (supervisor/linkbench/discovery/modelcache/healing, 47 test) · UI (build+typecheck) ·
wiring Planner↔CP · endpoint join-token/create-model · mock host OpenAI SSE.

### E2E dimostrato (single-node, binari reali, mock engine)
`bash tools/e2e_full.sh` esegue: avvio CP+Gateway → mint join-token → **enrollment agent via gRPC** →
node READY con HW profile reale → seed modello → **fit del Planner** → deploy → **piano DP** →
orchestrator `StartEngine(host)` → mock engine READY → **deployment ACTIVE** → **route-sync CP→Gateway** →
`GET /v1/models` mostra il modello → **chat reale** (non-stream + SSE token-by-token fino a `[DONE]`).

## Come si costruisce ed esegue

```bash
source ./env.sh          # toolchain project-local nel PATH
make setup               # installa toolchain (idempotente)
make gen                 # rigenera i tipi dai .proto (buf + tonic)
make build && make test  # build + test di tutti i workspace
bash tools/e2e_full.sh   # dimostrazione end-to-end (enroll→deploy→chat)
```
Nota disco: profilo Rust "lean" (`[profile.dev] debug=0`) per contenere `target/`. Binari in `rust/target/debug/` e `bin/`.

## Backlog / follow-up (onesto, prioritizzato)

**Correttezza / calibrazione**
- **Calibrazione Planner**: i coefficienti `computeTime`/`commTime`/perf sono placeholder → le stime tok/s escono ~0. Servono benchmark reali + emulazione `tc netem`. Costanti in `plan.go`/`partition.go` (`W1..W5`, `HEADROOM`, `expectedAcceptedTokens`, ecc.).
- **Vincoli operatore**: `applyPinnedRanges` è best-effort e non ri-verifica la memoria dopo lo spostamento; `ForceNodeCount=1` bypassa il fit single-node → aggiungere validazione post-vincolo + `PlanError`.

**Integrazione multi-nodo**
- **Deploy pipeline multi-nodo**: oggi provato single-node. Per più agent (soprattutto sullo stesso host) serve che ogni agent registri il proprio *advertised address* nel `RegistrationService` (campo nel proto) e che il `Resolver` dell'orchestrator lo usi invece della convenzione `host:porta`.

**Backend reale**
- **llama.cpp live**: adapter (flag builder, GGUF reader, metrics parser) implementato e unit-testato; il test di conformità live è opt-in (`PURSER_LLAMACPP_BIN`). Manca la validazione con binari llama.cpp reali + hardware GPU.
- **DwarfStar adapter**, speculative tuning, tensor-parallelism opportunistico (vLLM/SGLang).

**Contratti UI↔backend da congelare** (assunti dalla UI, alcuni non ancora esposti dal CP)
- `POST /nodes/{id}/drain|restart`, `DELETE /nodes/{id}`, `POST /models/{id}/plan` (preview), `GET/DELETE /apikeys`, wiring del join-token nella UI; schema del frame SSE `/api/v1/metrics`; casing JSON (protojson camelCase).

**Enterprise / hardening (tier premium, per lo più)**
- Gossip SWIM (foca) — oggi discovery = mDNS+seed+heartbeat.
- HA control-plane (Raft) + replica Registry; RBAC/SSO/LDAP; audit; isolamento forte multi-tenant.
- Failover *execution* (il reconciler rileva e pianifica; l'esecuzione del ribilanciamento va completata).
- Packaging servizi di sistema (systemd/launchd/MSI), bundle air-gap firmati, pipeline CI, self-update firmato.

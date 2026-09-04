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

### E2E multi-nodo dimostrato (split su 2 nodi, binari reali, mock engine)
`bash tools/e2e_multinode.sh` prova il caso pipeline-parallel: enrolla **due agent** (sullo stesso host, ognuno con il
proprio *advertised address* AgentService + inference, così il Resolver li distingue), sceglie una taglia di modello
**troppo grande per un nodo ma che entra su due**, la deploya e verifica: (1) due nodi READY con advertised addr distinti;
(2) piano con **2 assignment (HOST+WORKER)** e `pipeline_order` di lunghezza 2; (3) deployment **ACTIVE**; (4) l'orchestrator
avvia il **WORKER prima dell'HOST** su advertised addr distinti (:50151 vs :50161); (5) **chat reale** (non-stream + SSE)
proxata all'advertised inference addr dell'HOST; (6) stime di performance del piano **non nulle** (path di calibrazione
esercitato).

## Come si costruisce ed esegue

```bash
source ./env.sh          # toolchain project-local nel PATH
make setup               # installa toolchain (idempotente)
make gen                 # rigenera i tipi dai .proto (buf + tonic)
make build && make test  # build + test di tutti i workspace
bash tools/e2e_full.sh   # dimostrazione end-to-end single-node (enroll→deploy→chat)
bash tools/e2e_multinode.sh  # dimostrazione end-to-end multi-nodo (split su 2 nodi, worker→host, chat)
```
Nota disco: profilo Rust "lean" (`[profile.dev] debug=0`) per contenere `target/`. Binari in `rust/target/debug/` e `bin/`.

## Chiuso in questa milestone (era backlog → ora fatto/dimostrato)

- **Deploy pipeline multi-nodo**: ogni agent registra i propri *advertised address* (AgentService + inference) nel `JoinRequest` (`advertised_agent_addr` / `advertised_inference_addr`); il `RegistrationService` li persiste sul Node e il `Resolver` dell'orchestrator li usa (fallback per-faccia alla convenzione `hostname:porta`). Split su 2 nodi (worker→host) con chat reale — dimostrato da `tools/e2e_multinode.sh`.
- **Calibrazione stime Planner**: modello di decode **memory-bandwidth-bound** (byte di pesi attivi letti per token / banda), con `memBandwidthUtilFraction` (MBU), speed-up speculativo effettivo e rapporto prefill/decode. Le stime tok/s decode/prefill ora escono **non nulle e plausibili** (le costanti restano `CALIBRATABLE` con benchmark reali + `tc netem`).
- **Validazione vincoli operatore**: `validatePlanMemory` ri-verifica il piano assemblato contro la memoria utile di ogni nodo dopo l'applicazione dei vincoli. Un pin che fa sforare un nodo, o `ForceNodeCount=1` su un modello che non entra sul nodo top, ora producono un `PlanError` motivato invece di un piano con headroom negativo.

## Backlog / follow-up (onesto, prioritizzato)

**Backend reale**
- **llama.cpp live**: adapter (flag builder, GGUF reader, metrics parser) implementato e unit-testato; il test di conformità live è opt-in (`PURSER_LLAMACPP_BIN`). Manca la validazione con binari llama.cpp reali + hardware GPU.
- **DwarfStar adapter**, speculative tuning, tensor-parallelism opportunistico (vLLM/SGLang).

**API / contratti UI↔backend da congelare** (assunti dalla UI, alcuni non ancora esposti dal CP)
- Manca l'endpoint **`DELETE /api/v1/models/{id}`** (oggi non si può rimuovere un modello dal catalogo — vedi il probe throwaway in `e2e_multinode.sh`).
- `POST /nodes/{id}/drain|restart`, `DELETE /nodes/{id}`, `POST /models/{id}/plan` (preview), `GET/DELETE /apikeys`, wiring del join-token nella UI; schema del frame SSE `/api/v1/metrics`; casing JSON (protojson camelCase).

**Enterprise / hardening (tier premium, per lo più)**
- Gossip SWIM (foca) — oggi discovery = mDNS+seed+heartbeat.
- HA control-plane (Raft) + replica Registry; RBAC/SSO/LDAP; audit; isolamento forte multi-tenant.
- Failover *execution* (il reconciler rileva e pianifica; l'esecuzione del ribilanciamento va completata).
- Packaging servizi di sistema (systemd/launchd/MSI), bundle air-gap firmati, self-update firmato.

**CI / qualità**
- **CI da portare a verde**, inclusi i **4 warning clippy preesistenti** in `purser-agent`.

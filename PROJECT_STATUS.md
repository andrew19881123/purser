# Purser — Stato del progetto

> **Purser** — "il Kubernetes dei layer LLM": orchestratore zero-config per inferenza LLM
> distribuita su LAN. Prende una flotta di macchine eterogenee, spezza un modello **per layer**
> (pipeline parallelism) su più nodi, e lo espone dietro un unico endpoint OpenAI-compatibile.
> Usa motori esistenti (llama.cpp, DwarfStar) via un Engine Adapter — non riscrive l'inferenza.

Data snapshot: 2026-09-06. **Questo documento descrive lo scope del rilascio `v0.1.0`** (prima
release Community) e il lavoro atterrato successivamente fino a `v0.3` (candidate su `release/v0.3`). Cronologia completa in [CHANGELOG.md](CHANGELOG.md).

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

## Stato v0.1.0: MVP verticale COMPLETO e dimostrato E2E

Tutti i componenti implementati e verdi (~300 test complessivi tra i moduli):
proto+codegen · engine-adapter+mock (conformance suite) · planner **DP==brute-force + property-based (2000 casi)** ·
control-plane (registry/orchestrator/reconciler/pki/fleet, 56 test) · gateway (routing/auth/quota/route-sync, 16 test) ·
agent (supervisor/linkbench/discovery/modelcache/healing, 47 test) · UI (build+typecheck) ·
wiring Planner↔CP · endpoint join-token/create-model · mock host OpenAI SSE.

**CI verde** (`.github/workflows/ci.yml`): tutti e quattro i job passano — `proto` (buf lint),
`rust` (build + test + **clippy `-D warnings`** + `cargo fmt --check`), `go` (build/vet/test su
`planner` e `controlplane` + **`gofmt --check`** su `go/` e `enterprise/license`), `ui` (typecheck + build).
I 4 warning clippy preesistenti in `purser-agent` sono stati risolti.

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

## Modello di licenza — open-core (stile LiteLLM)

Purser adotta il modello open-core reso popolare da LiteLLM: **core MIT** + `enterprise/`
**source-available key-gated**.

- **Core MIT** — tutto ciò che sta **fuori** da `enterprise/` è MIT (vedi [LICENSE](LICENSE)):
  l'intero stack single-cluster (Agent, Engine Adapter, Planner, Control Plane, Gateway, UI).
- **`enterprise/` source-available** — codice **pubblico** (compilabile, ispezionabile, usabile per
  sviluppo/valutazione) sotto Purser Enterprise License; **l'uso in produzione richiede una licenza
  commerciale**. Vedi [LICENSING.md](LICENSING.md).
- **Key-gate** — attivazione a runtime via `PURSER_LICENSE_KEY`; verifica **ed25519 completamente
  offline** (nessun phone-home, air-gap friendly). Senza entitlement valido le rotte enterprise del
  control-plane rispondono **`402 Payment Required`** (`writeLicenseRequired` in
  `go/controlplane/server/server.go`). La chiave di trust spedita è di **sviluppo** (chiave privata
  scartata): le feature enterprise restano disattive finché un maintainer non provvede il proprio
  trust root con `purser-license keygen`.

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

## Chiuso in v0.1.0 (era backlog → ora fatto/dimostrato)

- **Deploy pipeline multi-nodo**: ogni agent registra i propri *advertised address* (AgentService + inference) nel `JoinRequest` (`advertised_agent_addr` / `advertised_inference_addr`); il `RegistrationService` li persiste sul Node e il `Resolver` dell'orchestrator li usa (fallback per-faccia alla convenzione `hostname:porta`). Split su 2 nodi (worker→host) con chat reale — dimostrato da `tools/e2e_multinode.sh`.
- **Calibrazione stime Planner**: modello di decode **memory-bandwidth-bound** (byte di pesi attivi letti per token / banda), con `memBandwidthUtilFraction` (MBU), speed-up speculativo effettivo e rapporto prefill/decode. Le stime tok/s decode/prefill ora escono **non nulle e plausibili** (le costanti restano `CALIBRATABLE` con benchmark reali + `tc netem`).
- **Validazione vincoli operatore**: `validatePlanMemory` ri-verifica il piano assemblato contro la memoria utile di ogni nodo dopo l'applicazione dei vincoli. Un pin che fa sforare un nodo, o `ForceNodeCount=1` su un modello che non entra sul nodo top, ora producono un `PlanError` motivato invece di un piano con headroom negativo.
- **Artefatti prebuilt pubblicati (Release + GHCR)** — installare non richiede più di compilare. La **Release `v0.1.0`** allega gli artefatti installabili: pacchetti nativi dell'Agent **`.deb`** (`purser-agent_0.1.0_amd64.deb`) e **`.rpm`** (`purser-agent-0.1.0-1.x86_64.rpm`), i **tarball** dei tre componenti (`purser-{agent,control-plane,gateway}-0.1.0-linux-amd64.tar.gz`) e `SHA256SUMS`. I package nativi (servizio host **non** K8s — accedono a GPU/device, engine RPC non sandboxato) sono prodotti da `make package-agent` con **nfpm**; si installano con `sudo apt install ./…deb` / `yum install ./…rpm` (unit **systemd** + `/etc/purser/agent.env`).
- **Immagini container su GHCR + Helm chart OCI (default GHCR)** — control-plane / gateway / UI pubblicati come `ghcr.io/andrew19881123/purser-{control-plane,gateway,ui}:0.1.0` (+ `:latest`), **pubblici** (pull anonimo verificato) → l'install di default **non richiede pull secret**. Il chart è **pubblicato come artefatto OCI** su GHCR (`oci://ghcr.io/andrew19881123/charts/purser`, `:0.1.0`), quindi l'install è **un solo comando senza clone**: `helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.0 --set controlPlane.service.type=LoadBalancer` (LoadBalancer per farsi raggiungere dagli Agent LAN). Il `deploy/helm/purser/values.yaml` ha i **default puntati su GHCR**, così Helm scarica sia il chart sia le immagini prebuilt **senza build**; resta disponibile l'install *from source* (`helm install purser deploy/helm/purser` dal repo clonato) per personalizzare il chart. Chart validato con `helm lint`/`helm template`. Nota: l'HA multi-replica su K8s resta dietro il **Registry replicato Raft** (enterprise) — il SQLite embedded regge un pod singolo.

## Chiuso dopo v0.1.0 (2026-09-05)

Lavoro atterrato post-release, non ancora etichettato come `v0.1.1`:

- **Audit log tamper-evident** (`go/controlplane/audit/`): engine hash-chain con `seq` / `prev_hash` / `hash` persistiti nel registry SQLite (migrazione idempotente). `GET /api/v1/enterprise/audit-log` restituisce entry reali + risultato di verifica della chain (`{verified, length, break?}`). Feature enterprise, attiva con `PURSER_LICENSE_KEY` + feature `"audit"`.
- **`DELETE /api/v1/models/{id}`**: rimozione dal catalogo; 404 modello sconosciuto, 409 modello in uso da deployment non terminale, 204 successo.
- **`POST /api/v1/models/{id}/plan`** (preview dry-run): Planner read-only, nessun effetto su persist/deploy/audit; risponde `{feasible:true, ...piano}` o `{feasible:false, reason}`.
- **`POST /api/v1/nodes/{id}/drain`**: cordon del nodo (marks unschedulable); non effettua live-migration dei deployment esistenti.
- **`DELETE /api/v1/nodes/{id}`**: decommissioning soft (state → `DECOMMISSIONED` + revoca certificato); 409 se il nodo ospita un deployment non terminale.
- **`POST /api/v1/nodes/{id}/restart` non implementato** — omesso intenzionalmente: richiede una decisione di design (resta in backlog).
- **Fix gateway TLS** (fix di correttezza/sicurezza): rimosso il `TlsConfig` e il log `tls-configured: true` fuorvianti. Il gateway serve HTTP in chiaro e delega la terminazione TLS all'ingress/LB; il codice precedente implicava il contrario.
- **`GET /api/v1/nodes` rimosso dal gateway**: lo stub era sempre vuoto; i dati dei nodi vivono nel control-plane.
- **UI default reale**: la dashboard punta all'API reale di default; la modalità mock è opt-in (`PURSER_UI_MOCK=1`). URL configurabile a runtime via `PURSER_API_BASE_URL` (default `/api/v1`, same-origin) tramite `env.js` (servito con `Cache-Control: no-store`, caricato prima del bundle); il chart Helm cabla il valore al container start. **Nota**: l'immagine GHCR `purser-ui:0.1.0` eroga ancora la build mock-by-default fino alla ricostruzione e ripubblicazione dell'immagine.
- **Gate `gofmt`** nel job Go di CI (copre `go/` e `enterprise/license`).

## Chiuso in v0.3 (release/v0.3, 2026-09-06)

Tutto il lavoro della `release/v0.3` candidate — ~37 feature distribuite in 4 wave:

- **OIDC SSO completo**: Authorization Code Flow + PKCE, session cookie HttpOnly, browser SSO end-to-end funzionante; `PURSER_OIDC_CLIENT_SECRET` ora letto correttamente.
- **RBAC OIDC**: mapping claim groups/roles → ruoli Purser via `PURSER_OIDC_GROUP_MAPPINGS`; tenant-scoped list deployments.
- **TLS management API** (auto via PKI interna, `PURSER_TLS_AUTO`) + **rate limiting** per-IP e per-key (100/50 RPS default).
- **Webhook notification** per approval-required del reconciler (`PURSER_RECONCILER_WEBHOOK_URL`).
- **Fleet Capacity Headroom API** (`GET /api/v1/fleet/capacity`).
- **Osservabilità completa**: NodeMetrics→OTEL bridge (5 gauge per nodo), reconciler OTEL metrics (counter/gauge/histogram), trace sampler configurabile (`OTEL_TRACES_SAMPLER`), reconciler status endpoint.
- **llama.cpp registrato in BackendRegistry** (feature-gated `--features llamacpp`); ModelCache cablato nel path StartEngine; HttpFetcher attivato di default.
- **`prefix_caching_factor` e `kv_ssd_offload`** in HardwareProfile proto; planner usa le capability reali; flash attention come campo esplicito in EngineParams.
- **ARM64 build matrix**: linux/arm64 + darwin/arm64 (Apple Silicon M2/M3 come nodi di inferenza).
- **Demo mode** (`docker-compose.yml`, `docker compose up`, no GPU) + `devcontainer.json` + `make dev` + good-first-issue guide.
- **SDK**: Python `AsyncPurserClient` + `stream_metrics()` SSE generator; TypeScript `@purser/sdk 0.3.0` zero-dependency.
- **Planner benchmark suite** con dati baseline reali; Ansible role `purser_agent` per fleet enrollment.
- **Release pipeline**: ARM64 artifacts, SLSA L2 provenance, cosign SBOM attestazioni, chart signing; apt/yum hosted via Cloudsmith.
- **Production license trust root** (chiave reale, modello open-core commercialmente funzionale).
- **Fix e2e scripts**: ROOT auto-detection via `${BASH_SOURCE[0]}` (postmortem `docs/postmortems/e2e_hardcoded_path.md`).
- **Failover execution** completato e testato.
- Documentazione GitHub Pages aggiornata: webhook notifications, Ansible integration.

## Backlog / follow-up (post-v0.3, onesto, prioritizzato)

**Distribuzione degli artefatti (residui)**
- **`.pkg` macOS notarizzato** (P6) e **Homebrew tap** (P7) — richiedono ARM64 (spedito) + firma Apple notarization.
- **`.msi` Windows** — deferred v0.4.

**Backend reale**
- **llama.cpp validazione GPU**: adapter registrato (v0.3); manca la validazione con binari llama.cpp reali + hardware GPU (benchmark + calibrazione costanti Planner).
- **DwarfStar adapter**, speculative tuning, tensor-parallelism opportunistico (vLLM/SGLang).

**API / contratti UI↔backend da congelare**
- `GET/DELETE /apikeys`, wiring del join-token nella UI; schema del frame SSE `/api/v1/metrics`; casing JSON (protojson camelCase).
- **`POST /api/v1/nodes/{id}/restart` non implementato** — richiede decisione di design prima di procedere.

**Enterprise / hardening (dietro il key-gate)**
> Il gate di licenza è **in piedi e testato**; OIDC+RBAC sono atterrati in v0.3.
> Restano aperte le capability enterprise più pesanti.
- **HA control-plane (Raft)** + replica Registry; Gateway HA dietro VIP.
- **LDAP/AD** — identity provider alternativo a OIDC.
- Isolamento forte multi-tenant e chargeback/usage accounting.
- Bundle air-gap firmati, self-update firmato, Agent secrets TPM-backed.

**Community / discovery**
- Gossip SWIM (foca) — oggi discovery = mDNS+seed+heartbeat.

**CI / qualità**
- **Pin della toolchain in CI** — oggi i job usano actions standard invece delle versioni project-local di `.toolchain/`.

## Roadmap post-v0.3

Priorità dopo il rilascio v0.3:

1. **Validazione hardware** — chiudere il path di inferenza reale: conformità **llama.cpp** live su
   binari e GPU reali, adapter **DwarfStar**, e ri-calibrazione delle costanti del Planner con
   benchmark reali (`tc netem` per la rete). Il benchmark suite è già presente (v0.3).
2. **Distribuzione macOS** — `.pkg` notarizzato (P6) + Homebrew tap (P7), ora che ARM64 è spedito.
3. **Enterprise avanzata** — **HA/Raft** (registry replicato, multi-pod K8s), **LDAP/AD** come
   provider identity alternativo, isolamento multi-tenant pieno.
4. **API e UI refinement** — congelare i contratti UI↔backend mancanti, decidere `POST /nodes/{id}/restart`.

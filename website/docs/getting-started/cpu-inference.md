# CPU-only Inference (No GPU Required)

Purser supports real LLM inference on **CPU-only machines** — no GPU, no CUDA,
no special drivers. The `cpu` engine backend uses llama.cpp's OpenBLAS-accelerated
CPU path, which can serve smaller quantised models at 2–8 tokens/second on modern
hardware.

This is useful for:

- Developer laptops and cloud VMs without a GPU
- Distributing a model across multiple cheap CPU machines (pipeline parallelism)
- Running models up to ~7B parameters on 16 GB RAM

---

## How it works

The `cpu` backend is an alias for the `llamacpp` backend with one key difference:
it forces `-ngl 0` (zero GPU layers), meaning **all transformer layers run on the
CPU**. llama.cpp automatically uses BLAS (OpenBLAS / Accelerate on macOS) for
matrix multiplications, which gives a significant speedup over naive scalar math.

| Setting | `llamacpp` (GPU) | `cpu` (no GPU) |
|---|---|---|
| GPU offload | All layers (`-ngl 99`) | None (`-ngl 0`) |
| Thread count | Engine default | `PURSER_CPU_THREADS` (default: `num_cpus/2`) |
| Context window | Caller-supplied | `PURSER_CPU_CONTEXT_SIZE` (default: 2048) |
| Binary | Same (`llama-server`, `rpc-server`) | Same |

---

## Recommended models for CPU inference

These models fit comfortably in RAM on most developer machines and run at
practical speeds on CPU:

| Model | Params | Q4_K_M size | RAM needed | ~tok/s (4 cores) |
|---|---|---|---|---|
| TinyLlama 1.1B | 1.1B | 0.7 GB | 2 GB | 4–12 |
| Llama3 1B | 1B | 0.7 GB | 2 GB | 4–10 |
| Phi-3 Mini | 3.8B | 2.3 GB | 4 GB | 2–6 |
| Gemma 2B | 2.6B | 1.6 GB | 3 GB | 2–5 |

!!! warning "Performance figures are estimates only"
    Throughput depends heavily on BLAS library, compiler flags, and CPU
    microarchitecture. These figures are rough guides from community reports, not
    calibrated benchmarks. Your actual throughput may be higher or lower.

---

## Quick start

### 1. Download binaries and model

The setup script downloads the latest pre-built llama.cpp release and TinyLlama
1.1B Q4_K_M in one command:

```bash
./tools/setup-cpu-inference.sh
```

This installs everything to `~/.purser/bin/` and `~/.purser/models/`. The script
is idempotent — re-running it skips downloads that already exist.

To use a different model, set `PURSER_MODEL_URL` before running:

```bash
PURSER_MODEL_URL=https://huggingface.co/TheBloke/phi-3-mini-4k-instruct-GGUF/resolve/main/phi-3-mini-4k-instruct.Q4_K_M.gguf \
  ./tools/setup-cpu-inference.sh
```

### 2. Run the agent

```bash
export PURSER_LLAMACPP_BIN=~/.purser/bin
export PURSER_ENGINE_BACKEND=cpu
export PURSER_CPU_THREADS=4            # optional: default = num_cpus/2
export PURSER_CPU_CONTEXT_SIZE=2048    # optional: context window (tokens)
export PURSER_CONTROL_PLANE_ADDR=http://localhost:9443
export PURSER_JOIN_TOKEN=<token>

./purser-agent
```

### 3. Register the model

```bash
curl -X POST http://localhost:8080/api/v1/models/import/cpu \
  -H 'Content-Type: application/json' \
  -d '{
    "model_id":    "tinyllama-1.1b",
    "gguf_path":   "/home/user/.purser/models/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf",
    "context_max": 2048
  }'
```

The endpoint auto-fills the model spec (layer count, hidden dim, parameter count)
from a built-in lookup table of known models. Supported model IDs:

| `model_id` | Description |
|---|---|
| `tinyllama-1.1b` | TinyLlama 1.1B Chat |
| `llama3-1b` | Llama 3 1B |
| `llama3-8b` | Llama 3 8B |
| `phi3-mini` | Phi-3 Mini 4k |
| `gemma2-2b` | Gemma 2 2B |

### 4. Deploy and run inference

```bash
# Create a deployment plan and deploy.
curl -X POST http://localhost:8080/api/v1/models/tinyllama-1.1b/deploy \
  -H 'Authorization: Bearer <admin-key>' \
  -H 'Content-Type: application/json' \
  -d '{"quantization": "Q4_K_M"}'

# Run a chat completion through the OpenAI-compatible gateway.
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer <inference-key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "tinyllama-1.1b",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PURSER_ENGINE_BACKEND` | `mock` | Set to `cpu` to enable CPU inference |
| `PURSER_CPU_THREADS` | `num_cpus/2` | llama.cpp thread count (`-t N`) |
| `PURSER_CPU_CONTEXT_SIZE` | `2048` | Default context window in tokens (`-c N`) |
| `PURSER_LLAMACPP_BIN` | (PATH) | Directory containing `llama-server` and `rpc-server` |

---

## CPU vs GPU inference tradeoffs

| | CPU | GPU |
|---|---|---|
| Hardware cost | Low — any modern laptop or server | High — GPU required |
| Models size | Up to ~7B practical (more with enough RAM) | Up to fleet's combined VRAM |
| Throughput | 2–12 tok/s (small models, 4+ cores) | 30–150+ tok/s |
| Latency | Higher (100–500 ms TTFT) | Lower (10–50 ms TTFT) |
| Multi-node | Yes — pipeline-parallel across CPUs | Yes — RPC across GPUs |
| Power draw | Low | High |
| Setup complexity | Low — no drivers, no CUDA | Medium — GPU drivers, CUDA/ROCm |

---

## Multi-node CPU inference

CPU machines can participate in pipeline-parallel inference just like GPU machines.
The Planner treats them as nodes with "VRAM" equal to their available RAM minus a
headroom reserve, and splits model layers accordingly.

### Example: 2 CPU machines, 7B model

```
Machine A (16 GB RAM):  layers 0–15   (Llama3 7B, 8 GB weights)
Machine B (16 GB RAM):  layers 16–31  (Llama3 7B, 8 GB weights)
```

**On both machines**:

```bash
./tools/setup-cpu-inference.sh

export PURSER_LLAMACPP_BIN=~/.purser/bin
export PURSER_ENGINE_BACKEND=cpu
export PURSER_CPU_THREADS=8
export PURSER_CONTROL_PLANE_ADDR=http://192.168.1.10:9443
export PURSER_JOIN_TOKEN=<token>
./purser-agent
```

**Register and deploy** (same as single-node, Planner handles the split):

```bash
curl -X POST http://192.168.1.10:8080/api/v1/models/import/cpu \
  -d '{"model_id":"llama3-8b","gguf_path":"/home/user/.purser/models/llama3-8b.gguf","context_max":2048}'

curl -X POST http://192.168.1.10:8080/api/v1/models/llama3-8b/deploy \
  -H 'Authorization: Bearer <admin-key>' \
  -d '{"quantization":"Q4_K_M"}'
```

The Planner will automatically split the 32 layers across both machines, sending
only activations (~KB per token) over the LAN between stages.

!!! tip "Bandwidth matters less for CPU pipelines"
    CPU throughput is the bottleneck, not the LAN. Even a 1 Gbps link is
    sufficient for most CPU-only pipeline configurations since the per-token
    activation data is small relative to CPU compute time.

---

## See also

- [Multi-Node Inference](multi-node-inference.md) — GPU and CPU multi-node guide
- [Architecture — Engine Backends](architecture.md#engine-backends)
- [Environment Variables Reference](../configuration/env-vars.md)

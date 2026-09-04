// ---------------------------------------------------------------------------
// A tiny, deterministic *mock* of the Purser planner. It is intentionally a
// rough heuristic — just enough to drive believable fit badges, deployment
// plans and honest performance ranges in the skeleton. The real planner
// (go/planner) will replace this behind the same `/api/v1` responses.
// ---------------------------------------------------------------------------
import type {
  CatalogEntry,
  ClusterCapacity,
  DeployOverrides,
  Deployment,
  DeploymentPlan,
  FitVerdict,
  ModelSpec,
  NodeView,
  PerfEstimate,
  Quantization,
  Role,
} from '../api/types';
import { clamp } from '../lib/format';

/** A node counts toward usable capacity when it can actually take load. */
function isUsable(n: NodeView): boolean {
  return n.profile.state === 'ready' || n.profile.state === 'running';
}

/** Usable memory a node can dedicate to weights (VRAM if present, else RAM). */
function nodeUsableGb(n: NodeView): number {
  const vram = n.profile.gpus.reduce((s, g) => s + g.vramGb * g.count, 0);
  // Unified-memory machines (Apple, DGX) share one pool; avoid double counting.
  const unified = n.profile.gpus.some((g) => g.unified);
  const base = unified ? Math.max(vram, n.profile.ramTotalGb) : vram || n.profile.ramTotalGb;
  // Reserve ~15% headroom for KV-cache/activations/OS.
  return base * 0.85;
}

export function computeCapacity(nodes: NodeView[]): ClusterCapacity {
  const usable = nodes.filter(isUsable);
  const vramTotal = nodes.reduce(
    (s, n) => s + n.profile.gpus.reduce((g, x) => g + x.vramGb * x.count, 0),
    0,
  );
  const vramAvail = usable.reduce(
    (s, n) => s + n.profile.gpus.reduce((g, x) => g + x.vramGb * x.count, 0),
    0,
  );
  const backends = Array.from(new Set(nodes.flatMap((n) => n.profile.backends)));
  return {
    nodeCount: nodes.length,
    readyNodeCount: usable.length,
    ramTotalGb: nodes.reduce((s, n) => s + n.profile.ramTotalGb, 0),
    ramAvailableGb: usable.reduce((s, n) => s + n.profile.ramAvailableGb, 0),
    vramTotalGb: vramTotal,
    vramAvailableGb: vramAvail,
    gpuCount: nodes.reduce((s, n) => s + n.profile.gpus.reduce((g, x) => g + x.count, 0), 0),
    backends,
    fp4Capable: nodes.some((n) => n.profile.gpus.some((g) => g.fp4Native)),
    aggregateDecodeTokS: nodes.reduce((s, n) => s + (n.metrics?.decodeTokS ?? 0), 0),
  };
}

/** Pick the best quantization that fits, respecting the quality/speed bias. */
function pickQuant(
  model: ModelSpec,
  budgetGb: number,
  fp4Capable: boolean,
  preference: DeployOverrides['preference'],
): Quantization | null {
  const feasible = model.quantizations.filter(
    (q) => (!q.requiresFp4 || fp4Capable) && q.sizeGb <= budgetGb,
  );
  if (feasible.length === 0) return null;
  // quality-first -> highest quality; speed-first -> smallest footprint.
  const sorted = [...feasible].sort((a, b) =>
    preference === 'speed' ? a.sizeGb - b.sizeGb : b.quality - a.quality,
  );
  return sorted[0];
}

/** Smallest quantization regardless of budget, used to compute a deficit. */
function smallestQuant(model: ModelSpec): Quantization {
  return [...model.quantizations].sort((a, b) => a.sizeGb - b.sizeGb)[0];
}

export function computeFit(
  model: ModelSpec,
  nodes: NodeView[],
  overrides: DeployOverrides = { forceNodeCount: null, preference: 'balanced' },
): FitVerdict {
  const usable = nodes.filter(isUsable);
  if (usable.length === 0) {
    return {
      fits: false,
      quantization: null,
      nodesNeeded: 0,
      estimated: null,
      deficitGb: smallestQuant(model).sizeGb,
      reasonKey: 'no_ready_nodes',
    };
  }

  const cap = computeCapacity(nodes);
  const budget = usable.reduce((s, n) => s + nodeUsableGb(n), 0);
  const quant = pickQuant(model, budget, cap.fp4Capable, overrides.preference);

  if (!quant) {
    const smallest = smallestQuant(model);
    // FP4-only model on non-FP4 fleet is a distinct, actionable failure.
    if (smallest.requiresFp4 && !cap.fp4Capable) {
      return {
        fits: false,
        quantization: null,
        nodesNeeded: usable.length,
        estimated: null,
        deficitGb: 0,
        reasonKey: 'needs_fp4',
      };
    }
    return {
      fits: false,
      quantization: null,
      nodesNeeded: usable.length,
      estimated: null,
      deficitGb: Math.max(0, Math.round(smallest.sizeGb - budget)),
      reasonKey: 'not_enough_memory',
    };
  }

  // Nodes needed: pack the chosen quant across the largest nodes first.
  const sortedByCap = [...usable].sort((a, b) => nodeUsableGb(b) - nodeUsableGb(a));
  let acc = 0;
  let nodesNeeded = 0;
  for (const n of sortedByCap) {
    if (acc >= quant.sizeGb) break;
    acc += nodeUsableGb(n);
    nodesNeeded += 1;
  }
  nodesNeeded = clamp(overrides.forceNodeCount ?? nodesNeeded, 1, usable.length);

  const estimated = estimatePerf(model, quant, nodesNeeded, budget);
  const tight = estimated.headroomGb < 6;
  return {
    fits: true,
    quantization: quant.name,
    nodesNeeded,
    estimated,
    deficitGb: 0,
    reasonKey: tight ? 'fits_tight' : 'fits',
  };
}

/** Honest performance envelope: a range, widened by pipeline depth + link cost. */
function estimatePerf(
  model: ModelSpec,
  quant: Quantization,
  nodes: number,
  budgetGb: number,
): PerfEstimate {
  // Decode is memory-bound; MoE has far fewer active params.
  const activeGb = quant.sizeGb * (model.paramsActiveB / model.paramsTotalB);
  const single = clamp(900 / Math.max(4, activeGb), 6, 90); // tok/s on one strong node
  // Pipeline parallelism across N nodes: sub-linear, with per-hop latency tax.
  const pipeFactor = 1 / (1 + 0.18 * (nodes - 1));
  const base = single * pipeFactor;
  const decodeMin = base * 0.8;
  const decodeMax = base * 1.15;
  const prefillMin = base * 9;
  const prefillMax = base * 15;
  return {
    decodeTokSMin: decodeMin,
    decodeTokSMax: decodeMax,
    prefillTokSMin: prefillMin,
    prefillTokSMax: prefillMax,
    headroomGb: Math.max(0, Math.round(budgetGb - quant.sizeGb)),
  };
}

export function buildCatalog(
  models: ModelSpec[],
  nodes: NodeView[],
): CatalogEntry[] {
  return models.map((model) => ({ model, fit: computeFit(model, nodes) }));
}

/** Produce a full DeploymentPlan (assignments, pipeline order, explanation). */
export function buildPlan(
  model: ModelSpec,
  nodes: NodeView[],
  overrides: DeployOverrides,
): DeploymentPlan {
  const fit = computeFit(model, nodes, overrides);
  const usable = nodes.filter(isUsable).sort((a, b) => nodeUsableGb(b) - nodeUsableGb(a));
  const chosen = usable.slice(0, Math.max(1, fit.nodesNeeded));

  // The fastest-linked node hosts the pipeline.
  const host =
    chosen.find((n) => n.linkQuality === 'excellent') ?? chosen[0];
  const ordered = [host, ...chosen.filter((n) => n.profile.nodeId !== host.profile.nodeId)];

  // Distribute layers proportionally to each node's usable memory.
  const totalCap = ordered.reduce((s, n) => s + nodeUsableGb(n), 0);
  let cursor = 0;
  const assignments = ordered.map((n, i) => {
    const share = nodeUsableGb(n) / totalCap;
    const count =
      i === ordered.length - 1
        ? model.layers - cursor
        : Math.max(1, Math.round(model.layers * share));
    const start = cursor;
    const end = Math.min(model.layers, cursor + count);
    cursor = end;
    return {
      nodeId: n.profile.nodeId,
      role: (i === 0 ? 'host' : 'worker') as Role,
      layerStart: start,
      layerEnd: end,
      draft: model.draft.available && i === 0,
    };
  });

  const quantName = fit.quantization ?? smallestQuant(model).name;
  const quant = model.quantizations.find((q) => q.name === quantName)!;

  const explanation: string[] = [];
  explanation.push(
    `Selected ${chosen.length} node${chosen.length > 1 ? 's' : ''}: the ${quantName} weights (${quant.sizeGb} GB) don't fit on fewer.`,
  );
  if (quant.requiresFp4) {
    explanation.push(`Chose ${quantName} because the fleet has FP4-native accelerators.`);
  } else {
    explanation.push(
      `Chose ${quantName} (${overrides.preference}-biased): best quality that fits the memory budget.`,
    );
  }
  explanation.push(
    `${host.profile.hostname} is the pipeline host: it has the fastest inter-node link (${host.linkQuality}).`,
  );
  if (model.isMoe) {
    explanation.push(
      `MoE model: only ~${model.paramsActiveB}B of ${model.paramsTotalB}B params are active per token, so decode stays fast despite the split.`,
    );
  }
  if (model.draft.available) {
    explanation.push(
      `Speculative decoding enabled (${model.draft.type}) on the host to raise decode throughput.`,
    );
  }

  return {
    planId: `plan-${model.modelId}-${Date.now().toString(36)}`,
    modelId: model.modelId,
    quantization: quantName,
    assignments,
    pipelineOrder: ordered.map((n) => n.profile.nodeId),
    estimated:
      fit.estimated ?? estimatePerf(model, quant, chosen.length, totalCap),
    cost: Number((quant.sizeGb / 100 + chosen.length * 0.2).toFixed(2)),
    explanation,
  };
}

/** Build the initial (all-LOADING) deployment record from a plan. */
export function planToDeployment(plan: DeploymentPlan): Deployment {
  return {
    id: `dep-${plan.modelId}`,
    plan,
    state: 'provisioning',
    createdAt: new Date().toISOString(),
    nodeStatus: plan.assignments.map((a) => ({
      nodeId: a.nodeId,
      state: 'loading',
      progress: 0,
      detail: 'Downloading & sharding weights…',
    })),
  };
}

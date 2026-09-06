// React Query hooks wrapping the PurserApi client.
//
// Why React Query (over hand-rolled context): the whole app is read-mostly data
// from a remote control plane with loading/error/refetch/polling needs. React
// Query gives declarative loading & error states (which map straight onto our
// actionable ErrorState), background refetching for the live fleet/rollout,
// cache invalidation after operator actions, and — crucially for Phase 2 — it
// keeps every component decoupled from *how* data is fetched. Swapping the mock
// client for a real `fetch('/api/v1')` implementation touches zero components.
import { useEffect, useState } from 'react';
import {
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { api, type CreateApiKeyInput } from '../api/client';
import type { ChatClient } from '../api/openai';
import type { DeployOverrides, ImportSource, MetricsSnapshot } from '../api/types';

export const qk = {
  capacity: ['capacity'] as const,
  nodes: ['nodes'] as const,
  node: (id: string) => ['node', id] as const,
  catalog: ['catalog'] as const,
  model: (id: string) => ['model', id] as const,
  deployments: ['deployments'] as const,
  deployment: (id: string) => ['deployment', id] as const,
  planById: (id: string) => ['planById', id] as const,
  plan: (modelId: string, o: DeployOverrides) => ['plan', modelId, o] as const,
  join: ['join'] as const,
  apiKeys: ['apiKeys'] as const,
  gatewayModels: (baseUrl: string) => ['gatewayModels', baseUrl] as const,
};

// --- fleet ------------------------------------------------------------------

export function useCapacity() {
  return useQuery({ queryKey: qk.capacity, queryFn: () => api.getCapacity() });
}

export function useNodes() {
  return useQuery({
    queryKey: qk.nodes,
    queryFn: () => api.listNodes(),
    // The fleet is live; refresh in the background.
    refetchInterval: 8000,
  });
}

export function useNode(id: string | undefined) {
  return useQuery({
    queryKey: qk.node(id ?? ''),
    queryFn: () => api.getNode(id as string),
    enabled: Boolean(id),
  });
}

export function useNodeAction() {
  const qc = useQueryClient();
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: qk.nodes });
    qc.invalidateQueries({ queryKey: qk.capacity });
    qc.invalidateQueries({ queryKey: qk.catalog });
  };
  const drain = useMutation({ mutationFn: (id: string) => api.drainNode(id), onSuccess: invalidate });
  const restart = useMutation({ mutationFn: (id: string) => api.restartNode(id), onSuccess: invalidate });
  const remove = useMutation({ mutationFn: (id: string) => api.removeNode(id), onSuccess: invalidate });
  return { drain, restart, remove };
}

// --- catalog / model --------------------------------------------------------

export function useCatalog() {
  return useQuery({ queryKey: qk.catalog, queryFn: () => api.getCatalog() });
}

// --- model studio -----------------------------------------------------------

/**
 * Mutation: import a model from an external registry into the Purser catalog.
 * On success the catalog cache is invalidated so the Catalog page reflects the
 * new entry immediately.
 */
export function useImportModel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (source: ImportSource) => api.importModel(source),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.catalog }),
  });
}

/**
 * Mutation: delete a model from the catalog.
 * On success the catalog cache is invalidated. On 409 the error is surfaced to the caller
 * (model is referenced by active deployments — tear them down first).
 */
export function useDeleteModel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (modelId: string) => api.deleteModel(modelId),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.catalog }),
  });
}

/** Mutation: compute a plan-preview for an already-imported model. */
export function usePreviewModelPlan() {
  return useMutation({
    mutationFn: (modelId: string) => api.previewModelPlan(modelId),
  });
}

/** Mutation: deploy an already-imported model (alias over createDeployment). */
export function useDeployModel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { modelId: string }) =>
      api.createDeployment(args.modelId, { forceNodeCount: null, preference: 'balanced' }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.deployments });
      qc.invalidateQueries({ queryKey: qk.nodes });
      qc.invalidateQueries({ queryKey: qk.capacity });
      qc.invalidateQueries({ queryKey: qk.catalog });
    },
  });
}

export function useModel(id: string | undefined) {
  return useQuery({
    queryKey: qk.model(id ?? ''),
    queryFn: () => api.getModel(id as string),
    enabled: Boolean(id),
  });
}

// --- deployments ------------------------------------------------------------

export function useDeployments() {
  return useQuery({ queryKey: qk.deployments, queryFn: () => api.listDeployments() });
}

export function useDeployment(id: string | undefined) {
  return useQuery({
    queryKey: qk.deployment(id ?? ''),
    queryFn: () => api.getDeployment(id as string),
    enabled: Boolean(id),
    // Poll while a rollout is in progress so LOADING -> READY animates.
    refetchInterval: (query) =>
      query.state.data && query.state.data.state === 'active' ? false : 1000,
  });
}

export function usePlanPreview(modelId: string | undefined, overrides: DeployOverrides) {
  return useQuery({
    queryKey: qk.plan(modelId ?? '', overrides),
    queryFn: () => api.planDeployment(modelId as string, overrides),
    enabled: Boolean(modelId),
  });
}

/** GET /api/v1/plans/{id} — the authoritative plan + its explanation. */
export function usePlan(planId: string | undefined) {
  return useQuery({
    queryKey: qk.planById(planId ?? ''),
    queryFn: () => api.getPlan(planId as string),
    enabled: Boolean(planId),
  });
}

export function useCreateDeployment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { modelId: string; overrides: DeployOverrides }) =>
      api.createDeployment(args.modelId, args.overrides),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.deployments });
      qc.invalidateQueries({ queryKey: qk.nodes });
      qc.invalidateQueries({ queryKey: qk.capacity });
      qc.invalidateQueries({ queryKey: qk.catalog });
    },
  });
}

export function useUndeploy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.undeployDeployment(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.deployments });
      qc.invalidateQueries({ queryKey: qk.nodes });
      qc.invalidateQueries({ queryKey: qk.capacity });
      qc.invalidateQueries({ queryKey: qk.catalog });
    },
  });
}

// --- onboarding -------------------------------------------------------------

export function useJoinInfo() {
  return useQuery({ queryKey: qk.join, queryFn: () => api.getJoinInfo() });
}

export function useRotateToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.rotateJoinToken(),
    onSuccess: (data) => qc.setQueryData(qk.join, data),
  });
}

export function useCreateJoinToken() {
  return useMutation({
    mutationFn: (ttlSeconds: number) => api.createJoinToken(ttlSeconds),
  });
}

// --- api keys ---------------------------------------------------------------

export function useApiKeys() {
  return useQuery({ queryKey: qk.apiKeys, queryFn: () => api.listApiKeys() });
}

export function useCreateApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateApiKeyInput) => api.createApiKey(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiKeys }),
  });
}

export function useRevokeApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeApiKey(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiKeys }),
  });
}

// --- gateway (playground) ---------------------------------------------------

/** GET /v1/models on the Gateway (mock or real, per the chat client). */
export function useGatewayModels(chatClient: ChatClient) {
  return useQuery({
    queryKey: qk.gatewayModels(chatClient.baseUrl),
    queryFn: () => chatClient.listModels(),
    // The Gateway may be unreachable / keyless; fail fast and let the UI fall back.
    retry: 0,
    staleTime: 30_000,
  });
}

// --- live metrics (SSE) -----------------------------------------------------

/**
 * Subscribe to GET /api/v1/metrics for the lifetime of the component and expose
 * the latest snapshot. Returns null until the first frame arrives (or if the
 * stream errors), so callers can gracefully fall back to polled data.
 */
export function useMetricsStream(): MetricsSnapshot | null {
  const [snapshot, setSnapshot] = useState<MetricsSnapshot | null>(null);
  useEffect(() => {
    let alive = true;
    const stop = api.streamMetrics({
      onMetrics: (s) => {
        if (alive) setSnapshot(s);
      },
      onError: () => {
        /* keep the last good snapshot; polled data covers the gap */
      },
    });
    return () => {
      alive = false;
      stop();
    };
  }, []);
  return snapshot;
}

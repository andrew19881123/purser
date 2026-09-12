// React Query hooks wrapping the PurserApi client.
//
// Why React Query (over hand-rolled context): the whole app is read-mostly data
// from a remote control plane with loading/error/refetch/polling needs. React
// Query gives declarative loading & error states (which map straight onto our
// actionable ErrorState), background refetching for the live fleet/rollout,
// cache invalidation after operator actions, and — crucially for Phase 2 — it
// keeps every component decoupled from *how* data is fetched. Swapping the mock
// client for a real `fetch('/api/v1')` implementation touches zero components.
import { useEffect, useMemo, useState } from 'react';
import {
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { api, type CreateApiKeyInput, type CreateDataPlaneInput, type CreateServiceAccountInput } from '../api/client';
import { config } from '../api/config';
import type { ChatClient } from '../api/openai';
import type {
  AccessLogParams,
  DeployOverrides,
  ImportSource,
  InferenceAuditParams,
  MetricsSnapshot,
  WhatIfRequest,
} from '../api/types';

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
  reconcilerStatus: ['reconcilerStatus'] as const,
  sloCompliance: (windowHours: number) => ['sloCompliance', windowHours] as const,

  dataPlanes: ['dataPlanes'] as const,
  serviceAccounts: ['serviceAccounts'] as const,
  platformUsers: ['platformUsers'] as const,
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

// --- reconciler status ------------------------------------------------------

/**
 * Raw JSON shape returned by GET /api/v1/reconciler/status (Go handler).
 * Fields use snake_case to match the JSON tags on the Go structs.
 */
interface GoReconcilerStatusResponse {
  config: {
    interval_s: number;
    node_timeout_s: number;
    hysteresis_s: number;
    action_cooldown_s: number;
  };
  /** Per-event-type tracker snapshot; may be an empty object when nothing is tracked. */
  tracker: Record<string, { tracked: number; oldest_age_s: number }>;
}

/**
 * Derived reconciler health: state machine phase, last-sync timestamp (always
 * null — the Go API does not expose it), pending/error counts, plus the raw
 * config and tracker data for the enhanced status widget.
 *
 * Backed by GET /api/v1/reconciler/status (v0.3+ endpoint). When the endpoint
 * is absent (older control plane) the query enters the error state after one
 * retry — FleetPage renders a "Status unknown" badge instead of crashing or
 * hiding the card entirely.
 */
export interface ReconcilerStatus {
  state: 'idle' | 'syncing' | 'error';
  /** Always null — the Go API does not return a last-sync timestamp. */
  lastSyncAt: string | null;
  /** Sum of all tracker[*].tracked values. */
  pendingCount: number;
  /** Count of event types whose oldest tracked event exceeds the error age threshold. */
  errorCount: number;
  config: {
    intervalS: number;
    nodeTimeoutS: number;
    hysteresisS: number;
    actionCooldownS: number;
  };
  tracker: Record<string, { tracked: number; oldestAgeS: number }>;
}

/**
 * Age in seconds above which a tracked event is considered stale enough to
 * count as an error (5 minutes). This threshold is intentionally kept in the
 * UI layer so it can be tuned without a Go deploy.
 */
const RECONCILER_ERROR_AGE_S = 300;

function deriveReconcilerStatus(raw: GoReconcilerStatusResponse): ReconcilerStatus {
  const tracker: ReconcilerStatus['tracker'] = {};
  let pendingCount = 0;
  let errorCount = 0;

  for (const [key, val] of Object.entries(raw.tracker ?? {})) {
    tracker[key] = { tracked: val.tracked, oldestAgeS: val.oldest_age_s };
    pendingCount += val.tracked;
    if (val.tracked > 0 && val.oldest_age_s > RECONCILER_ERROR_AGE_S) {
      errorCount++;
    }
  }

  const state: ReconcilerStatus['state'] =
    errorCount > 0 ? 'error' : pendingCount > 0 ? 'syncing' : 'idle';

  return {
    state,
    lastSyncAt: null,
    pendingCount,
    errorCount,
    config: {
      intervalS: raw.config.interval_s,
      nodeTimeoutS: raw.config.node_timeout_s,
      hysteresisS: raw.config.hysteresis_s,
      actionCooldownS: raw.config.action_cooldown_s,
    },
    tracker,
  };
}

export function useReconcilerStatus() {
  return useQuery({
    queryKey: qk.reconcilerStatus,
    queryFn: (): Promise<ReconcilerStatus> =>
      fetch(`${config.apiBase}/reconciler/status`, { credentials: 'same-origin' }).then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return (r.json() as Promise<GoReconcilerStatusResponse>).then(deriveReconcilerStatus);
      }),
    retry: 1,
    retryDelay: 2000,
  });
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

// --- model health -----------------------------------------------------------

export function useModelHealth(modelId: string | undefined) {
  return useQuery({
    queryKey: ['modelHealth', modelId],
    queryFn: () => api.getModelHealth(modelId!),
    enabled: !!modelId,
    refetchInterval: 10_000,
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

// --- usage ------------------------------------------------------------------

export function useKeyUsage(keyId: string | undefined) {
  return useQuery({
    queryKey: ['keyUsage', keyId],
    queryFn: () => api.getKeyUsage(keyId!),
    enabled: !!keyId,
  });
}

export function useUsageSummary() {
  return useQuery({ queryKey: ['usageSummary'], queryFn: () => api.getUsageSummary() });
}

// --- billing / chargeback ---

/**
 * GET /api/v1/billing/report — chargeback usage aggregated by tenant+model.
 * Returns 402 when the "billing" enterprise feature is not licensed; callers
 * should detect ApiError with status 402 and show an upgrade prompt.
 */
export function useBillingReport(params: { days: number; tenantId?: string }) {
  const end = new Date().toISOString();
  const start = new Date(Date.now() - params.days * 86400000).toISOString();
  return useQuery({
    queryKey: ['billing', params],
    queryFn: () => api.getBillingReport(start, end, params.tenantId),
  });
}

export function useEnterpriseStatus() {
  return useQuery({ queryKey: ['enterpriseStatus'], queryFn: () => api.getEnterpriseStatus() });
}

// --- enterprise -------------------------------------------------------------

export function useAuditLog(limit = 100) {
  return useQuery({
    queryKey: ['auditLog', limit],
    queryFn: () => api.getAuditLog(limit),
  });
}

// --- inference audit --------------------------------------------------------

export function useInferenceAudit(params: InferenceAuditParams = {}) {
  return useQuery({
    queryKey: ['inferenceAudit', params],
    queryFn: () => api.listInferenceAudit(params),
  });
}

export function useAuditChainVerify() {
  return useQuery({
    queryKey: ['auditChainVerify'],
    queryFn: () => api.verifyAuditChain(),
    // Don't auto-refetch — operator triggers verification explicitly.
    refetchOnWindowFocus: false,
  });
}

export function useAccessLog(params: AccessLogParams = {}) {
  return useQuery({
    queryKey: ['accessLog', params],
    queryFn: () => api.listAccessLog(params),
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

// --- deployment approvals (AI Act Art.14) -----------------------------------

export const approvalQk = {
  list: (status?: string) => ['approvals', status ?? ''] as const,
  detail: (id: string) => ['approval', id] as const,
};

/**
 * GET /api/v1/approvals — list approval records.
 * Returns 402 when the deployment_approvals feature is not licensed;
 * the page should detect ApiError with status 402 and show an upgrade prompt.
 */
export function useApprovals(status?: string, limit = 50) {
  return useQuery({
    queryKey: approvalQk.list(status),
    queryFn: () => api.listDeploymentApprovals(status, limit),
    // Approvals are operator-facing; refresh every 10s when the page is open.
    refetchInterval: 10_000,
  });
}

export function useApproval(deploymentId: string | undefined) {
  return useQuery({
    queryKey: approvalQk.detail(deploymentId ?? ''),
    queryFn: () => api.getDeploymentApproval(deploymentId!),
    enabled: Boolean(deploymentId),
  });
}

export function useApproveDeployment() {
  const qc = useQueryClient();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['approvals'] });
    void qc.invalidateQueries({ queryKey: qk.deployments });
  };
  return useMutation({
    mutationFn: ({ deploymentId, notes }: { deploymentId: string; notes?: string }) =>
      api.approveDeployment(deploymentId, notes),
    onSuccess: invalidate,
  });
}

export function useRejectDeployment() {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: ['approvals'] });
  return useMutation({
    mutationFn: ({ deploymentId, notes }: { deploymentId: string; notes?: string }) =>
      api.rejectDeployment(deploymentId, notes),
    onSuccess: invalidate,
  });
}

// --- v0.4 platform model: organizations ------------------------------------

export function useOrganizations() {
  return useQuery({
    queryKey: ['organizations'],
    queryFn: () => api.listOrganizations(),
  });
}

export function useCreateOrganization() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { name: string; slug: string; description?: string }) =>
      api.createOrganization(data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['organizations'] }),
  });
}

export function useDeleteOrganization() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteOrganization(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['organizations'] }),
  });
}

// --- v0.4 platform model: teams -------------------------------------------

export function useTeams(orgId: string | undefined) {
  return useQuery({
    queryKey: ['teams', orgId ?? ''],
    queryFn: () => api.listTeams(orgId as string),
    enabled: Boolean(orgId),
  });
}

export function useTeam(id: string | undefined) {
  return useQuery({
    queryKey: ['team', id ?? ''],
    queryFn: () => api.getTeam(id as string),
    enabled: Boolean(id),
  });
}

export function useCreateTeam(orgId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { name: string; slug: string; description?: string }) =>
      api.createTeam(orgId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['teams', orgId] }),
  });
}

export function useDeleteTeam(orgId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteTeam(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['teams', orgId] }),
  });
}

// --- v0.4 platform model: team members ------------------------------------

export function useTeamMembers(teamId: string | undefined) {
  return useQuery({
    queryKey: ['teamMembers', teamId ?? ''],
    queryFn: () => api.listTeamMembers(teamId as string),
    enabled: Boolean(teamId),
  });
}

export function useAddTeamMember(teamId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { user_id: string; role_id: string }) =>
      api.addTeamMember(teamId, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['teamMembers', teamId] }),
  });
}

export function useRemoveTeamMember(teamId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => api.removeTeamMember(teamId, userId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['teamMembers', teamId] }),
  });
}

// --- v0.4 platform model: node pools ------------------------------------

export function useNodePools() {
  return useQuery({
    queryKey: ['nodePools'],
    queryFn: () => api.listNodePools(),
  });
}

export function useCreateNodePool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof api.createNodePool>[0]) =>
      api.createNodePool(data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodePools'] }),
  });
}

export function usePoolNodes(poolId: string | undefined) {
  return useQuery({
    queryKey: ['poolNodes', poolId ?? ''],
    queryFn: () => api.listPoolNodes(poolId as string),
    enabled: Boolean(poolId),
  });
}

export function usePoolQuotas(poolId: string | undefined) {
  return useQuery({
    queryKey: ['poolQuotas', poolId ?? ''],
    queryFn: () => api.listPoolQuotas(poolId as string),
    enabled: Boolean(poolId),
  });
}

export function useUpsertPoolQuota(poolId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ teamId, quota }: { teamId: string; quota: Parameters<typeof api.upsertPoolQuota>[2] }) =>
      api.upsertPoolQuota(poolId, teamId, quota),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['poolQuotas', poolId] }),
  });
}

export function useAssignNodeToPool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ poolId, nodeId }: { poolId: string; nodeId: string }) =>
      api.assignNodeToPool(poolId, nodeId),
    onSuccess: (_d, { poolId }) => qc.invalidateQueries({ queryKey: ['poolNodes', poolId] }),
  });
}

export function useRemoveNodeFromPool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ poolId, nodeId }: { poolId: string; nodeId: string }) =>
      api.removeNodeFromPool(poolId, nodeId),
    onSuccess: (_d, { poolId }) => qc.invalidateQueries({ queryKey: ['poolNodes', poolId] }),
  });
}

// --- v0.4 platform model: current user -----------------------------------

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: () => api.getMe(),
  });
}

export function useMyTeamPermissions(teamId: string | undefined) {
  return useQuery({
    queryKey: ['myTeamPermissions', teamId ?? ''],
    queryFn: () => api.getMyTeamPermissions(teamId as string),
    enabled: Boolean(teamId),
  });
}

// --- data planes ------------------------------------------------------------

export function useDataPlanes() {
  return useQuery({
    queryKey: qk.dataPlanes,
    queryFn: () => api.listDataPlanes(),
    refetchInterval: 30_000,
  });
}

export function useCreateDataPlane() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateDataPlaneInput) => api.createDataPlane(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.dataPlanes }),
  });
}

export function useRefreshDataPlaneConfig() {
  return useMutation({
    mutationFn: (id: string) => api.refreshDataPlaneConfig(id),
  });
}

// --- service accounts -------------------------------------------------------

export function useServiceAccounts() {
  return useQuery({
    queryKey: qk.serviceAccounts,
    queryFn: () => api.listServiceAccounts(),
  });
}

export function useCreateServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateServiceAccountInput) => api.createServiceAccount(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceAccounts }),
  });
}

export function useRevokeServiceAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeServiceAccount(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceAccounts }),
  });
}

// --- platform users ---------------------------------------------------------

export function usePlatformUsers() {
  return useQuery({
    queryKey: qk.platformUsers,
    queryFn: () => api.listPlatformUsers(),
  });
}

// --- live metrics (SSE) -----------------------------------------------------

// --- team slugs derived from API keys (for API key creation form) -----------

/**
 * Returns the unique set of team slugs already used in existing API keys.
 * Used to populate the "Team" dropdown in the Create API Key form so
 * operators pick from existing tenants rather than free-typing.
 */
export function useApiKeyTeamSlugs(): string[] {
  const { data: keys } = useApiKeys();
  return useMemo(() => {
    if (!keys || keys.length === 0) return [];
    return Array.from(new Set(keys.map((k) => k.team).filter(Boolean)));
  }, [keys]);
}

// --- what-if planner --------------------------------------------------------

export function useWhatIfPlan() {
  return useMutation({
    mutationFn: (request: WhatIfRequest) => api.whatIfPlan(request),
  });
}

// --- SLO compliance ---------------------------------------------------------

export function useSloCompliance(windowHours = 24) {
  return useQuery({
    queryKey: qk.sloCompliance(windowHours),
    queryFn: () =>
      api.getSloCompliance(windowHours).catch((e: unknown) => {
        // 404 = endpoint not available in this CP version (pre-v0.6); hide silently.
        if (e instanceof Error && e.message.includes('404')) return null;
        throw e;
      }),
    refetchInterval: 60_000,
  });
}

// --- billing forecast -------------------------------------------------------

export function useBillingForecast() {
  return useQuery({
    queryKey: ['billingForecast'],
    queryFn: () =>
      api.getBillingForecast().catch((e: unknown) => {
        // 404/402 = endpoint not available in this CP version; hide silently.
        if (e instanceof Error && /40[24]/.test(e.message)) return null;
        throw e;
      }),
  });
}

/**
 * Subscribe to GET /api/v1/metrics for the lifetime of the component and expose
 * the latest snapshot. Returns null snapshot until the first frame arrives.
 * `streamError` is set when the SSE stream errors so callers can show a stale
 * data warning while still displaying the last known values.
 */
export function useMetricsStream(): { snapshot: MetricsSnapshot | null; streamError: boolean } {
  const [snapshot, setSnapshot] = useState<MetricsSnapshot | null>(null);
  const [streamError, setStreamError] = useState(false);
  useEffect(() => {
    let alive = true;
    const stop = api.streamMetrics({
      onMetrics: (s) => {
        if (alive) {
          setSnapshot(s);
          setStreamError(false);
        }
      },
      onError: () => {
        if (alive) setStreamError(true);
        /* keep the last good snapshot; polled data covers the gap */
      },
    });
    return () => {
      alive = false;
      stop();
    };
  }, []);
  return { snapshot, streamError };
}

// --- SLO compliance (full nested shape, v0.6) --------------------------------

export const sloQk = {
  complianceFull: (windowHours: number) => ['sloComplianceFull', windowHours] as const,
};

export function useSloComplianceFull(windowHours = 24) {
  return useQuery({
    queryKey: sloQk.complianceFull(windowHours),
    queryFn: () =>
      api.getSloComplianceFull(windowHours).catch((e: unknown) => {
        // 404 = endpoint not available in this CP version; hide silently.
        if (e instanceof Error && e.message.includes('404')) return null;
        throw e;
      }),
    refetchInterval: 60_000,
  });
}

// --- policy-as-code (enterprise: policy_engine) ----------------------------

export const policyQk = {
  list: ['policies'] as const,
};

export function usePolicies() {
  return useQuery({
    queryKey: policyQk.list,
    queryFn: () => api.listPolicies(),
  });
}

export function useUpsertPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, rego, enabled }: { name: string; rego: string; enabled?: boolean }) =>
      api.upsertPolicy(name, rego, enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: policyQk.list }),
  });
}

export function useDeletePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.deletePolicy(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: policyQk.list }),
  });
}

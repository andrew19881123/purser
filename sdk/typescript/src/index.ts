/**
 * @purser/sdk — TypeScript client for the Purser control-plane management API.
 *
 * @example
 * ```ts
 * import { PurserClient } from '@purser/sdk';
 *
 * const client = new PurserClient('http://localhost:8080', 'psk_...');
 * const nodes = await client.listNodes();
 * const health = await client.clusterHealth();
 * ```
 */

export { PurserClient } from './client';
export type { CreateApiKeyOptions } from './client';

export { PurserError, NotFoundError, ConflictError, LicenseRequiredError } from './errors';

export type {
  // Node
  Node,
  NodeState,
  // Model
  Model,
  ModelSpec,
  ModelWithFit,
  ModelHealth,
  EstimateRange,
  Fit,
  // Plan
  Plan,
  PlanPreview,
  PlanPreviewFeasible,
  PlanPreviewInfeasible,
  DeployResponse,
  // Deployment
  Deployment,
  DeploymentState,
  // API key
  APIKey,
  KeyUsage,
  // Cluster
  JoinToken,
  ClusterHealth,
  // Enterprise
  EnterpriseStatus,
  AuditEntry,
  AuditLog,
  ChainVerification,
  ChainBreak,
} from './types';

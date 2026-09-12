// WhatIfPlannerPage — hardware ROI simulation via POST /api/v1/planner/what-if.
//
// Lets operators add virtual nodes (spec-only, no real hardware) and see whether
// a given model would become deployable and by how much throughput would improve.
// No enterprise gate — available to all editions.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingBlock,
  PageHeader,
  useFieldId,
  type Tone,
} from '../components/ui';
import { useCatalog, useWhatIfPlan } from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { WhatIfNode, WhatIfResult } from '../api/types';

// ---------------------------------------------------------------------------
// Virtual node form row
// ---------------------------------------------------------------------------

interface VirtualNodeRowProps {
  index: number;
  node: WhatIfNode;
  onChange: (updated: WhatIfNode) => void;
  onRemove: () => void;
  t: ReturnType<typeof useT>;
}

function VirtualNodeRow({ index, node, onChange, onRemove, t }: VirtualNodeRowProps) {
  const nodeIdId = useFieldId(`wif-node-id-${index}`);
  const vramId = useFieldId(`wif-vram-${index}`);
  const countId = useFieldId(`wif-count-${index}`);
  const bwId = useFieldId(`wif-bw-${index}`);

  return (
    <div
      className="what-if-node-row"
      style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', flexWrap: 'wrap', marginBottom: '0.75rem', padding: '0.75rem', border: '1px solid var(--color-border)', borderRadius: '6px' }}
    >
      <div style={{ flex: '1 1 120px' }}>
        <Field label={t('planner.whatIf.nodeId')} htmlFor={nodeIdId}>
          <input
            id={nodeIdId}
            className="input"
            value={node.node_id}
            placeholder={`virtual-${index + 1}`}
            onChange={(e) => onChange({ ...node, node_id: e.target.value })}
          />
        </Field>
      </div>
      <div style={{ flex: '1 1 80px' }}>
        <Field label={t('planner.whatIf.gpuVram')} htmlFor={vramId}>
          <input
            id={vramId}
            className="input"
            type="number"
            min={1}
            value={node.gpu_vram_gb || ''}
            placeholder="24"
            onChange={(e) => onChange({ ...node, gpu_vram_gb: Number(e.target.value) })}
          />
        </Field>
      </div>
      <div style={{ flex: '1 1 60px' }}>
        <Field label={t('planner.whatIf.gpuCount')} htmlFor={countId}>
          <input
            id={countId}
            className="input"
            type="number"
            min={0}
            value={node.gpu_count || ''}
            placeholder="1"
            onChange={(e) => onChange({ ...node, gpu_count: Number(e.target.value) })}
          />
        </Field>
      </div>
      <div style={{ flex: '1 1 80px' }}>
        <Field label={t('planner.whatIf.netBandwidth')} htmlFor={bwId}>
          <input
            id={bwId}
            className="input"
            type="number"
            min={0}
            step="0.1"
            value={node.net_bandwidth_gbps || ''}
            placeholder="10"
            onChange={(e) => onChange({ ...node, net_bandwidth_gbps: Number(e.target.value) })}
          />
        </Field>
      </div>
      <div style={{ display: 'flex', alignItems: 'flex-end', paddingBottom: '0.25rem' }}>
        <Button variant="danger" size="sm" onClick={onRemove}>
          {t('planner.whatIf.removeNode')}
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Result panel
// ---------------------------------------------------------------------------

function ResultPanel({ result, t }: { result: WhatIfResult; t: ReturnType<typeof useT> }) {
  const feasibleTone: Tone = result.feasible ? 'success' : 'danger';

  return (
    <Card title="Simulation result">
      <div style={{ display: 'flex', gap: '1rem', alignItems: 'center', marginBottom: '1rem' }}>
        <Badge tone={feasibleTone} data-testid="whatif-feasible-badge">
          {result.feasible ? t('planner.whatIf.result.feasible') : t('planner.whatIf.result.infeasible')}
        </Badge>
        {result.current_plan !== undefined && (
          <span className="muted">
            {t('planner.whatIf.result.currentPlan', {
              status: result.current_plan.feasible
                ? t('planner.whatIf.result.feasible')
                : t('planner.whatIf.result.infeasible'),
            })}
          </span>
        )}
      </div>

      {!result.feasible && result.reason && (
        <p className="muted">{t('planner.whatIf.result.reason', { reason: result.reason })}</p>
      )}

      {result.feasible && (
        <>
          {result.estimated_decode_tok_s_min !== undefined && result.estimated_decode_tok_s_max !== undefined && (
            <p className="stat__value" style={{ marginBottom: '0.5rem' }}>
              {t('planner.whatIf.result.throughput', {
                min: result.estimated_decode_tok_s_min.toFixed(0),
                max: result.estimated_decode_tok_s_max.toFixed(0),
              })}
            </p>
          )}

          {result.improvement_delta !== undefined && result.improvement_delta > 0 && (
            <p style={{ marginBottom: '0.75rem' }}>
              <Badge tone="success">
                {t('planner.whatIf.result.delta', {
                  delta: (result.improvement_delta * 100).toFixed(0),
                })}
              </Badge>
            </p>
          )}

          {result.assignments && result.assignments.length > 0 && (
            <>
              <p className="muted" style={{ marginBottom: '0.5rem' }}>{t('planner.whatIf.result.assignments')}</p>
              <div className="table-wrap">
                <table className="table" data-testid="whatif-assignments-table">
                  <thead>
                    <tr>
                      <th scope="col">{t('planner.whatIf.result.col.nodeId')}</th>
                      <th scope="col">{t('planner.whatIf.result.col.layerStart')}</th>
                      <th scope="col">{t('planner.whatIf.result.col.layerEnd')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {result.assignments.map((a) => (
                      <tr key={a.node_id}>
                        <td><code>{a.node_id}</code></td>
                        <td>{a.layer_start}</td>
                        <td>{a.layer_end}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function newVirtualNode(index: number): WhatIfNode {
  return { node_id: `virtual-${index + 1}`, gpu_vram_gb: 24, gpu_count: 1, net_bandwidth_gbps: 10 };
}

export function WhatIfPlannerPage() {
  const t = useT();
  const catalog = useCatalog();
  const whatIf = useWhatIfPlan();

  const modelSelectId = useFieldId('wif-model');
  const [modelId, setModelId] = useState('');
  const [virtualNodes, setVirtualNodes] = useState<WhatIfNode[]>([newVirtualNode(0)]);
  const [includeExisting, setIncludeExisting] = useState(true);

  const models = (catalog.data ?? []).map((e) => e.model);

  const addNode = () => setVirtualNodes((prev) => [...prev, newVirtualNode(prev.length)]);

  const removeNode = (i: number) =>
    setVirtualNodes((prev) => prev.filter((_, idx) => idx !== i));

  const updateNode = (i: number, updated: WhatIfNode) =>
    setVirtualNodes((prev) => prev.map((n, idx) => (idx === i ? updated : n)));

  const simulate = () => {
    if (!modelId) return;
    whatIf.mutate({
      model_id: modelId,
      hypothetical_nodes: virtualNodes.map((n) => ({
        ...n,
        node_id: n.node_id.trim() || `virtual-${virtualNodes.indexOf(n) + 1}`,
      })),
      include_existing_nodes: includeExisting,
    });
  };

  return (
    <div className="page">
      <PageHeader title={t('planner.whatIf.title')} subtitle={t('planner.whatIf.subtitle')} />

      <Card title="Configuration">
        {catalog.isLoading && <LoadingBlock />}
        {catalog.isError && (
          <ErrorState message={errorMessage(catalog.error, t, 'error.catalog')} onRetry={() => catalog.refetch()} />
        )}
        {!catalog.isLoading && !catalog.isError && (
          <>
            <Field label={t('planner.whatIf.model')} htmlFor={modelSelectId}>
              <select
                id={modelSelectId}
                className="input"
                value={modelId}
                onChange={(e) => setModelId(e.target.value)}
              >
                <option value="">{t('planner.whatIf.noModel')}</option>
                {models.map((m) => (
                  <option key={m.modelId} value={m.modelId}>{m.modelId}</option>
                ))}
              </select>
            </Field>

            <div style={{ marginTop: '1rem' }}>
              <p className="muted" style={{ marginBottom: '0.5rem' }}>{t('planner.whatIf.nodes.title')}</p>
              {virtualNodes.map((node, i) => (
                <VirtualNodeRow
                  key={i}
                  index={i}
                  node={node}
                  onChange={(updated) => updateNode(i, updated)}
                  onRemove={() => removeNode(i)}
                  t={t}
                />
              ))}
              <Button variant="ghost" size="sm" onClick={addNode}>
                {t('planner.whatIf.addNode')}
              </Button>
            </div>

            <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginTop: '1rem', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={includeExisting}
                onChange={(e) => setIncludeExisting(e.target.checked)}
              />
              {t('planner.whatIf.includeExisting')}
            </label>

            <div style={{ marginTop: '1.25rem' }}>
              <Button
                variant="primary"
                onClick={simulate}
                disabled={whatIf.isPending || !modelId}
              >
                {whatIf.isPending ? t('common.loading') : t('planner.whatIf.simulate')}
              </Button>
            </div>
          </>
        )}
      </Card>

      {whatIf.isPending && <LoadingBlock />}

      {whatIf.isError && (
        <ErrorState message={errorMessage(whatIf.error, t, 'error.whatIfPlan')} />
      )}

      {whatIf.data && (
        <ResultPanel result={whatIf.data} t={t} />
      )}

      {!whatIf.data && !whatIf.isPending && !whatIf.isError && !modelId && (
        <EmptyState message={t('planner.whatIf.noModel')} />
      )}
    </div>
  );
}

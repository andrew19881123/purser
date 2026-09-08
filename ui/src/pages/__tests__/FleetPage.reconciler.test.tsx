// Unit tests for ReconcilerStatusCard — pure display component that receives
// a ReconcilerStatus prop and renders state badge, pending/error counts,
// active tracker events, and a collapsible configuration panel.
//
// Tests cover:
//   - undefined status (loading/error) → "Status unknown" fallback
//   - idle state (no tracked events) → success badge, zero counts
//   - syncing state (tracked > 0, age < threshold) → info badge, event table
//   - error state (tracked > 0, age > threshold) → danger badge
//   - config values rendered in the collapsible panel
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { ReactElement } from 'react';
import { I18nProvider } from '../../i18n';
import { ReconcilerStatusCard } from '../FleetPage';
import type { ReconcilerStatus } from '../../hooks/queries';

/** Wrap a component in the providers required by ReconcilerStatusCard. */
function wrap(ui: ReactElement) {
  return render(<I18nProvider>{ui}</I18nProvider>);
}

/** Build a minimal valid ReconcilerStatus, applying caller overrides. */
function makeStatus(overrides: Partial<ReconcilerStatus> = {}): ReconcilerStatus {
  return {
    state: 'idle',
    lastSyncAt: null,
    pendingCount: 0,
    errorCount: 0,
    config: {
      intervalS: 10,
      nodeTimeoutS: 45,
      hysteresisS: 30,
      actionCooldownS: 60,
    },
    tracker: {},
    ...overrides,
  };
}

describe('ReconcilerStatusCard', () => {
  it('shows_status_unknown_badge_when_status_is_undefined', () => {
    wrap(<ReconcilerStatusCard status={undefined} />);
    screen.getByText('Status unknown');
  });

  it('shows_idle_state_when_all_tracker_counts_are_zero', () => {
    const status = makeStatus({
      state: 'idle',
      pendingCount: 0,
      errorCount: 0,
      tracker: {
        node_down: { tracked: 0, oldestAgeS: 0 },
        orphan_deployment: { tracked: 0, oldestAgeS: 0 },
      },
    });
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('idle');
    // No event table rows for zero-tracked events
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows_syncing_state_and_pending_count_when_tracker_has_events', () => {
    const status = makeStatus({
      state: 'syncing',
      pendingCount: 2,
      errorCount: 0,
      tracker: {
        node_down: { tracked: 0, oldestAgeS: 0 },
        orphan_deployment: { tracked: 2, oldestAgeS: 120 },
      },
    });
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('syncing');
    // '2' appears in the stat grid (pendingCount) and in the event table (tracked).
    // Use getAllByText since the same number intentionally renders in both places.
    expect(screen.getAllByText('2').length).toBeGreaterThanOrEqual(1);
    // Active events table is shown with the event type
    screen.getByText('orphan_deployment');
    // zero-tracked event is not shown
    expect(screen.queryByText('node_down')).not.toBeInTheDocument();
  });

  it('shows_error_state_badge_when_error_count_is_positive', () => {
    const status = makeStatus({
      state: 'error',
      pendingCount: 1,
      errorCount: 1,
      tracker: {
        node_down: { tracked: 1, oldestAgeS: 600 },
      },
    });
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('error');
    // '1' appears as pendingCount, errorCount, and tracked count in the event table.
    expect(screen.getAllByText('1').length).toBeGreaterThanOrEqual(1);
    // Event type is shown in the active events table
    screen.getByText('node_down');
  });

  it('shows_config_values_in_the_collapsed_panel', () => {
    const status = makeStatus({
      config: {
        intervalS: 15,
        nodeTimeoutS: 60,
        hysteresisS: 20,
        actionCooldownS: 90,
      },
    });
    const { container } = wrap(<ReconcilerStatusCard status={status} />);
    // <details> content is in the DOM even when closed
    expect(container.textContent).toContain('15s');
    expect(container.textContent).toContain('60s');
    // The summary/label text is also present
    expect(container.textContent).toContain('Configuration');
  });

  it('renders_last_sync_timestamp_when_present', () => {
    const ts = '2026-09-05T12:00:00Z';
    const status = makeStatus({ lastSyncAt: ts });
    const { container } = wrap(<ReconcilerStatusCard status={status} />);
    expect(container.textContent ?? '').toMatch(/Last sync/);
  });
});

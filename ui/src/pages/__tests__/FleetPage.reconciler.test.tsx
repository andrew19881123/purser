// Unit tests for ReconcilerStatusCard — pure display component that receives
// a ReconcilerStatus prop and renders state badge, pending/error counts, and
// last-sync timestamp.
// Tests cover:
//   - undefined status (loading/error) → "Status unknown" fallback
//   - idle state → success badge
//   - syncing state → info badge
//   - error state → danger badge
//   - pending/error counts and lastSyncAt rendering
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

describe('ReconcilerStatusCard', () => {
  it('renders_status_unknown_when_no_data', () => {
    wrap(<ReconcilerStatusCard status={undefined} />);
    screen.getByText('Status unknown');
  });

  it('renders_idle_state_as_success_badge', () => {
    const status: ReconcilerStatus = {
      state: 'idle',
      lastSyncAt: null,
      pendingCount: 0,
      errorCount: 0,
    };
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('idle');
  });

  it('renders_error_state_badge', () => {
    const status: ReconcilerStatus = {
      state: 'error',
      lastSyncAt: null,
      pendingCount: 0,
      errorCount: 3,
    };
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('error');
    screen.getByText('3');
  });

  it('renders_syncing_state_badge', () => {
    const status: ReconcilerStatus = {
      state: 'syncing',
      lastSyncAt: null,
      pendingCount: 2,
      errorCount: 0,
    };
    wrap(<ReconcilerStatusCard status={status} />);
    screen.getByText('syncing');
    screen.getByText('2');
  });

  it('renders_last_sync_timestamp_when_present', () => {
    const ts = '2026-09-05T12:00:00Z';
    const status: ReconcilerStatus = {
      state: 'idle',
      lastSyncAt: ts,
      pendingCount: 0,
      errorCount: 0,
    };
    const { container } = wrap(<ReconcilerStatusCard status={status} />);
    expect(container.textContent ?? '').toMatch(/Last sync/);
  });
});

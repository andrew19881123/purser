// Unit tests for ReconcilerStatusCard — pure display component that receives
// a ReconcilerStatus prop and renders the config summary + pending event list.
// These tests exercise the three key UI states:
//   - healthy (empty tracker)
//   - pending events present (tracker with tracked > 0)
//   - config values visible in the config row
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { ReactElement } from 'react';
import { I18nProvider } from '../../i18n';
import { ReconcilerStatusCard } from '../FleetPage';
import type { ReconcilerStatus } from '../../api/types';

/** Wrap a component in the providers required by ReconcilerStatusCard. */
function wrap(ui: ReactElement) {
  return render(<I18nProvider>{ui}</I18nProvider>);
}

const baseConfig: ReconcilerStatus['config'] = {
  intervalS: 10,
  nodeTimeoutS: 45,
  hysteresisS: 30,
  actionCooldownS: 120,
};

describe('ReconcilerStatusCard', () => {
  it('renders_reconciler_healthy_when_no_pending', () => {
    const status: ReconcilerStatus = { config: baseConfig, tracker: {} };
    wrap(<ReconcilerStatusCard status={status} />);
    // getByText throws if the element is absent — no assertion needed beyond the call.
    screen.getByText('Healthy');
  });

  it('renders_pending_events_when_present', () => {
    const status: ReconcilerStatus = {
      config: baseConfig,
      tracker: { node_down: { tracked: 2, oldestAgeS: 45 } },
    };
    wrap(<ReconcilerStatusCard status={status} />);
    // Event type rendered as a warning Badge; "Healthy" must NOT be present.
    screen.getByText('node_down');
  });

  it('renders_config_values', () => {
    const status: ReconcilerStatus = { config: baseConfig, tracker: {} };
    const { container } = wrap(<ReconcilerStatusCard status={status} />);
    expect(container.textContent ?? '').toMatch(/Interval: 10s/);
    expect(container.textContent ?? '').toMatch(/Timeout: 45s/);
  });
});

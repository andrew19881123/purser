/**
 * useMetricsStream — SSE lifecycle tests (M3).
 *
 * Verifies:
 * 1. EventSource is closed (stop function called) when the component unmounts.
 * 2. Calling the onerror handler sets streamError to true.
 * 3. A subsequent successful onMetrics call resets streamError to false.
 */
import { renderHook, act } from '@testing-library/react';
import { useMetricsStream } from '../queries';
import type { MetricsStreamHandlers, MetricsSnapshot } from '../../api/types';

// ---- captured state shared between mock factory and tests ------------------

// vi.hoisted ensures these values are initialised before vi.mock runs.
const captured = vi.hoisted(() => ({
  handlers: undefined as MetricsStreamHandlers | undefined,
  stopFn: vi.fn(),
}));

// ---- mock api.streamMetrics -------------------------------------------------

vi.mock('../../api/client', () => ({
  api: {
    streamMetrics: (handlers: MetricsStreamHandlers) => {
      captured.handlers = handlers;
      return captured.stopFn;
    },
  },
}));

// ---- helpers ----------------------------------------------------------------

function fakeSnapshot(): MetricsSnapshot {
  return {
    at: '2026-01-01T00:00:00Z',
    aggregateDecodeTokS: 42,
    nodes: [],
  };
}

// ---- setup ------------------------------------------------------------------

beforeEach(() => {
  captured.stopFn.mockClear();
  captured.handlers = undefined;
});

// ---- tests ------------------------------------------------------------------

describe('useMetricsStream — cleanup', () => {
  it('closes_eventsource_on_unmount', () => {
    const { unmount } = renderHook(() => useMetricsStream());

    // streamMetrics was called → handlers captured
    expect(captured.handlers).toBeDefined();
    unmount();

    expect(captured.stopFn).toHaveBeenCalledOnce();
  });
});

describe('useMetricsStream — streamError', () => {
  it('sets_stream_error_on_onerror', () => {
    const { result } = renderHook(() => useMetricsStream());

    expect(result.current.streamError).toBe(false);

    act(() => {
      captured.handlers?.onError?.(new Error('SSE disconnected'));
    });

    expect(result.current.streamError).toBe(true);
    // snapshot preserved at last known value (null on first error)
    expect(result.current.snapshot).toBeNull();
  });

  it('resets_stream_error_on_successful_metrics', () => {
    const { result } = renderHook(() => useMetricsStream());

    // First trigger an error
    act(() => {
      captured.handlers?.onError?.(new Error('SSE disconnected'));
    });
    expect(result.current.streamError).toBe(true);

    // Then a successful metric should clear the error
    act(() => {
      captured.handlers?.onMetrics(fakeSnapshot());
    });
    expect(result.current.streamError).toBe(false);
    expect(result.current.snapshot).not.toBeNull();
  });
});

// Small, dependency-free formatting helpers. Locale-neutral by design so the
// same numbers render identically regardless of UI language.

export function gb(value: number, digits = 0): string {
  return `${value.toFixed(digits)} GB`;
}

export function tokS(value: number): string {
  return `${Math.round(value)} tok/s`;
}

/** Render a performance range honestly as "min–max unit" (never a single number). */
export function range(min: number, max: number, unit: string): string {
  const lo = Math.round(min);
  const hi = Math.round(max);
  if (lo === hi) return `~${lo} ${unit}`;
  return `${lo}–${hi} ${unit}`;
}

export function percent(value0to1: number, digits = 0): string {
  return `${(value0to1 * 100).toFixed(digits)}%`;
}

/** "3.4 s ago", "2 min ago", "1 h ago" — compact relative time. */
export function relativeTime(iso: string, now: Date = new Date()): string {
  const then = new Date(iso).getTime();
  const secs = Math.max(0, Math.round((now.getTime() - then) / 1000));
  if (secs < 5) return 'just now';
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

/** Bytes-ish human count for parameters (in billions). */
export function billions(value: number): string {
  return `${value % 1 === 0 ? value.toFixed(0) : value.toFixed(1)}B`;
}

export function clamp(value: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, value));
}

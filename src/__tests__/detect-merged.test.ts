import { describe, expect, it } from 'vitest';

import { resolveSince } from '../detect-merged.ts';
import type { ResolvedConfig } from '../lib/config.ts';
import type { StateEntry } from '../lib/state.ts';

/** Minimal synthetic config — only the fields `resolveSince` touches. */
function makeCfg(overrides: Partial<ResolvedConfig['raw']['detect']> = {}): ResolvedConfig {
  const detect = {
    lookback_days: 30,
    max_lookback_days: 180,
    limit: 50,
    start_after_pr: 0,
    ...overrides,
  };
  return {
    raw: { detect } as ResolvedConfig['raw'],
  } as ResolvedConfig;
}

const NOW = new Date('2026-04-17T00:00:00Z');

describe('resolveSince', () => {
  it('absolute `detect.since` always wins', () => {
    const cfg = makeCfg({ since: '2026-01-01' });
    const r = resolveSince(cfg, [], NOW);
    expect(r).toEqual({ since: '2026-01-01', source: 'config.since' });
  });

  it('falls back to lookback_days when state is empty', () => {
    const cfg = makeCfg({ lookback_days: 30 });
    const r = resolveSince(cfg, [], NOW);
    // 30 days before 2026-04-17 = 2026-03-18
    expect(r).toEqual({ since: '2026-03-18', source: 'lookback_days' });
  });

  it('widens to oldest non-terminal entry when state is older than lookback', () => {
    const cfg = makeCfg({ lookback_days: 30, max_lookback_days: 180 });
    const entries: StateEntry[] = [
      // 60 days ago, still in-flight (only "started") → non-terminal
      { pr: 100, event: 'started', at: '2026-02-16T12:00:00Z', run_id: 'r1' },
      // terminal, should be ignored
      { pr: 99, event: 'promoted', at: '2026-01-01T00:00:00Z', run_id: 'r0' },
    ];
    const r = resolveSince(cfg, entries, NOW);
    expect(r.source).toBe('state_aware');
    expect(r.since).toBe('2026-02-16');
  });

  it('clamps at max_lookback_days even if state points further back', () => {
    const cfg = makeCfg({ lookback_days: 30, max_lookback_days: 60 });
    const entries: StateEntry[] = [
      // 200 days ago, non-terminal — but max_lookback is 60
      { pr: 5, event: 'started', at: '2025-09-29T00:00:00Z', run_id: 'r' },
    ];
    const r = resolveSince(cfg, entries, NOW);
    expect(r.source).toBe('max_lookback_clamp');
    // 60 days before 2026-04-17 = 2026-02-16
    expect(r.since).toBe('2026-02-16');
  });

  it('ignores terminal entries even if older than lookback', () => {
    const cfg = makeCfg({ lookback_days: 30 });
    const entries: StateEntry[] = [
      // 90 days ago but terminal — does not widen
      { pr: 50, event: 'failed', at: '2026-01-17T00:00:00Z', run_id: 'r', reason: 'x' },
    ];
    const r = resolveSince(cfg, entries, NOW);
    expect(r.source).toBe('lookback_days');
    expect(r.since).toBe('2026-03-18');
  });

  it('does not widen when state is within lookback window', () => {
    const cfg = makeCfg({ lookback_days: 30 });
    const entries: StateEntry[] = [
      { pr: 77, event: 'started', at: '2026-04-10T00:00:00Z', run_id: 'r' },
    ];
    const r = resolveSince(cfg, entries, NOW);
    expect(r.source).toBe('lookback_days');
    expect(r.since).toBe('2026-03-18');
  });
});

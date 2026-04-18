import { describe, expect, it } from 'vitest';

import { collapseByKey } from '../jsonl.ts';

describe('collapseByKey', () => {
  it('last entry wins by array order', () => {
    const arr = [
      { k: 1, v: 'a' },
      { k: 2, v: 'b' },
      { k: 1, v: 'a2' },
    ];
    const m = collapseByKey(arr, (e) => e.k);
    expect(m.get(1)?.v).toBe('a2');
    expect(m.get(2)?.v).toBe('b');
  });

  it('tieBreak picks larger value', () => {
    const arr = [
      { k: 1, v: 'old', ts: '2026-01-01' },
      { k: 1, v: 'new', ts: '2026-04-01' },
      { k: 1, v: 'older', ts: '2025-12-01' },
    ];
    const m = collapseByKey(arr, (e) => e.k, (e) => e.ts);
    expect(m.get(1)?.v).toBe('new');
  });

  it('empty input → empty map', () => {
    expect(collapseByKey([], (x: number) => x).size).toBe(0);
  });
});

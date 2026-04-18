import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import {
  appendState,
  isTerminal,
  maxRecordedPR,
  readState,
  StateError,
  summarize,
  TERMINAL_EVENTS,
  unprocessedPRs,
} from '../state.ts';

describe('state.ts', () => {
  let dir: string;
  let path: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'auto-improve-state-'));
    path = join(dir, 'processed.jsonl');
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it('returns [] for missing file', () => {
    expect(readState(path)).toEqual([]);
  });

  it('appends and round-trips entries', () => {
    appendState(path, { pr: 1, event: 'started', run_id: 'r1' });
    appendState(path, {
      pr: 1,
      event: 'step_done',
      run_id: 'r1',
      step: 10,
    });
    const entries = readState(path);
    expect(entries).toHaveLength(2);
    expect(entries[0]?.pr).toBe(1);
    expect(entries[0]?.event).toBe('started');
    expect(entries[1]?.step).toBe(10);
  });

  it('summarize marks terminal correctly', () => {
    appendState(path, { pr: 1, event: 'started', run_id: 'r1' });
    appendState(path, { pr: 1, event: 'promoted', run_id: 'r1', adopted: true });
    appendState(path, { pr: 2, event: 'started', run_id: 'r2' });
    const s = summarize(readState(path));
    expect(s.get(1)?.terminal).toBe(true);
    expect(s.get(2)?.terminal).toBe(false);
    expect(s.get(1)?.last.event).toBe('promoted');
  });

  it('unprocessedPRs filters terminals but retains only-started', () => {
    appendState(path, { pr: 10, event: 'promoted', run_id: 'a' });
    appendState(path, { pr: 11, event: 'failed', run_id: 'b' });
    appendState(path, { pr: 12, event: 'started', run_id: 'c' });
    const entries = readState(path);
    expect(unprocessedPRs([10, 11, 12, 13], entries)).toEqual([12, 13]);
  });

  it('isTerminal checks single PR', () => {
    appendState(path, { pr: 5, event: 'skipped', run_id: 'r' });
    const entries = readState(path);
    expect(isTerminal(5, entries)).toBe(true);
    expect(isTerminal(6, entries)).toBe(false);
  });

  it('maxRecordedPR returns highest observed', () => {
    appendState(path, { pr: 3, event: 'started', run_id: 'r' });
    appendState(path, { pr: 99, event: 'failed', run_id: 'r' });
    appendState(path, { pr: 7, event: 'promoted', run_id: 'r' });
    expect(maxRecordedPR(readState(path))).toBe(99);
  });

  it('throws StateError on malformed line', () => {
    writeFileSync(path, 'not json\n', 'utf8');
    expect(() => readState(path)).toThrow(StateError);
  });

  it('throws StateError on schema-invalid entry', () => {
    writeFileSync(path, JSON.stringify({ pr: 'notanumber', event: 'started' }) + '\n', 'utf8');
    expect(() => readState(path)).toThrow(StateError);
  });

  it("terminal event set matches the plan's M2 vocabulary", () => {
    expect(Array.from(TERMINAL_EVENTS).sort()).toEqual(
      ['completed', 'failed', 'promoted', 'rollback', 'skipped', 'timeout'].sort(),
    );
  });

  it('timeout is treated as terminal (not retried)', () => {
    appendState(path, { pr: 20, event: 'started', run_id: 'r' });
    appendState(path, {
      pr: 20,
      event: 'timeout',
      run_id: 'r',
      reason: 'agent exceeded 1800s',
    });
    const entries = readState(path);
    expect(isTerminal(20, entries)).toBe(true);
    expect(unprocessedPRs([20, 21], entries)).toEqual([21]);
  });

  it('ignores blank lines', () => {
    writeFileSync(
      path,
      `\n${JSON.stringify({ pr: 1, event: 'started', at: new Date().toISOString(), run_id: 'r' })}\n\n`,
      'utf8',
    );
    expect(readState(path)).toHaveLength(1);
  });
});

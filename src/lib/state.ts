import { appendFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import { dirname } from 'node:path';

import {
  EVENT_VALUES,
  type Event,
  StateEntrySchema,
  type StateEntry,
  TERMINAL_EVENTS,
} from '../contracts.ts';

// Re-export for backward compatibility with existing consumers / tests.
export { EVENT_VALUES, type Event, StateEntrySchema, type StateEntry, TERMINAL_EVENTS };

/**
 * processed.jsonl — append-only event log.
 *
 * Schema and terminal set are defined in `src/contracts.ts` (single source
 * of truth). This module only hosts the read/write helpers.
 *
 * Idempotency model:
 *   - A PR is considered "terminal" once it has any of:
 *       promoted | rollback | skipped | failed | completed | timeout
 *     Terminal PRs are filtered out by `unprocessedPRs()` regardless of prior "started".
 *   - A PR may appear multiple times in the log (retries); latest terminal event wins.
 */

export interface PRStateSummary {
  pr: number;
  last: StateEntry;
  terminal: boolean;
  events: Event[];
}

/** Read and parse a processed.jsonl file. Missing file → empty array. */
export function readState(path: string): StateEntry[] {
  if (!existsSync(path)) return [];
  const text = readFileSync(path, 'utf8');
  const out: StateEntry[] = [];
  let lineNum = 0;
  for (const rawLine of text.split('\n')) {
    lineNum++;
    const line = rawLine.trim();
    if (!line) continue;
    let obj: unknown;
    try {
      obj = JSON.parse(line);
    } catch (e) {
      throw new StateError(`Invalid JSON at ${path}:${lineNum}: ${(e as Error).message}`);
    }
    const parsed = StateEntrySchema.safeParse(obj);
    if (!parsed.success) {
      const issues = parsed.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`).join('; ');
      throw new StateError(`Invalid state entry at ${path}:${lineNum}: ${issues}`);
    }
    out.push(parsed.data);
  }
  return out;
}

/**
 * Collapse the log into per-PR summaries keyed by PR number.
 * `last` is the most recent entry for that PR (by array order — jsonl is append-only).
 */
export function summarize(entries: StateEntry[]): Map<number, PRStateSummary> {
  const byPr = new Map<number, PRStateSummary>();
  for (const e of entries) {
    const prev = byPr.get(e.pr);
    if (!prev) {
      byPr.set(e.pr, {
        pr: e.pr,
        last: e,
        terminal: TERMINAL_EVENTS.has(e.event),
        events: [e.event],
      });
    } else {
      prev.last = e;
      prev.events.push(e.event);
      prev.terminal = prev.terminal || TERMINAL_EVENTS.has(e.event);
    }
  }
  return byPr;
}

/**
 * Append an entry to processed.jsonl atomically enough for our use case.
 * Uses Node's sync appendFile with an O_APPEND semantic; the OS guarantees
 * line atomicity for writes smaller than PIPE_BUF (4KB on macOS/Linux), which
 * our entries comfortably fit under.
 *
 * Creates the directory if missing.
 */
export function appendState(
  path: string,
  entry: Omit<StateEntry, 'at'> & { at?: string },
): StateEntry {
  const full: StateEntry = {
    ...entry,
    at: entry.at ?? new Date().toISOString(),
  };
  const parsed = StateEntrySchema.parse(full);
  const line = JSON.stringify(parsed) + '\n';
  mkdirSync(dirname(path), { recursive: true });
  appendFileSync(path, line, { encoding: 'utf8' });
  return parsed;
}

/** Max PR number that has any record (processed or in-flight). 0 if none. */
export function maxRecordedPR(entries: StateEntry[]): number {
  let max = 0;
  for (const e of entries) if (e.pr > max) max = e.pr;
  return max;
}

/**
 * Given a set of candidate PR numbers (e.g. from `gh pr list`), return those
 * that are not yet terminal according to state.
 *
 * PRs that have only "started" (crash mid-cycle) are returned — they should
 * be retried. Terminal PRs are skipped.
 */
export function unprocessedPRs(candidatePRs: readonly number[], entries: StateEntry[]): number[] {
  const summary = summarize(entries);
  return candidatePRs.filter((pr) => {
    const s = summary.get(pr);
    return !s?.terminal;
  });
}

/** True if the given PR has reached a terminal state. */
export function isTerminal(pr: number, entries: StateEntry[]): boolean {
  return summarize(entries).get(pr)?.terminal ?? false;
}

export class StateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'StateError';
  }
}

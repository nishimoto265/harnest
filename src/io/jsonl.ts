import { appendFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import { dirname } from 'node:path';

import type { ZodType } from 'zod';

/**
 * Append a single entry as one line to a jsonl file.
 * The entry is validated against the schema first; if invalid, throws and
 * does NOT write (prevents corrupt lines from entering the log).
 *
 * Line atomicity: O_APPEND write of a JSON line < PIPE_BUF (4KB) is atomic
 * under POSIX. Entries must fit within that budget; large free-text fields
 * should be referenced by sha256, not inlined.
 */
export function appendJsonl<T>(path: string, entry: unknown, schema: ZodType<T>): T {
  const parsed = schema.parse(entry);
  const line = JSON.stringify(parsed) + '\n';
  if (Buffer.byteLength(line, 'utf8') >= 4096) {
    throw new JsonlWriteError(
      `entry > 4KB, may not be atomic under POSIX O_APPEND (${path}). ` +
        `Offload free-text to a sha-referenced side file.`,
    );
  }
  mkdirSync(dirname(path), { recursive: true });
  appendFileSync(path, line, { encoding: 'utf8' });
  return parsed;
}

/**
 * Read a jsonl file and parse every line against the schema.
 * Empty/blank lines are skipped. Missing file → empty array.
 * Invalid JSON or schema violation on any line throws (line number in error).
 */
export function readJsonl<T>(path: string, schema: ZodType<T>): T[] {
  if (!existsSync(path)) return [];
  const text = readFileSync(path, 'utf8');
  const out: T[] = [];
  let lineNum = 0;
  for (const rawLine of text.split('\n')) {
    lineNum++;
    const line = rawLine.trim();
    if (!line) continue;
    let obj: unknown;
    try {
      obj = JSON.parse(line);
    } catch (e) {
      throw new JsonlReadError(`invalid JSON at ${path}:${lineNum}: ${(e as Error).message}`);
    }
    const result = schema.safeParse(obj);
    if (!result.success) {
      const issues = result.error.issues
        .map((i) => `${i.path.join('.')}: ${i.message}`)
        .join('; ');
      throw new JsonlReadError(`schema violation at ${path}:${lineNum}: ${issues}`);
    }
    out.push(result.data);
  }
  return out;
}

export class JsonlReadError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'JsonlReadError';
  }
}

export class JsonlWriteError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'JsonlWriteError';
  }
}

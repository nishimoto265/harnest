import { existsSync, readFileSync } from 'node:fs';

import type { ZodType } from 'zod';

import { writeAtomic } from './atomic.ts';

/**
 * Write a JSON document atomically with pretty formatting.
 * Validates via the provided schema *before* writing to disk, so a
 * downstream consumer never sees a structurally-invalid payload.
 *
 * Throws if validation fails — the file is NOT written.
 */
export function writeJsonAtomic<T>(path: string, value: unknown, schema: ZodType<T>): T {
  const parsed = schema.parse(value);
  writeAtomic(path, JSON.stringify(parsed, null, 2) + '\n');
  return parsed;
}

/**
 * Read and schema-validate a JSON file.
 *
 * If `allowMissing` is true and the file does not exist, returns null.
 * Otherwise missing/invalid files throw `JsonReadError`.
 */
export function readJson<T>(
  path: string,
  schema: ZodType<T>,
  opts?: { allowMissing?: boolean },
): T | null {
  if (!existsSync(path)) {
    if (opts?.allowMissing) return null;
    throw new JsonReadError(`missing: ${path}`);
  }
  const raw = readFileSync(path, 'utf8');
  let obj: unknown;
  try {
    obj = JSON.parse(raw);
  } catch (e) {
    throw new JsonReadError(`invalid JSON at ${path}: ${(e as Error).message}`);
  }
  const result = schema.safeParse(obj);
  if (!result.success) {
    const issues = result.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`).join('; ');
    throw new JsonReadError(`schema violation in ${path}: ${issues}`);
  }
  return result.data;
}

export class JsonReadError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'JsonReadError';
  }
}

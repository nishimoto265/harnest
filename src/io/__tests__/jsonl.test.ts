import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { z } from 'zod';

import { appendJsonl, JsonlReadError, JsonlWriteError, readJsonl } from '../jsonl.ts';

const Schema = z.object({ id: z.number(), name: z.string() });

describe('jsonl helpers', () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'auto-improve-jsonl-'));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it('appendJsonl writes one line per entry', () => {
    const p = join(dir, 'log.jsonl');
    appendJsonl(p, { id: 1, name: 'a' }, Schema);
    appendJsonl(p, { id: 2, name: 'b' }, Schema);
    expect(readFileSync(p, 'utf8')).toBe('{"id":1,"name":"a"}\n{"id":2,"name":"b"}\n');
  });

  it('appendJsonl refuses entries that violate schema', () => {
    const p = join(dir, 'log.jsonl');
    expect(() => appendJsonl(p, { id: 'oops', name: 'a' }, Schema)).toThrow();
    // File should not exist — we refused before write
    const fs = require('node:fs');
    expect(fs.existsSync(p) && readFileSync(p, 'utf8').length > 0).toBe(false);
  });

  it('appendJsonl throws when entry > 4KB', () => {
    const p = join(dir, 'log.jsonl');
    const huge = { id: 1, name: 'x'.repeat(5000) };
    expect(() => appendJsonl(p, huge, Schema)).toThrow(JsonlWriteError);
  });

  it('readJsonl parses each non-blank line', () => {
    const p = join(dir, 'log.jsonl');
    writeFileSync(p, '{"id":1,"name":"a"}\n\n{"id":2,"name":"b"}\n');
    expect(readJsonl(p, Schema)).toEqual([
      { id: 1, name: 'a' },
      { id: 2, name: 'b' },
    ]);
  });

  it('readJsonl returns [] for missing file', () => {
    expect(readJsonl(join(dir, 'none.jsonl'), Schema)).toEqual([]);
  });

  it('readJsonl throws on invalid JSON with line number', () => {
    const p = join(dir, 'bad.jsonl');
    writeFileSync(p, '{"id":1,"name":"a"}\n{not-json\n');
    expect(() => readJsonl(p, Schema)).toThrow(JsonlReadError);
  });

  it('readJsonl throws on schema violation with line number', () => {
    const p = join(dir, 'bad.jsonl');
    writeFileSync(p, '{"id":"x","name":"a"}\n');
    expect(() => readJsonl(p, Schema)).toThrow(/schema violation/);
  });
});

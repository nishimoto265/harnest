import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { z } from 'zod';

import { JsonReadError, readJson, writeJsonAtomic } from '../json.ts';

const Schema = z.object({ a: z.string(), b: z.number() });

describe('json helpers', () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'auto-improve-json-'));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it('writeJsonAtomic validates + writes pretty JSON', () => {
    const p = join(dir, 'x.json');
    const r = writeJsonAtomic(p, { a: 'hi', b: 2 }, Schema);
    expect(r).toEqual({ a: 'hi', b: 2 });
    expect(existsSync(p)).toBe(true);
    expect(require('node:fs').readFileSync(p, 'utf8')).toMatch(/"a": "hi"/);
  });

  it('writeJsonAtomic throws on schema violation without writing', () => {
    const p = join(dir, 'x.json');
    expect(() => writeJsonAtomic(p, { a: 123, b: 2 }, Schema)).toThrow();
    expect(existsSync(p)).toBe(false);
  });

  it('readJson round-trips', () => {
    const p = join(dir, 'r.json');
    writeJsonAtomic(p, { a: 'k', b: 9 }, Schema);
    expect(readJson(p, Schema)).toEqual({ a: 'k', b: 9 });
  });

  it('readJson throws on missing (default)', () => {
    expect(() => readJson(join(dir, 'none.json'), Schema)).toThrow(JsonReadError);
  });

  it('readJson returns null with allowMissing', () => {
    expect(readJson(join(dir, 'none.json'), Schema, { allowMissing: true })).toBeNull();
  });

  it('readJson throws on invalid JSON', () => {
    const p = join(dir, 'bad.json');
    writeFileSync(p, 'nope');
    expect(() => readJson(p, Schema)).toThrow(/invalid JSON/);
  });

  it('readJson throws on schema violation', () => {
    const p = join(dir, 'bad.json');
    writeFileSync(p, '{"a":1,"b":"x"}');
    expect(() => readJson(p, Schema)).toThrow(/schema violation/);
  });
});

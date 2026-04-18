import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { writeAtomic } from '../atomic.ts';

describe('writeAtomic', () => {
  let dir: string;

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'auto-improve-atomic-'));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it('writes file content and leaves no tmp behind', () => {
    const p = join(dir, 'sub', 'out.json');
    writeAtomic(p, '{"a":1}');
    expect(existsSync(p)).toBe(true);
    expect(readFileSync(p, 'utf8')).toBe('{"a":1}');
    const entries = require('node:fs').readdirSync(join(dir, 'sub'));
    expect(entries.filter((e: string) => e.includes('.tmp-'))).toEqual([]);
  });

  it('overwrites existing file', () => {
    const p = join(dir, 'x.txt');
    writeAtomic(p, 'first');
    writeAtomic(p, 'second');
    expect(readFileSync(p, 'utf8')).toBe('second');
  });

  it('creates parent directories', () => {
    const p = join(dir, 'a', 'b', 'c', 'deep.txt');
    writeAtomic(p, 'hi');
    expect(existsSync(p)).toBe(true);
  });
});

import { describe, expect, it } from 'vitest';

import { expandWorktreeName, makeRunId, resolvePath, runDir } from '../paths.ts';

describe('paths.ts', () => {
  it('resolvePath: absolute paths pass through', () => {
    expect(resolvePath('/etc/hosts', '/repo')).toBe('/etc/hosts');
  });

  it('resolvePath: relative paths resolve against repoRoot', () => {
    expect(resolvePath('foo/bar', '/repo')).toBe('/repo/foo/bar');
  });

  it('resolvePath: ~ expands to HOME', () => {
    const home = process.env.HOME ?? '/tmp';
    expect(resolvePath('~/stuff', '/repo')).toBe(`${home}/stuff`);
    expect(resolvePath('~stuff', '/repo')).toBe(`${home}/stuff`);
  });

  it('runDir: formats YYYY-MM-DD-PR<num>', () => {
    const d = new Date('2026-04-17T15:00:00Z');
    expect(runDir('/runs', 74, d)).toBe('/runs/2026-04-17-PR74');
  });

  it('makeRunId: stable format', () => {
    const d = new Date('2026-04-17T15:00:00.123Z');
    expect(makeRunId(74, d)).toBe('pr74-20260417T150000Z');
  });

  it('expandWorktreeName: substitutes template variables', () => {
    expect(
      expandWorktreeName('wt-pr{pr}-{pass}-{agent}', {
        pr: 74,
        pass: 'pass1',
        agent: 'a1',
      }),
    ).toBe('wt-pr74-pass1-a1');
  });
});

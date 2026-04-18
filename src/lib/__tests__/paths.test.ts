import { describe, expect, it } from 'vitest';

import { expandWorktreeName, resolvePath } from '../paths.ts';

// NOTE: runDir / makeRunId were moved to src/io/run-layout.ts (newRunId, runDir).
// See src/io/__tests__/run-layout.test.ts for the new coverage.

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

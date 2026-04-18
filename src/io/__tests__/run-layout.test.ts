import { describe, expect, it } from 'vitest';

import * as L from '../run-layout.ts';

const ctx: L.RunContext = {
  runsBase: '/tmp/runs',
  worktreeBase: '/tmp/wt',
  pr: 74,
  runId: '2026-04-18-PR74-abc1234',
};

describe('run-layout', () => {
  it('newRunId includes date + pr + short hash', () => {
    const id = L.newRunId(42, new Date('2026-05-01T10:00:00Z'));
    expect(id).toMatch(/^2026-05-01-PR42-[a-f0-9]{7}$/);
  });

  it('runDir / taskPackagePath / base shaPath', () => {
    expect(L.runDir(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234');
    expect(L.taskPackagePath(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234/task-package.json');
    expect(L.baseShaPath(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234/base.sha');
  });

  it('agentOutputDir uses pass-specific prefix', () => {
    expect(L.agentOutputDir(ctx, 'A', 'a1')).toBe('/tmp/runs/2026-04-18-PR74-abc1234/20-pass1/a1');
    expect(L.agentOutputDir(ctx, 'B', 'a2')).toBe('/tmp/runs/2026-04-18-PR74-abc1234/50-pass2/a2');
  });

  it('manifest / session / diff / checklist paths', () => {
    expect(L.manifestPath(ctx, 'A', 'a1')).toMatch(/20-pass1\/a1\/manifest\.json$/);
    expect(L.sessionJsonlPath(ctx, 'B', 'a3')).toMatch(/50-pass2\/a3\/session\.jsonl$/);
    expect(L.diffPatchPath(ctx, 'A', 'a1')).toMatch(/20-pass1\/a1\/diff\.patch$/);
    expect(L.checklistResultPath(ctx, 'B', 'a1')).toMatch(
      /50-pass2\/a1\/checklist-result\.json$/,
    );
  });

  it('score / compliance / pairwise paths', () => {
    expect(L.scoreJsonlPath(ctx, 'A')).toMatch(/\/30\/scores-A\.jsonl$/);
    expect(L.scoreJsonlPath(ctx, 'B')).toMatch(/\/60\/scores-B\.jsonl$/);
    expect(L.complianceJsonlPath(ctx, 'A')).toMatch(/\/30\/compliance-A\.jsonl$/);
    expect(L.pairwiseJsonlPath(ctx)).toMatch(/\/60\/pairwise\.jsonl$/);
  });

  it('candidates / decision paths', () => {
    expect(L.candidatesPath(ctx)).toMatch(/\/40\/candidates\.json$/);
    expect(L.classificationJsonlPath(ctx)).toMatch(/\/40\/classification\.jsonl$/);
    expect(L.decisionPath(ctx)).toMatch(/\/70\/decision\.json$/);
  });

  it('worktree paths are outside run dir', () => {
    expect(L.pass1WorktreePath(ctx, 'a1')).toBe('/tmp/wt/2026-04-18-PR74-abc1234-pass1-a1');
    expect(L.pass2WorktreePath(ctx, 'a2')).toBe('/tmp/wt/2026-04-18-PR74-abc1234-pass2-a2');
  });

  it('cross-run artifact paths live at runsBase', () => {
    expect(L.processedJsonlPath(ctx.runsBase)).toBe('/tmp/runs/processed.jsonl');
    expect(L.rulesRegistryJsonlPath(ctx.runsBase)).toBe('/tmp/runs/rules-registry.jsonl');
  });

  it('standardAgentIds returns a{1..n}', () => {
    expect(L.standardAgentIds(3)).toEqual(['a1', 'a2', 'a3']);
    expect(() => L.standardAgentIds(0)).toThrow();
  });
});

import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import type { Manifest, TaskPackage } from '../../contracts.ts';
import * as L from '../run-layout.ts';

const ctx: L.RunContext = {
  runsBase: '/tmp/runs',
  worktreeBase: '/tmp/wt',
  pr: 74,
  runId: '2026-04-18-PR74-abc1234',
};

const ISO = '2026-04-18T10:00:00Z';
const SHA40 = 'a'.repeat(40);
const SHA256 = 'b'.repeat(64);

describe('run-layout: id generation and validation', () => {
  it('newRunId includes date + pr + short hash and passes RunIdSchema', () => {
    const id = L.newRunId(42, new Date('2026-05-01T10:00:00Z'));
    expect(id).toMatch(/^2026-05-01-PR42-[a-f0-9]+$/);
  });

  it('newRunId rejects invalid pr', () => {
    expect(() => L.newRunId(0)).toThrow();
    expect(() => L.newRunId(-1)).toThrow();
    expect(() => L.newRunId(1.5)).toThrow();
  });

  it('makeRunContext rejects invalid runId format', () => {
    expect(() =>
      L.makeRunContext({ ...ctx, runId: 'pr74-20260417T150000Z' }),
    ).toThrow(L.RunLayoutError);
    expect(() => L.makeRunContext({ ...ctx, runId: '' })).toThrow();
  });

  it('assertSafeSegment blocks traversal & separators', () => {
    expect(() => L.assertSafeSegment('..', 'runId')).toThrow();
    expect(() => L.assertSafeSegment('../foo', 'runId')).toThrow();
    expect(() => L.assertSafeSegment('foo/bar', 'runId')).toThrow();
    expect(() => L.assertSafeSegment('foo\0bar', 'runId')).toThrow();
    expect(() => L.assertSafeSegment('', 'runId')).toThrow();
  });

  it('assertAgentId enforces strict form', () => {
    L.assertAgentId('a1');
    L.assertAgentId('a7');
    expect(() => L.assertAgentId('../a1')).toThrow();
    expect(() => L.assertAgentId('A1')).toThrow();
    expect(() => L.assertAgentId('a0')).toThrow();
  });
});

describe('run-layout: path helpers', () => {
  it('runDir / taskPackagePath / baseShaPath', () => {
    expect(L.runDir(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234');
    expect(L.taskPackagePath(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234/task-package.json');
    expect(L.baseShaPath(ctx)).toBe('/tmp/runs/2026-04-18-PR74-abc1234/base.sha');
  });

  it('agentOutputDir uses pass-specific prefix', () => {
    expect(L.agentOutputDir(ctx, 'A', 'a1')).toBe('/tmp/runs/2026-04-18-PR74-abc1234/20-pass1/a1');
    expect(L.agentOutputDir(ctx, 'B', 'a2')).toBe('/tmp/runs/2026-04-18-PR74-abc1234/50-pass2/a2');
  });

  it('agentOutputDir rejects bad agent id', () => {
    expect(() => L.agentOutputDir(ctx, 'A', '../xx')).toThrow();
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
    expect(() => L.standardAgentIds(10)).toThrow();
  });
});

describe('run-layout: RunContext from TaskPackage', () => {
  it('reconstructs a consistent RunContext', () => {
    const pkg: TaskPackage = {
      run_id: ctx.runId,
      pr: ctx.pr,
      title: 't',
      body: 'b',
      base_ref: 'develop',
      base_sha: SHA40,
      head_ref: 'feature/x',
      linked_issue_numbers: [],
      target_files_hint: [],
      acceptance_hints: [],
      reconstructed_task_prompt: 'do this',
      agent_ids: ['a1'],
      worktrees: [
        {
          agent: 'a1',
          pass: 'A',
          path: '/tmp/wt/x',
          branch: 'feature/x',
          base_sha: SHA40,
          head_sha: SHA40,
        },
      ],
      built_at: ISO,
    };
    const reconstructed = L.runContextFromTaskPackage(pkg, {
      runsBase: ctx.runsBase,
      worktreeBase: ctx.worktreeBase,
    });
    expect(reconstructed.runId).toBe(ctx.runId);
    expect(reconstructed.pr).toBe(ctx.pr);
  });
});

describe('run-layout: manifest finalization', () => {
  let dir: string;
  let runCtx: L.RunContext;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'auto-improve-rl-'));
    runCtx = L.makeRunContext({
      runsBase: dir,
      worktreeBase: dir,
      pr: 74,
      runId: ctx.runId,
    });
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  function writeManifest(agent: string, pass: 'A' | 'B', body: unknown) {
    const p = L.manifestPath(runCtx, pass, agent);
    const fs = require('node:fs');
    fs.mkdirSync(require('node:path').dirname(p), { recursive: true });
    writeFileSync(p, JSON.stringify(body), 'utf8');
    return p;
  }

  it('returns null when manifest missing', () => {
    expect(L.loadFinalizedManifest(runCtx, 'A', 'a1')).toBeNull();
    expect(L.isManifestFinalized(runCtx, 'A', 'a1')).toBe(false);
  });

  it('parses and validates a success manifest', () => {
    const m: Manifest = {
      run_id: runCtx.runId,
      pr: 74,
      agent: 'a1',
      pass: 'A',
      started_at: ISO,
      completed_at: ISO,
      worktree: '/tmp/wt-x',
      session_jsonl_sha256: SHA256,
      diff_sha256: SHA256,
      checklist_result_sha256: SHA256,
      checklist_version: 'v1',
      bytes_written: 100,
      exit_status: 'success',
      exit_code: 0,
      head_sha: SHA40,
    };
    writeManifest('a1', 'A', m);
    const loaded = L.loadFinalizedManifest(runCtx, 'A', 'a1');
    expect(loaded?.exit_status).toBe('success');
    expect(L.isManifestFinalized(runCtx, 'A', 'a1')).toBe(true);
  });

  it('throws when run_id mismatches (well-formed but different)', () => {
    writeManifest('a1', 'A', {
      run_id: '2026-04-18-PR99-deadbee',
      pr: 74,
      agent: 'a1',
      pass: 'A',
      started_at: ISO,
      completed_at: ISO,
      worktree: '/tmp/wt-x',
      session_jsonl_sha256: SHA256,
      diff_sha256: SHA256,
      checklist_result_sha256: null,
      checklist_version: 'v1',
      bytes_written: 0,
      exit_status: 'timeout',
      exit_code: null,
      head_sha: null,
    });
    expect(() => L.loadFinalizedManifest(runCtx, 'A', 'a1')).toThrow(L.RunLayoutError);
  });

  it('throws when manifest is corrupt JSON', () => {
    const p = L.manifestPath(runCtx, 'A', 'a1');
    const fs = require('node:fs');
    fs.mkdirSync(require('node:path').dirname(p), { recursive: true });
    writeFileSync(p, 'not-json', 'utf8');
    expect(() => L.loadFinalizedManifest(runCtx, 'A', 'a1')).toThrow();
  });
});

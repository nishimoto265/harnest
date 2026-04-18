import { createHash } from 'node:crypto';
import { join } from 'node:path';

/**
 * Canonical run directory layout.
 *
 * Every step MUST obtain its paths from this module. Never concatenate path
 * fragments inline in step code. This keeps the on-disk layout a single
 * source of truth and lets us relocate directories without touching steps.
 *
 * See docs/design/io-contracts.md for the tree diagram.
 */

export interface RunContext {
  /** configured runs root (absolute) */
  runsBase: string;
  /** configured worktree root (absolute) */
  worktreeBase: string;
  /** PR number */
  pr: number;
  /** run id — stable across step invocations within one cycle */
  runId: string;
}

export type StepNumber = 10 | 20 | 30 | 40 | 50 | 60 | 70;
export type Pass = 'A' | 'B';

/** Generate a new run id (ISO date + short hash of pr + random). */
export function newRunId(pr: number, now: Date = new Date()): string {
  const date = now.toISOString().slice(0, 10); // YYYY-MM-DD
  const h = createHash('sha256')
    .update(`${pr}-${now.getTime()}-${Math.random()}`)
    .digest('hex')
    .slice(0, 7);
  return `${date}-PR${pr}-${h}`;
}

/** Path to the run root directory. */
export function runDir(ctx: RunContext): string {
  return join(ctx.runsBase, ctx.runId);
}

export function configSnapshotPath(ctx: RunContext): string {
  return join(runDir(ctx), 'config.snapshot.yaml');
}

export function taskPackagePath(ctx: RunContext): string {
  return join(runDir(ctx), 'task-package.json');
}

export function baseShaPath(ctx: RunContext): string {
  return join(runDir(ctx), 'base.sha');
}

/** Directory for an agent's output within a given pass. */
export function agentOutputDir(ctx: RunContext, pass: Pass, agent: string): string {
  const stepPrefix = pass === 'A' ? '20-pass1' : '50-pass2';
  return join(runDir(ctx), stepPrefix, agent);
}

export function manifestPath(ctx: RunContext, pass: Pass, agent: string): string {
  return join(agentOutputDir(ctx, pass, agent), 'manifest.json');
}

export function sessionJsonlPath(ctx: RunContext, pass: Pass, agent: string): string {
  return join(agentOutputDir(ctx, pass, agent), 'session.jsonl');
}

export function diffPatchPath(ctx: RunContext, pass: Pass, agent: string): string {
  return join(agentOutputDir(ctx, pass, agent), 'diff.patch');
}

export function checklistResultPath(ctx: RunContext, pass: Pass, agent: string): string {
  return join(agentOutputDir(ctx, pass, agent), 'checklist-result.json');
}

/** Step 30 / 60 scoring outputs. */
export function scoreJsonlPath(ctx: RunContext, pass: Pass): string {
  return pass === 'A'
    ? join(runDir(ctx), '30', 'scores-A.jsonl')
    : join(runDir(ctx), '60', 'scores-B.jsonl');
}

export function complianceJsonlPath(ctx: RunContext, pass: Pass): string {
  return pass === 'A'
    ? join(runDir(ctx), '30', 'compliance-A.jsonl')
    : join(runDir(ctx), '60', 'compliance-B.jsonl');
}

export function pairwiseJsonlPath(ctx: RunContext): string {
  return join(runDir(ctx), '60', 'pairwise.jsonl');
}

export function judgeRawDir(ctx: RunContext, pass: Pass): string {
  return pass === 'A' ? join(runDir(ctx), '30', 'raw') : join(runDir(ctx), '60', 'raw');
}

/** Step 40 candidate extraction output. */
export function candidatesPath(ctx: RunContext): string {
  return join(runDir(ctx), '40', 'candidates.json');
}

export function classificationJsonlPath(ctx: RunContext): string {
  return join(runDir(ctx), '40', 'classification.jsonl');
}

/** Step 70 decision output. */
export function decisionPath(ctx: RunContext): string {
  return join(runDir(ctx), '70', 'decision.json');
}

/**
 * Worktree paths are OUTSIDE the run dir to isolate ephemeral build state
 * from durable artifacts. `worktree.base` is configured separately.
 */
export function pass1WorktreePath(ctx: RunContext, agent: string): string {
  return join(ctx.worktreeBase, `${ctx.runId}-pass1-${agent}`);
}

export function pass2WorktreePath(ctx: RunContext, agent: string): string {
  return join(ctx.worktreeBase, `${ctx.runId}-pass2-${agent}`);
}

/** Cross-run artifacts (outside any run dir). */
export function processedJsonlPath(runsBase: string): string {
  return join(runsBase, 'processed.jsonl');
}

export function rulesRegistryJsonlPath(runsBase: string): string {
  return join(runsBase, 'rules-registry.jsonl');
}

/**
 * Check: does the agent have a finalized manifest?
 * Callers should use this (not session.jsonl existence) to decide whether
 * the agent's output is safe to score. Codex H5.
 */
export function isManifestFinalized(
  _ctx: RunContext,
  _pass: Pass,
  _agent: string,
  existsFn: (p: string) => boolean,
  path: string,
): boolean {
  return existsFn(path);
}

/** List of canonical agent ids for a fixed-size implementer pool. */
export function standardAgentIds(count: number): string[] {
  if (count < 1) throw new RangeError('agent count must be ≥ 1');
  return Array.from({ length: count }, (_, i) => `a${i + 1}`);
}

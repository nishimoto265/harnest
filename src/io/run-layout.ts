import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

import { AgentIdSchema, ManifestSchema, RunIdSchema, type TaskPackage } from '../contracts.ts';
import { readJson } from './json.ts';

/**
 * Canonical run directory layout. One source of truth.
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

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

/**
 * Build a RunContext with strict validation so no caller can slip in
 * traversal (`..`, `/`, `\0`) segments that would escape the runs/worktree
 * roots.
 */
export function makeRunContext(ctx: RunContext): RunContext {
  const parsed = RunIdSchema.safeParse(ctx.runId);
  if (!parsed.success) {
    throw new RunLayoutError(`invalid run_id: ${ctx.runId} (${parsed.error.issues[0]?.message})`);
  }
  assertSafeSegment(ctx.runId, 'runId');
  return { ...ctx };
}

/**
 * Reject any path segment that could traverse or inject separators.
 * Accepts `[A-Za-z0-9._-]` (plus hyphen). Blocks `/`, `\`, `..`, NUL, control.
 */
export function assertSafeSegment(segment: string, label: string): void {
  if (segment.length === 0) throw new RunLayoutError(`${label} is empty`);
  if (segment.length > 128) throw new RunLayoutError(`${label} too long (>128)`);
  if (!/^[A-Za-z0-9._-]+$/.test(segment)) {
    throw new RunLayoutError(`${label} has invalid chars: ${JSON.stringify(segment)}`);
  }
  if (segment === '.' || segment === '..') {
    throw new RunLayoutError(`${label} cannot be relative ref: ${segment}`);
  }
}

/** Throws if the agent id is not in the strict `a<positive int>` form. */
export function assertAgentId(agent: string): void {
  const r = AgentIdSchema.safeParse(agent);
  if (!r.success) throw new RunLayoutError(`invalid agent id: ${JSON.stringify(agent)}`);
  // agent ids are already a safe subset of runId but double-check segment safety:
  assertSafeSegment(agent, 'agent');
}

// ---------------------------------------------------------------------------
// Run id
// ---------------------------------------------------------------------------

/** Generate a new run id (YYYY-MM-DD + PR + short hash). */
export function newRunId(pr: number, now: Date = new Date()): string {
  if (!Number.isInteger(pr) || pr <= 0) {
    throw new RunLayoutError(`pr must be a positive integer, got ${pr}`);
  }
  const date = now.toISOString().slice(0, 10); // YYYY-MM-DD
  const h = createHash('sha256')
    .update(`${pr}-${now.getTime()}-${Math.random()}`)
    .digest('hex')
    .slice(0, 7);
  const id = `${date}-PR${pr}-${h}`;
  // Validate against schema so we catch future format drift.
  RunIdSchema.parse(id);
  return id;
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

export function runDir(ctx: RunContext): string {
  const v = makeRunContext(ctx);
  return join(v.runsBase, v.runId);
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
  assertAgentId(agent);
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
 *
 * NOTE: The actual worktree metadata (branch, base_sha, exact path) is
 * written into `task-package.json.worktrees[]` by step 10 — that is the
 * authoritative record for cleanup/restore. These helpers produce the
 * *canonical* path; step 10 MUST honor them when creating worktrees so the
 * two views stay consistent.
 */
export function pass1WorktreePath(ctx: RunContext, agent: string): string {
  assertAgentId(agent);
  const v = makeRunContext(ctx);
  return join(v.worktreeBase, `${v.runId}-pass1-${agent}`);
}

export function pass2WorktreePath(ctx: RunContext, agent: string): string {
  assertAgentId(agent);
  const v = makeRunContext(ctx);
  return join(v.worktreeBase, `${v.runId}-pass2-${agent}`);
}

/** Cross-run artifacts (outside any run dir). */
export function processedJsonlPath(runsBase: string): string {
  return join(runsBase, 'processed.jsonl');
}

export function rulesRegistryJsonlPath(runsBase: string): string {
  return join(runsBase, 'rules-registry.jsonl');
}

// ---------------------------------------------------------------------------
// Manifest finalization
// ---------------------------------------------------------------------------

/**
 * Returns the validated Manifest if the agent's manifest file exists AND
 * parses cleanly AND the identity fields match the expected run/pass/agent.
 * Returns null if the manifest is missing.
 * Throws RunLayoutError if the manifest exists but is corrupt or mismatched.
 *
 * step 30/60 MUST use this (not a raw file-existence check) to decide whether
 * an agent's output is safe to score (Codex H10).
 */
export function loadFinalizedManifest(
  ctx: RunContext,
  pass: Pass,
  agent: string,
): import('../contracts.ts').Manifest | null {
  const path = manifestPath(ctx, pass, agent);
  if (!existsSync(path)) return null;
  const manifest = readJson(path, ManifestSchema);
  if (!manifest) return null;
  if (manifest.run_id !== ctx.runId) {
    throw new RunLayoutError(
      `manifest ${path} has run_id=${manifest.run_id}, expected ${ctx.runId}`,
    );
  }
  if (manifest.pass !== pass) {
    throw new RunLayoutError(`manifest ${path} has pass=${manifest.pass}, expected ${pass}`);
  }
  if (manifest.agent !== agent) {
    throw new RunLayoutError(`manifest ${path} has agent=${manifest.agent}, expected ${agent}`);
  }
  return manifest;
}

/** Simple boolean wrapper for loadFinalizedManifest. Callers that need the
 * manifest contents should call `loadFinalizedManifest` directly. */
export function isManifestFinalized(ctx: RunContext, pass: Pass, agent: string): boolean {
  return loadFinalizedManifest(ctx, pass, agent) !== null;
}

// ---------------------------------------------------------------------------
// RunContext construction
// ---------------------------------------------------------------------------

export interface RunContextSources {
  runsBase: string;
  worktreeBase: string;
}

/**
 * Build a RunContext from a parsed TaskPackage plus configured bases.
 * Use this in step 30/40/50/60/70 so every step reconstructs the same shape.
 */
export function runContextFromTaskPackage(
  taskPackage: TaskPackage,
  bases: RunContextSources,
): RunContext {
  return makeRunContext({
    runsBase: bases.runsBase,
    worktreeBase: bases.worktreeBase,
    pr: taskPackage.pr,
    runId: taskPackage.run_id,
  });
}

// ---------------------------------------------------------------------------
// Agent id pool
// ---------------------------------------------------------------------------

/** List of canonical agent ids for a fixed-size implementer pool. */
export function standardAgentIds(count: number): string[] {
  if (!Number.isInteger(count) || count < 1) {
    throw new RangeError('agent count must be a positive integer');
  }
  if (count > 9) throw new RangeError('agent count >9 not supported by AgentIdSchema');
  return Array.from({ length: count }, (_, i) => `a${i + 1}`);
}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

export class RunLayoutError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'RunLayoutError';
  }
}

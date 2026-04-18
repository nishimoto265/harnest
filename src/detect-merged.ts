import { execFileSync } from 'node:child_process';

import { z } from 'zod';

import { loadConfig, type ResolvedConfig } from './lib/config.ts';
import { createLogger } from './lib/logger.ts';
import { readState, type StateEntry, summarize, unprocessedPRs } from './lib/state.ts';
import { runPreflight } from './preflight.ts';

const GhPRSchema = z.object({
  number: z.number().int().positive(),
  title: z.string(),
  mergedAt: z.string().datetime({ offset: true }).nullable().optional(),
  baseRefName: z.string(),
  headRefName: z.string(),
  body: z.string().optional().default(''),
});
export type GhPR = z.infer<typeof GhPRSchema>;

export interface DetectResult {
  generated_at: string;
  repo: string;
  since: string;
  /** How `since` was derived — useful for debugging stale-window issues. */
  since_source: 'config.since' | 'lookback_days' | 'state_aware' | 'max_lookback_clamp';
  total_merged: number;
  already_processed: number;
  prs: GhPR[];
}

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10); // YYYY-MM-DD
}

function daysAgo(days: number, now: Date): Date {
  return new Date(now.getTime() - days * 86_400_000);
}

/**
 * Pick the oldest `mergedAt` (or `at` fallback) among non-terminal state entries.
 * Non-terminal here = any PR that does not have a terminal event yet, meaning it
 * should still be in scope when we query gh.
 *
 * Returns null if no non-terminal entries exist (fresh install, or all done).
 */
function oldestNonTerminalMergedAt(entries: StateEntry[]): string | null {
  const summary = summarize(entries);
  let oldest: string | null = null;
  for (const s of summary.values()) {
    if (s.terminal) continue;
    // Prefer the `at` of the first (earliest) event — good proxy for "when did this enter state".
    // If a future event adds mergedAt explicitly we can use it; for M1, `at` is close enough.
    const firstAt = entries.find((e) => e.pr === s.pr)?.at;
    if (!firstAt) continue;
    if (oldest === null || firstAt < oldest) oldest = firstAt;
  }
  return oldest;
}

/**
 * Resolve the `since` date for `gh pr list --search "merged:>=YYYY-MM-DD"`.
 *
 * Precedence:
 *   1. `detect.since` (absolute) — always wins when set
 *   2. min(`lookback_days` ago, oldest-non-terminal-PR `at` from state)
 *      — automatically widens when the pipeline has been down longer than lookback_days
 *   3. Clamped by `max_lookback_days` (safety rail against degenerate state)
 */
export function resolveSince(
  cfg: ResolvedConfig,
  entries: StateEntry[],
  now = new Date(),
): { since: string; source: DetectResult['since_source'] } {
  const det = cfg.raw.detect;

  // (1) absolute override
  if (det.since) {
    return { since: det.since, source: 'config.since' };
  }

  // (2) lookback_days
  const lookbackFloor = daysAgo(det.lookback_days, now);
  const maxFloor = daysAgo(det.max_lookback_days, now);

  // (3) state-aware widening — find oldest non-terminal entry
  const oldestNT = oldestNonTerminalMergedAt(entries);
  let chosen: Date = lookbackFloor;
  let source: DetectResult['since_source'] = 'lookback_days';

  if (oldestNT) {
    const ntDate = new Date(oldestNT);
    if (!Number.isNaN(ntDate.getTime()) && ntDate < lookbackFloor) {
      chosen = ntDate;
      source = 'state_aware';
    }
  }

  // Clamp against max_lookback_days
  if (chosen < maxFloor) {
    chosen = maxFloor;
    source = 'max_lookback_clamp';
  }

  return { since: isoDate(chosen), source };
}

function runGhPrList(cfg: ResolvedConfig, since: string): GhPR[] {
  const args = [
    'pr',
    'list',
    '--repo',
    cfg.raw.repo.github,
    '--state',
    'merged',
    '--limit',
    String(cfg.raw.detect.limit),
    '--search',
    `merged:>=${since}`,
    '--json',
    'number,title,mergedAt,baseRefName,headRefName,body',
  ];
  let stdout: string;
  try {
    stdout = execFileSync('gh', args, {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 60_000,
      maxBuffer: 32 * 1024 * 1024,
    });
  } catch (e) {
    const err = e as { stderr?: Buffer | string; message?: string };
    const stderr =
      typeof err.stderr === 'string' ? err.stderr : err.stderr ? err.stderr.toString() : '';
    throw new Error(`gh pr list failed: ${err.message ?? 'unknown'}${stderr ? `\n${stderr}` : ''}`);
  }

  let json: unknown;
  try {
    json = JSON.parse(stdout);
  } catch (e) {
    throw new Error(
      `gh returned non-JSON: ${(e as Error).message}\n--- stdout head ---\n${stdout.slice(0, 400)}`,
    );
  }
  const parsed = z.array(GhPRSchema).safeParse(json);
  if (!parsed.success) {
    throw new Error(`gh pr list JSON does not match expected schema: ${parsed.error.message}`);
  }
  return parsed.data;
}

/** Compute unprocessed merged PRs for a given config. */
export async function detectMerged(cfg: ResolvedConfig): Promise<DetectResult> {
  const entries = readState(cfg.abs.stateFile);
  const { since, source } = resolveSince(cfg, entries);
  const all = runGhPrList(cfg, since);

  const summary = summarize(entries);

  // Filter: drop PRs ≤ start_after_pr, drop terminal PRs.
  const startAfter = cfg.raw.detect.start_after_pr;
  const candidate = all.filter((p) => p.number > startAfter);
  const unprocessedNums = new Set(
    unprocessedPRs(
      candidate.map((p) => p.number),
      entries,
    ),
  );

  const prs = candidate
    .filter((p) => unprocessedNums.has(p.number))
    // Oldest merged first → FIFO queue; newer PRs benefit from earlier adoptions.
    .sort((a, b) => {
      const am = a.mergedAt ?? '';
      const bm = b.mergedAt ?? '';
      if (am && bm) return am.localeCompare(bm);
      return a.number - b.number;
    });

  // `already_processed` = fetched PRs that have a terminal entry
  const alreadyProcessed = candidate.filter((p) => summary.get(p.number)?.terminal).length;

  return {
    generated_at: new Date().toISOString(),
    repo: cfg.raw.repo.github,
    since,
    since_source: source,
    total_merged: all.length,
    already_processed: alreadyProcessed,
    prs,
  };
}

/**
 * Names of preflight checks that are "critical" for detect-merged.
 * Missing any of these means we cannot safely fetch/interpret merged PRs.
 * Non-critical NGs (e.g. missing claude/codex CLIs) are tolerated here — those
 * bite later in the pipeline, not at detection time.
 */
const CRITICAL_PREFLIGHT_CHECKS: ReadonlySet<string> = new Set([
  'node',
  'git',
  'gh',
  'gh auth',
  'jq',
  'yq',
  'config.yaml',
  'repo_root',
  'package.json',
  'pnpm install',
]);

/** CLI entry: prints JSON to stdout, returns exit code. */
export async function detectMergedCLI(argv: readonly string[] = []): Promise<number> {
  const log = createLogger({ name: 'detect-merged' });
  const skipPreflight = argv.includes('--skip-preflight');

  // Preflight hard gate — critical checks must pass (plan §M4).
  // Non-critical warnings (e.g. claude/codex CLI missing) are tolerated here.
  if (!skipPreflight) {
    try {
      const report = await runPreflight();
      const blocking = report.checks.filter(
        (c) => c.status === 'ng' && CRITICAL_PREFLIGHT_CHECKS.has(c.name),
      );
      if (blocking.length > 0) {
        for (const c of blocking) {
          log.error(`preflight NG: ${c.name}: ${c.detail ?? ''}`);
          if (c.remedy) log.error(`  → ${c.remedy}`);
        }
        log.error(
          `detect-merged blocked by ${blocking.length} critical preflight failure(s). ` +
            `Fix and re-run, or pass --skip-preflight for development.`,
        );
        return 2;
      }
    } catch (e) {
      log.error(`preflight crashed: ${(e as Error).message}`);
      return 2;
    }
  }

  let cfg: ResolvedConfig;
  try {
    cfg = loadConfig();
  } catch (e) {
    log.error((e as Error).message);
    return 2;
  }
  let result: DetectResult;
  try {
    result = await detectMerged(cfg);
  } catch (e) {
    log.error((e as Error).message);
    return 1;
  }
  // JSON goes to stdout (machine-readable); logs go to stderr via pino default.
  process.stdout.write(JSON.stringify(result, null, 2) + '\n');
  log.info(
    `found ${result.prs.length} unprocessed PR(s) (of ${result.total_merged} merged since ${result.since} [${result.since_source}]; ${result.already_processed} already processed)`,
  );
  return 0;
}

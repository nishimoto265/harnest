import { execSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * Path resolution helpers.
 *
 * The pipeline has three anchor directories:
 *   - repoRoot  : git top-level (auto-detected, overridable via AUTO_IMPROVE_REPO_ROOT)
 *   - pkgRoot   : auto-improve/ itself (parent of src/)
 *   - runsRoot  : where per-PR run dirs live (typically <repoRoot>/auto-improve/runs)
 *
 * All user-supplied paths from config.yaml are resolved against repoRoot.
 */

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

/** auto-improve/ package root (parent of src/). */
export const pkgRoot: string = resolve(__dirname, '..', '..');

/**
 * Detect the git repository root.
 *
 * Order:
 *   1. AUTO_IMPROVE_REPO_ROOT env var (for tests and unusual layouts)
 *   2. `git rev-parse --show-toplevel` from cwd
 *   3. fall back to pkgRoot's parent (assumes auto-improve/ is a top-level dir)
 *
 * Throws if none of the candidates exist.
 */
export function detectRepoRoot(cwd: string = process.cwd()): string {
  const fromEnv = process.env.AUTO_IMPROVE_REPO_ROOT;
  if (fromEnv && existsSync(fromEnv)) {
    return resolve(fromEnv);
  }
  try {
    const out = execSync('git rev-parse --show-toplevel', {
      cwd,
      stdio: ['ignore', 'pipe', 'ignore'],
      encoding: 'utf8',
    }).trim();
    if (out && existsSync(out)) return out;
  } catch {
    // fall through
  }
  const fallback = resolve(pkgRoot, '..');
  if (existsSync(join(fallback, '.git'))) return fallback;
  throw new Error(
    `Cannot determine repository root. Set AUTO_IMPROVE_REPO_ROOT or run inside a git repo.`,
  );
}

/**
 * Resolve a config-provided path into an absolute path.
 *
 * Rules:
 *   - absolute path → returned as-is
 *   - starts with "~" → expanded against $HOME
 *   - otherwise → resolved relative to repoRoot
 *
 * Trailing slashes are preserved (some callers rely on the "/" to signal a dir).
 */
export function resolvePath(input: string, repoRoot: string): string {
  if (!input) return input;
  if (isAbsolute(input)) return input;
  if (input.startsWith('~')) {
    return resolve(homedir(), input.slice(input.startsWith('~/') ? 2 : 1));
  }
  return resolve(repoRoot, input);
}

/**
 * Build a run directory path for a given PR.
 * Format: <runsRoot>/<YYYY-MM-DD>-PR<num>/
 *
 * Accepts an optional `date` for deterministic tests.
 */
export function runDir(runsRoot: string, prNumber: number, date: Date = new Date()): string {
  const iso = date.toISOString().slice(0, 10); // YYYY-MM-DD (UTC; stable across machines)
  return join(runsRoot, `${iso}-PR${prNumber}`);
}

/**
 * Build a stable run_id for log/state correlation. Collision-resistant for the
 * expected volume (<1 per PR per hour). Not cryptographic.
 */
export function makeRunId(prNumber: number, date: Date = new Date()): string {
  const ts = date
    .toISOString()
    .replace(/[-:]/g, '')
    .replace(/\.\d{3}Z$/, 'Z');
  return `pr${prNumber}-${ts}`;
}

/**
 * Expand worktree naming template. See config.worktree.naming.
 * Known variables: {pr} {pass} {agent}
 */
export function expandWorktreeName(
  template: string,
  vars: { pr: number | string; pass: string; agent: string },
): string {
  return template
    .replaceAll('{pr}', String(vars.pr))
    .replaceAll('{pass}', vars.pass)
    .replaceAll('{agent}', vars.agent);
}

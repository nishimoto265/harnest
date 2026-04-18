import { execFileSync, execSync } from 'node:child_process';
import {
  accessSync,
  constants as fsConst,
  existsSync,
  mkdirSync,
  readFileSync,
  statSync,
} from 'node:fs';
import { resolve } from 'node:path';

import { ConfigError, loadConfig, type ResolvedConfig } from './lib/config.ts';
import { createLogger } from './lib/logger.ts';
import { detectRepoRoot, pkgRoot } from './lib/paths.ts';

export interface CheckResult {
  name: string;
  status: 'ok' | 'ng' | 'warn';
  detail?: string;
  remedy?: string;
}

export interface PreflightReport {
  ok: boolean;
  checks: CheckResult[];
  /** Number of NG checks (warn does not count). */
  ngCount: number;
  /** Number of WARN checks. */
  warnCount: number;
}

const MIN_NODE_MAJOR = 24;

/** Run a command safely, returning stdout or null on failure. */
function tryRun(bin: string, args: readonly string[], timeoutMs = 8000): string | null {
  try {
    return execFileSync(bin, args, {
      stdio: ['ignore', 'pipe', 'pipe'],
      encoding: 'utf8',
      timeout: timeoutMs,
    }).trim();
  } catch {
    return null;
  }
}

function checkNode(): CheckResult {
  const v = process.versions.node;
  const major = Number(v.split('.')[0] ?? '0');
  if (major >= MIN_NODE_MAJOR) {
    return { name: 'node', status: 'ok', detail: `v${v}` };
  }
  return {
    name: 'node',
    status: 'ng',
    detail: `v${v} (need >= ${MIN_NODE_MAJOR})`,
    remedy: `Install Node.js ${MIN_NODE_MAJOR}+ (e.g. nodenv install ${MIN_NODE_MAJOR}.x)`,
  };
}

function checkBinary(
  name: string,
  args: readonly string[],
  { required, remedy }: { required: boolean; remedy: string },
): CheckResult {
  const out = tryRun(name, args);
  if (out === null) {
    return {
      name,
      status: required ? 'ng' : 'warn',
      detail: `not found or failed: \`${name} ${args.join(' ')}\``,
      remedy,
    };
  }
  const firstLine = out.split('\n')[0] ?? out;
  return { name, status: 'ok', detail: firstLine };
}

function checkGhAuth(): CheckResult {
  try {
    // gh auth status exits non-zero if not logged in.
    execSync('gh auth status', { stdio: 'ignore', timeout: 10_000 });
    return { name: 'gh auth', status: 'ok', detail: 'authenticated' };
  } catch {
    return {
      name: 'gh auth',
      status: 'ng',
      detail: 'not authenticated',
      remedy: 'Run `gh auth login` and complete the OAuth flow.',
    };
  }
}

function checkWritableDir(path: string, label: string): CheckResult {
  try {
    mkdirSync(path, { recursive: true });
    accessSync(path, fsConst.W_OK);
    const s = statSync(path);
    if (!s.isDirectory()) {
      return {
        name: label,
        status: 'ng',
        detail: `${path} exists but is not a directory`,
        remedy: `Remove or rename ${path}`,
      };
    }
    return { name: label, status: 'ok', detail: path };
  } catch (e) {
    return {
      name: label,
      status: 'ng',
      detail: `${path} not writable: ${(e as Error).message}`,
      remedy: `Ensure ${path} is writable (chmod/chown or choose a different path in config.yaml)`,
    };
  }
}

function checkRepoRoot(): CheckResult {
  try {
    const root = detectRepoRoot();
    return { name: 'repo_root', status: 'ok', detail: root };
  } catch (e) {
    return {
      name: 'repo_root',
      status: 'ng',
      detail: (e as Error).message,
      remedy:
        'Run preflight from inside the delish-web-global git checkout, or set AUTO_IMPROVE_REPO_ROOT.',
    };
  }
}

function checkConfig(): { result: CheckResult; config?: ResolvedConfig } {
  try {
    const cfg = loadConfig();
    return {
      result: {
        name: 'config.yaml',
        status: 'ok',
        detail: cfg.configPath,
      },
      config: cfg,
    };
  } catch (e) {
    if (e instanceof ConfigError) {
      const detail = [e.message, ...e.issues].join('\n    ');
      return {
        result: {
          name: 'config.yaml',
          status: 'ng',
          detail,
          remedy:
            'Copy auto-improve/config.yaml.example to auto-improve/config.yaml and fix the listed fields.',
        },
      };
    }
    return {
      result: {
        name: 'config.yaml',
        status: 'ng',
        detail: (e as Error).message,
      },
    };
  }
}

function checkHarnessFiles(cfg: ResolvedConfig): CheckResult {
  const missing = cfg.abs.harnessFiles.filter((p) => !existsSync(p));
  if (missing.length === 0) {
    return {
      name: 'harness_files',
      status: 'ok',
      detail: `${cfg.abs.harnessFiles.length} entries present`,
    };
  }
  // Missing harness files are not fatal for M1 (they can be fixed later), but they
  // *will* break M2. Flag as warn so preflight still exits 0 for partial setups,
  // but the user sees the list.
  return {
    name: 'harness_files',
    status: 'warn',
    detail: `${missing.length} missing: ${missing.slice(0, 5).join(', ')}${missing.length > 5 ? ' …' : ''}`,
    remedy: 'Adjust paths.harness_files in config.yaml, or create the missing files/directories.',
  };
}

function checkPkgInstalled(): CheckResult {
  const nm = resolve(pkgRoot, 'node_modules');
  if (!existsSync(nm)) {
    return {
      name: 'pnpm install',
      status: 'ng',
      detail: `node_modules missing at ${nm}`,
      remedy: 'Run `cd auto-improve && pnpm install`',
    };
  }
  return { name: 'pnpm install', status: 'ok', detail: nm };
}

function checkPkgJson(): CheckResult {
  const pj = resolve(pkgRoot, 'package.json');
  try {
    const text = readFileSync(pj, 'utf8');
    const parsed = JSON.parse(text) as { name?: string; version?: string };
    return {
      name: 'package.json',
      status: 'ok',
      detail: `${parsed.name ?? '?'}@${parsed.version ?? '?'}`,
    };
  } catch (e) {
    return {
      name: 'package.json',
      status: 'ng',
      detail: (e as Error).message,
    };
  }
}

/** Execute the full preflight. Does not exit. */
export async function runPreflight(): Promise<PreflightReport> {
  const checks: CheckResult[] = [];

  checks.push(checkPkgJson());
  checks.push(checkPkgInstalled());
  checks.push(checkNode());
  checks.push(
    checkBinary('pnpm', ['--version'], {
      required: true,
      remedy: 'Install pnpm: `npm i -g pnpm` or via corepack.',
    }),
  );
  checks.push(
    checkBinary('git', ['--version'], {
      required: true,
      remedy: 'Install git.',
    }),
  );
  checks.push(checkRepoRoot());
  checks.push(
    checkBinary('gh', ['--version'], {
      required: true,
      remedy: 'Install GitHub CLI: https://cli.github.com/',
    }),
  );
  checks.push(checkGhAuth());
  checks.push(
    checkBinary('jq', ['--version'], {
      required: true,
      remedy: 'Install jq: `brew install jq` / `apt install jq`',
    }),
  );
  checks.push(
    checkBinary('yq', ['--version'], {
      required: true,
      remedy: 'Install yq v4+: `brew install yq`',
    }),
  );
  checks.push(
    checkBinary('claude', ['--version'], {
      required: true,
      remedy: 'Install Claude Code CLI (https://docs.claude.com/claude-code) and authenticate.',
    }),
  );
  checks.push(
    checkBinary('codex', ['--version'], {
      required: false, // optional; panel falls back to single-claude
      remedy:
        'Optional. Install codex CLI for panel judging; otherwise set judges.arbiter_rotation to only claude.',
    }),
  );

  // Config + derived checks
  const cfgCheck = checkConfig();
  checks.push(cfgCheck.result);
  if (cfgCheck.config) {
    checks.push(checkWritableDir(cfgCheck.config.abs.worktreeBase, 'worktree_base'));
    checks.push(checkWritableDir(cfgCheck.config.abs.runs, 'runs_dir'));
    checks.push(checkHarnessFiles(cfgCheck.config));
  }

  const ngCount = checks.filter((c) => c.status === 'ng').length;
  const warnCount = checks.filter((c) => c.status === 'warn').length;
  return { ok: ngCount === 0, checks, ngCount, warnCount };
}

function pad(s: string, n: number): string {
  return s + ' '.repeat(Math.max(0, n - s.length));
}

/**
 * Render a human-friendly ASCII table of the report to stdout.
 * No ANSI colors — stays readable when redirected or captured.
 */
function renderTable(report: PreflightReport): string {
  const nameW = Math.max(10, ...report.checks.map((c) => c.name.length));
  const statusW = 4; // "WARN"
  const rows: string[] = [];
  rows.push(`  ${pad('CHECK', nameW)}  ${pad('STATE', statusW)}  DETAIL`);
  rows.push(`  ${'-'.repeat(nameW)}  ${'-'.repeat(statusW)}  ${'-'.repeat(30)}`);
  for (const c of report.checks) {
    const sym = c.status === 'ok' ? 'OK' : c.status === 'warn' ? 'WARN' : 'NG';
    rows.push(`  ${pad(c.name, nameW)}  ${pad(sym, statusW)}  ${c.detail ?? ''}`);
    if (c.status !== 'ok' && c.remedy) {
      rows.push(`  ${pad('', nameW)}  ${pad('', statusW)}  → ${c.remedy}`);
    }
  }
  rows.push('');
  const summary = report.ok
    ? report.warnCount > 0
      ? `preflight OK — ${report.warnCount} warning(s). Pipeline can run; some steps may fail later.`
      : 'preflight OK — all required checks passed.'
    : `preflight FAILED — ${report.ngCount} blocking issue(s). Fix and re-run.`;
  rows.push(summary);
  return rows.join('\n') + '\n';
}

/**
 * CLI entry: print a report and exit non-zero on any NG.
 *
 * Output mode resolution:
 *   - `--json`         → JSON on stdout (machine-readable)
 *   - non-TTY stdout   → JSON on stdout (safe to pipe/capture)
 *   - TTY stdout       → ASCII table on stdout (human-readable)
 *
 * `--no-preflight-logs` is accepted as a future hook; currently unused.
 */
export async function preflightCLI(argv: readonly string[] = []): Promise<number> {
  const log = createLogger({ name: 'preflight' });
  const wantJson = argv.includes('--json');
  const isTTY = Boolean(process.stdout.isTTY);
  const useTable = !wantJson && isTTY;

  const report = await runPreflight();

  if (useTable) {
    process.stdout.write(renderTable(report));
  } else {
    // JSON mode — clean machine-readable output on stdout; no logger noise.
    process.stdout.write(JSON.stringify(report, null, 2) + '\n');
  }

  // Always emit a terse summary to the logger (stderr) so CI logs aren't silent.
  if (!useTable) {
    if (report.ok) {
      log.info(
        report.warnCount > 0
          ? `preflight OK with ${report.warnCount} warning(s).`
          : 'preflight OK.',
      );
    } else {
      log.error(`preflight FAILED — ${report.ngCount} blocking issue(s).`);
    }
  }

  return report.ok ? 0 : 1;
}

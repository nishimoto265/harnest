import { readFileSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';

import yaml from 'js-yaml';
import { z } from 'zod';

import { detectRepoRoot, pkgRoot, resolvePath } from './paths.ts';

/**
 * Config schema (zod) — mirrors config.yaml.example 1:1.
 * All user-facing typos/missing-required fields produce a human-readable
 * list via `formatZodIssues` (see loadConfig).
 */

const GithubRepoSchema = z.string().regex(/^[\w.-]+\/[\w.-]+$/, {
  message: 'repo.github must be "owner/name" form',
});

const RepoSchema = z.object({
  github: GithubRepoSchema,
  default_branch: z.string().min(1),
  best_branch: z.string().min(1),
});

const WorktreeSchema = z.object({
  base: z.string().min(1),
  naming: z
    .string()
    .min(1)
    .refine((s) => s.includes('{pr}'), {
      message: 'worktree.naming must contain {pr}',
    })
    .default('auto-improve-pr{pr}-{pass}-{agent}'),
  cleanup_on_success: z.boolean().default(true),
});

const PipelineSchema = z.object({
  agents_per_pass: z.number().int().positive().max(16).default(3),
  timeout_seconds: z.number().int().positive().default(1800),
});

const ModelsSchema = z.object({
  implementer: z.string().min(1).default('sonnet'),
  judge: z.string().min(1).default('sonnet'),
  cheap_judge: z.string().min(1).default('haiku'),
});

const AgentsSchema = z.object({
  implementer: z.string().min(1).default('claude'),
  judge_primary: z.string().min(1).default('claude'),
  judge_secondary: z.string().optional(),
});

const ProviderEntrySchema = z.object({
  max_concurrent: z.number().int().positive(),
  tokens_per_minute: z.number().int().positive().optional(),
  fallback: z.string().min(1),
});
const ProvidersSchema = z.record(z.string(), ProviderEntrySchema).default({});

const PathsSchema = z.object({
  rules_source: z.string().min(1),
  rules_registry: z.string().min(1),
  rubric: z.string().min(1),
  checklist_generator: z.string().min(1),
  archives: z.string().min(1),
  runs: z.string().min(1),
  state_file: z.string().min(1),
  harness_files: z.array(z.string().min(1)).min(1),
});

const PromptsSchema = z.object({
  judge_score: z.string().min(1),
  judge_pairwise: z.string().min(1),
  extract_rules: z.string().min(1),
});

const JudgesSchema = z.object({
  score: z.string().min(1),
  pairwise: z.string().min(1),
  arbiter_rotation: z.array(z.string().min(1)).min(1).default(['codex']),
});

const ThresholdsSchema = z.object({
  adopt_win_count: z.number().int().positive().default(2),
  adopt_score_delta_min: z.number().default(0.3),
});

const SunsetSchema = z.object({
  min_prs_since_last: z.number().int().positive().default(10),
  min_days_since_last: z.number().int().positive().default(14),
  time_fallback_days: z.number().int().positive().default(90),
  contribution_negative_threshold: z.number().default(-0.3),
  compliance_low_threshold: z.number().min(0).max(1).default(0.3),
  at_risk_consecutive_for_remove: z.number().int().positive().default(2),
});

/**
 * `since` (optional) — absolute ISO date (YYYY-MM-DD). Overrides `lookback_days` when set.
 * `max_lookback_days` — upper bound when state-aware widening is active.
 */
const DetectSchema = z.object({
  since: z
    .string()
    .regex(/^\d{4}-\d{2}-\d{2}$/, {
      message: 'detect.since must be YYYY-MM-DD (e.g. 2026-01-01)',
    })
    .optional(),
  lookback_days: z.number().int().positive().default(30),
  max_lookback_days: z.number().int().positive().default(180),
  limit: z.number().int().positive().max(200).default(50),
  start_after_pr: z.number().int().nonnegative().default(0),
});

const NotifySchema = z
  .object({
    cmux_surface: z.string().default(''),
  })
  .default({ cmux_surface: '' });

const LoggingSchema = z
  .object({
    level: z.enum(['trace', 'debug', 'info', 'warn', 'error', 'fatal']).default('info'),
    pretty: z.boolean().default(true),
  })
  .default({ level: 'info', pretty: true });

export const ConfigSchema = z.object({
  // Repo is the only fully-required block (no sensible defaults for owner/branch).
  repo: RepoSchema,
  worktree: WorktreeSchema,
  pipeline: PipelineSchema.default({ agents_per_pass: 3, timeout_seconds: 1800 }),
  models: ModelsSchema.default({
    implementer: 'sonnet',
    judge: 'sonnet',
    cheap_judge: 'haiku',
  }),
  agents: AgentsSchema.default({ implementer: 'claude', judge_primary: 'claude' }),
  providers: ProvidersSchema,
  paths: PathsSchema,
  prompts: PromptsSchema,
  judges: JudgesSchema,
  thresholds: ThresholdsSchema.default({ adopt_win_count: 2, adopt_score_delta_min: 0.3 }),
  sunset: SunsetSchema.default({
    min_prs_since_last: 10,
    min_days_since_last: 14,
    time_fallback_days: 90,
    contribution_negative_threshold: -0.3,
    compliance_low_threshold: 0.3,
    at_risk_consecutive_for_remove: 2,
  }),
  detect: DetectSchema.default({
    lookback_days: 30,
    max_lookback_days: 180,
    limit: 50,
    start_after_pr: 0,
  }),
  notify: NotifySchema,
  logging: LoggingSchema,
});

export type Config = z.infer<typeof ConfigSchema>;

/** Resolved config: every user-provided path has been turned into an absolute path. */
export interface ResolvedConfig {
  raw: Config;
  repoRoot: string;
  pkgRoot: string;
  configPath: string;
  /** Absolute paths. Keyed by the logical name used across the codebase. */
  abs: {
    rulesSource: string;
    rulesRegistry: string;
    rubric: string;
    checklistGenerator: string;
    archives: string;
    runs: string;
    stateFile: string;
    worktreeBase: string;
    prompts: { judgeScore: string; judgePairwise: string; extractRules: string };
    judges: { score: string; pairwise: string };
    harnessFiles: string[];
  };
}

export class ConfigError extends Error {
  readonly issues: string[];
  constructor(message: string, issues: string[] = []) {
    super(message);
    this.name = 'ConfigError';
    this.issues = issues;
  }
}

function formatZodIssues(err: z.ZodError): string[] {
  return err.issues.map((i) => {
    const path = i.path.length ? i.path.join('.') : '<root>';
    return `  - ${path}: ${i.message}`;
  });
}

/**
 * Default config search order:
 *   1. explicit path param
 *   2. $AUTO_IMPROVE_CONFIG
 *   3. <repoRoot>/auto-improve/config.yaml
 *   4. <pkgRoot>/config.yaml
 */
export function resolveConfigPath(explicit: string | undefined, repoRoot: string): string {
  if (explicit) {
    return isAbsolute(explicit) ? explicit : resolve(process.cwd(), explicit);
  }
  if (process.env.AUTO_IMPROVE_CONFIG) {
    return resolve(process.env.AUTO_IMPROVE_CONFIG);
  }
  return resolve(repoRoot, 'auto-improve', 'config.yaml');
}

/**
 * Load, parse, validate, and resolve config.yaml.
 *
 * @throws ConfigError with human-readable issues if anything is off.
 */
export function loadConfig(explicitPath?: string): ResolvedConfig {
  const repoRoot = detectRepoRoot();
  const configPath = resolveConfigPath(explicitPath, repoRoot);

  let raw: unknown;
  try {
    const text = readFileSync(configPath, 'utf8');
    raw = yaml.load(text);
  } catch (e) {
    const err = e as NodeJS.ErrnoException;
    if (err.code === 'ENOENT') {
      throw new ConfigError(
        `config.yaml not found at ${configPath}. ` +
          `Copy auto-improve/config.yaml.example to this path and edit it.`,
      );
    }
    throw new ConfigError(`Failed to read ${configPath}: ${err.message ?? String(e)}`);
  }

  const parsed = ConfigSchema.safeParse(raw);
  if (!parsed.success) {
    throw new ConfigError(
      `config.yaml has validation errors (${parsed.error.issues.length}):`,
      formatZodIssues(parsed.error),
    );
  }

  const c = parsed.data;
  const abs: ResolvedConfig['abs'] = {
    rulesSource: resolvePath(c.paths.rules_source, repoRoot),
    rulesRegistry: resolvePath(c.paths.rules_registry, repoRoot),
    rubric: resolvePath(c.paths.rubric, repoRoot),
    checklistGenerator: resolvePath(c.paths.checklist_generator, repoRoot),
    archives: resolvePath(c.paths.archives, repoRoot),
    runs: resolvePath(c.paths.runs, repoRoot),
    stateFile: resolvePath(c.paths.state_file, repoRoot),
    worktreeBase: resolvePath(c.worktree.base, repoRoot),
    prompts: {
      judgeScore: resolvePath(c.prompts.judge_score, repoRoot),
      judgePairwise: resolvePath(c.prompts.judge_pairwise, repoRoot),
      extractRules: resolvePath(c.prompts.extract_rules, repoRoot),
    },
    judges: {
      score: resolvePath(c.judges.score, repoRoot),
      pairwise: resolvePath(c.judges.pairwise, repoRoot),
    },
    harnessFiles: c.paths.harness_files.map((p) => resolvePath(p, repoRoot)),
  };

  return {
    raw: c,
    repoRoot,
    pkgRoot,
    configPath,
    abs,
  };
}

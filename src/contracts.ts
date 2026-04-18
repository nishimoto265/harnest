/**
 * contracts.ts — Single source of truth for inter-step I/O schemas.
 *
 * DO NOT edit a schema in isolation without checking consumers in other steps.
 * All step implementations MUST import from this file; do not reinvent shapes.
 *
 * See docs/design/io-contracts.md for the human-readable spec and per-step duties.
 */

import { z } from 'zod';

// ---------------------------------------------------------------------------
// Primitive reuse
// ---------------------------------------------------------------------------

const Sha256Hex = z
  .string()
  .regex(/^[a-f0-9]{64}$/, 'must be a lowercase 64-char sha256 hex');

const IsoDateTime = z.string().datetime({ offset: true });

/** Agent identity: a1, a2, ..., a9 (strict). */
export const AgentIdSchema = z
  .string()
  .regex(/^a[1-9][0-9]*$/, 'agent id must be "a<positive-integer>" (e.g. a1)');

const PrNumber = z.number().int().positive();

/** Strict run id shape produced by run-layout.ts#newRunId. */
export const RunIdSchema = z
  .string()
  .regex(
    /^\d{4}-\d{2}-\d{2}-PR\d+-[a-f0-9]+$/,
    'run_id must be "YYYY-MM-DD-PR<num>-<hexshort>"',
  );

const RuleIdSchema = z
  .string()
  .regex(/^[A-Z]+(?:-[A-Z0-9]+)+$/, 'rule id must be UPPER-KEBAB (e.g. AI-GEN-016)');

export const PassSchema = z.enum(['A', 'B']);

/** Git commit SHA (40 lowercase hex). */
export const CommitShaSchema = z.string().regex(/^[a-f0-9]{40}$/, 'commit sha must be 40-hex');

// ---------------------------------------------------------------------------
// processed.jsonl — run-state log
// ---------------------------------------------------------------------------

export const EVENT_VALUES = [
  'started',
  'step_done',
  'promoting',
  'promoted',
  'rollback',
  'failed',
  'timeout',
  'skipped',
  'completed',
] as const;
export type Event = (typeof EVENT_VALUES)[number];

export const TERMINAL_EVENTS: ReadonlySet<Event> = new Set<Event>([
  'promoted',
  'rollback',
  'skipped',
  'failed',
  'completed',
  'timeout',
]);

export const StateEntrySchema = z
  .object({
    pr: PrNumber,
    event: z.enum(EVENT_VALUES),
    at: IsoDateTime,
    run_id: RunIdSchema,
    /** absolute path to the run directory (optional at `started`) */
    run_dir: z.string().optional(),
    /** step_done only: step number (10/20/30/40/50/60/70) — required iff event === "step_done" */
    step: z.number().int().positive().optional(),
    /** promoted only */
    adopted: z.boolean().optional(),
    /** failed/timeout/rollback */
    reason: z.string().optional(),
    /** atomic-completion manifest path (Codex H5) — set after step 20/50 */
    manifest: z.string().optional(),
  })
  .strict()
  .superRefine((v, ctx) => {
    if (v.event === 'step_done' && v.step === undefined) {
      ctx.addIssue({
        code: 'custom',
        message: 'step is required when event === "step_done"',
        path: ['step'],
      });
    }
    if (v.event !== 'step_done' && v.step !== undefined) {
      ctx.addIssue({
        code: 'custom',
        message: 'step must only be set when event === "step_done"',
        path: ['step'],
      });
    }
  });
export type StateEntry = z.infer<typeof StateEntrySchema>;

// ---------------------------------------------------------------------------
// Worktree allocation (produced by step 10, consumed by step 20/50/70)
// ---------------------------------------------------------------------------

export const WorktreeAllocationSchema = z
  .object({
    agent: AgentIdSchema,
    pass: PassSchema,
    /** absolute worktree path */
    path: z.string(),
    /** branch created for this worktree */
    branch: z.string().min(1),
    /** base SHA the worktree was restored to */
    base_sha: CommitShaSchema,
    /** head SHA on the branch at step 10 creation time */
    head_sha: CommitShaSchema,
  })
  .strict();
export type WorktreeAllocation = z.infer<typeof WorktreeAllocationSchema>;

// ---------------------------------------------------------------------------
// step 10 output: task-package.json
// ---------------------------------------------------------------------------

export const TaskPackageSchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    title: z.string(),
    /** raw PR body as produced by `gh pr view` */
    body: z.string(),
    base_ref: z.string(), // e.g. "develop"
    base_sha: CommitShaSchema,
    head_ref: z.string(),
    linked_issue_numbers: z.array(PrNumber).default([]),
    /** heuristic-mined target file paths from PR body or diff */
    target_files_hint: z.array(z.string()).default([]),
    /** heuristic-mined acceptance criteria bullets (if discoverable) */
    acceptance_hints: z.array(z.string()).default([]),
    /**
     * Reconstructed natural-language task prompt to hand to implementers.
     * Treat as UNSAFE for direct prompt embedding — always run through
     * `src/io/safe-text.ts#sanitizeForPromptEmbedding` before including
     * in a judge/extractor prompt.
     */
    reconstructed_task_prompt: z.string().min(1).max(20_000),
    /** canonical agent ids this run will use, e.g. ["a1","a2","a3"] */
    agent_ids: z.array(AgentIdSchema).min(1),
    /** worktree metadata for pass1 + pass2 × all agents; consumed by step 70 cleanup */
    worktrees: z.array(WorktreeAllocationSchema),
    /** ISO-8601 when this package was built */
    built_at: IsoDateTime,
  })
  .strict();
export type TaskPackage = z.infer<typeof TaskPackageSchema>;

// ---------------------------------------------------------------------------
// checklist-result.json (step 20/50 output per-agent)
// ---------------------------------------------------------------------------

/**
 * 3-symbol model from 全体設計.md:
 *   compliant  = [x] rule applicable and satisfied
 *   n_a        = [-] rule out of scope for this task
 *   exception  = [?] in scope but doesn't apply to this specific case (reason required)
 *   unchecked  = [ ] agent did not mark — tracked separately for checklist quality
 *
 * An explicit "violated" self-report is deliberately NOT included — see Codex H3.
 * Violations are detected by judges in step 30/60, not self-reported.
 */
export const ChecklistVerdictSchema = z.enum(['compliant', 'n_a', 'exception', 'unchecked']);
export type ChecklistVerdict = z.infer<typeof ChecklistVerdictSchema>;

export const ChecklistItemSchema = z
  .object({
    rule: RuleIdSchema,
    verdict: ChecklistVerdictSchema,
    /** required when verdict === "exception"; sanitized free text */
    reason: z.string().max(1000).optional(),
  })
  .strict()
  .superRefine((val, ctx) => {
    if (val.verdict === 'exception' && !val.reason) {
      ctx.addIssue({
        code: 'custom',
        message: 'reason is required when verdict === "exception"',
        path: ['reason'],
      });
    }
  });
export type ChecklistItem = z.infer<typeof ChecklistItemSchema>;

export const ChecklistResultSchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    agent: AgentIdSchema,
    pass: PassSchema,
    items: z.array(ChecklistItemSchema),
    /** source rules file path or hash — traceability */
    checklist_version: z.string().min(1),
  })
  .strict();
export type ChecklistResult = z.infer<typeof ChecklistResultSchema>;

// ---------------------------------------------------------------------------
// manifest.json (step 20/50 output, atomic completion marker)
//
// Discriminated union on exit_status so exit_code/head_sha combinations are
// internally consistent (Codex MEDIUM-19).
// ---------------------------------------------------------------------------

const ManifestBaseFields = {
  run_id: RunIdSchema,
  pr: PrNumber,
  agent: AgentIdSchema,
  pass: PassSchema,
  started_at: IsoDateTime,
  completed_at: IsoDateTime,
  /** absolute path to worktree (outside run dir) */
  worktree: z.string(),
  /** sha256 of session.jsonl — integrity proof */
  session_jsonl_sha256: Sha256Hex,
  /** sha256 of diff.patch */
  diff_sha256: Sha256Hex,
  /** sha256 of checklist-result.json, null if absent */
  checklist_result_sha256: Sha256Hex.nullable(),
  /** version/hash of the checklist used (Claude review: redundant store) */
  checklist_version: z.string().min(1),
  /** bytes written by agent during the session, for sanity */
  bytes_written: z.number().int().nonnegative(),
} as const;

const ManifestSuccessSchema = z
  .object({
    ...ManifestBaseFields,
    exit_status: z.literal('success'),
    exit_code: z.literal(0),
    head_sha: CommitShaSchema, // must have committed something
  })
  .strict();

const ManifestErrorSchema = z
  .object({
    ...ManifestBaseFields,
    exit_status: z.literal('error'),
    exit_code: z.number().int().refine((n) => n !== 0, 'error requires non-zero exit_code'),
    head_sha: CommitShaSchema.nullable(),
  })
  .strict();

const ManifestTimeoutSchema = z
  .object({
    ...ManifestBaseFields,
    exit_status: z.literal('timeout'),
    exit_code: z.null(),
    head_sha: CommitShaSchema.nullable(),
  })
  .strict();

export const ManifestSchema = z.discriminatedUnion('exit_status', [
  ManifestSuccessSchema,
  ManifestErrorSchema,
  ManifestTimeoutSchema,
]);
export type Manifest = z.infer<typeof ManifestSchema>;

// ---------------------------------------------------------------------------
// scores-{A|B}.jsonl (step 30 / step 60 output)
// ---------------------------------------------------------------------------

export const RUBRIC_DIMENSIONS = [
  'correctness',
  'design',
  'idiomatic',
  'fidelity',
  'discipline',
] as const;

export const DimensionScoreSchema = z.number().int().min(1).max(5);

const VerdictPathSchema = z.enum([
  'single', // only one judge ran
  'agreement', // primary and secondary matched
  'arbitrated', // arbiter broke a tie
  'arbiter_overruled', // arbiter overrode both primary/secondary
]);

export const ScoreEntrySchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    pass: PassSchema,
    agent: AgentIdSchema,
    scores: z
      .object({
        correctness: DimensionScoreSchema,
        design: DimensionScoreSchema,
        idiomatic: DimensionScoreSchema,
        fidelity: DimensionScoreSchema,
        discipline: DimensionScoreSchema,
      })
      .strict(),
    /** precomputed sum for convenience. Must equal Σ(scores.*). */
    total: z.number().int().min(5).max(25),
    /**
     * Structured per-dimension reasons. Each dimension capped at 1000 chars
     * to stay under the 4KB JSONL line budget. When longer, callers must
     * offload to a sidecar file and set `reasons_overflow_ref`.
     */
    reasons: z
      .object({
        correctness: z.string().max(1000),
        design: z.string().max(1000),
        idiomatic: z.string().max(1000),
        fidelity: z.string().max(1000),
        discipline: z.string().max(1000),
      })
      .strict(),
    /** sha256 of an out-of-line reasons blob when text exceeded inline budget */
    reasons_overflow_ref: Sha256Hex.optional(),
    /** Which judge(s) produced this score. `verdict_path` records the resolution mode. */
    judged_by: z
      .object({
        primary: z.string(),
        secondary: z.string().nullable(),
        arbiter: z.string().nullable(),
        verdict_path: VerdictPathSchema,
      })
      .strict(),
    rubric_version: z.string().min(1),
    prompt_version: z.string().min(1),
    scored_at: IsoDateTime,
  })
  .strict();
export type ScoreEntry = z.infer<typeof ScoreEntrySchema>;

// ---------------------------------------------------------------------------
// compliance-{A|B}.jsonl (step 30 / step 60 output, subordinate to Discipline)
// ---------------------------------------------------------------------------

export const ComplianceVerdictSchema = z.enum([
  'compliant',
  'violated',
  'valid_exception',
  'invalid_exception',
  'missed', // judge found a violation the agent did not self-report
  'n_a', // confirmed out-of-scope
]);
export type ComplianceVerdict = z.infer<typeof ComplianceVerdictSchema>;

export const ComplianceEntrySchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    pass: PassSchema,
    agent: AgentIdSchema,
    rule: RuleIdSchema,
    /** what the agent self-reported */
    self: ChecklistVerdictSchema,
    /** judge conclusion after cross-checking against diff & real PR */
    verdict: ComplianceVerdictSchema,
    /** path-like evidence (file:line etc.); capped to stay within 4KB budget */
    evidence: z.string().max(1000).optional(),
    audited_at: IsoDateTime,
  })
  .strict();
export type ComplianceEntry = z.infer<typeof ComplianceEntrySchema>;

// ---------------------------------------------------------------------------
// candidates.json (step 40 output)
// ---------------------------------------------------------------------------

export const CandidateKindSchema = z.enum(['new', 'update', 'duplicate']);
export type CandidateKind = z.infer<typeof CandidateKindSchema>;

export const CandidateCheckMethodSchema = z.enum(['agent', 'grep', 'lint']);

export const CandidateSchema = z
  .object({
    /** stable provisional id (hash of rule text + PR) — finalized when promoted */
    provisional_id: z.string().min(1),
    kind: CandidateKindSchema,
    /** When kind === "update", the existing rule id being updated */
    updates: RuleIdSchema.optional(),
    /** Human-readable rule statement. Strict length budget. */
    statement: z.string().min(1).max(2000),
    /** Evidence from pass1 that this rule would have helped (free text, capped) */
    problem: z.string().max(2000),
    rationale: z.string().max(2000),
    check_method: CandidateCheckMethodSchema.default('agent'),
    /** Auto-generated examples for the rule body (each entry capped) */
    examples: z.array(z.string().max(1000)).default([]),
  })
  .strict();
export type Candidate = z.infer<typeof CandidateSchema>;

export const CandidatesSchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    extracted_at: IsoDateTime,
    candidates: z.array(CandidateSchema),
  })
  .strict();
export type Candidates = z.infer<typeof CandidatesSchema>;

export const ClassificationEntrySchema = z
  .object({
    run_id: RunIdSchema,
    provisional_id: z.string(),
    kind: CandidateKindSchema,
    /** similarity score 0..1 against existing registry; present for update/duplicate */
    similarity: z.number().min(0).max(1).optional(),
    matched_rule: RuleIdSchema.optional(),
    rationale: z.string().max(1000).optional(),
    classified_at: IsoDateTime,
  })
  .strict();
export type ClassificationEntry = z.infer<typeof ClassificationEntrySchema>;

// ---------------------------------------------------------------------------
// pairwise.jsonl (step 60 output)
// ---------------------------------------------------------------------------

export const PairwiseWinnerSchema = z.enum(['A', 'B', 'tie']);

export const PairwiseMarginSchema = z.enum(['decisive', 'clear', 'slight']);

export const PairwiseEntrySchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    agent: AgentIdSchema, // the agent identity across both passes
    winner: PairwiseWinnerSchema,
    margin: PairwiseMarginSchema,
    reason: z.string().max(1500),
    judged_by: z
      .object({
        primary: z.string(),
        secondary: z.string().nullable(),
        arbiter: z.string().nullable(),
        verdict_path: VerdictPathSchema,
      })
      .strict(),
    prompt_version: z.string().min(1),
    judged_at: IsoDateTime,
  })
  .strict();
export type PairwiseEntry = z.infer<typeof PairwiseEntrySchema>;

// ---------------------------------------------------------------------------
// decision.json (step 70 output)
// ---------------------------------------------------------------------------

export const DecisionActionSchema = z.enum([
  'adopt', // candidates adopted → best_branch updated
  'reject', // candidates rejected → archived/rejected
  'noop', // nothing to decide (e.g. candidates empty)
  'rollback', // promotion attempted but failed, reverted
]);

export const AppliedRuleSchema = z
  .object({
    kind: CandidateKindSchema,
    provisional_id: z.string(),
    final_rule_id: RuleIdSchema.optional(), // set once promoted
    updates: RuleIdSchema.optional(),
  })
  .strict();

export const DecisionSchema = z
  .object({
    run_id: RunIdSchema,
    pr: PrNumber,
    action: DecisionActionSchema,
    /** Σ pairwise wins for B (candidates side) */
    win_count: z.number().int().nonnegative(),
    /** average(scores-B.total) − average(scores-A.total) */
    score_delta_avg: z.number(),
    /** what went into best_branch (empty on reject/noop/rollback) */
    applied: z.array(AppliedRuleSchema).default([]),
    /** rules rejected in this run (preserved in archives/rejected) */
    rejected: z.array(AppliedRuleSchema).default([]),
    /** best_branch commit SHA BEFORE this decision — rollback anchor */
    best_sha_before: CommitShaSchema,
    /** best_branch commit SHA after this decision; null if unchanged or rolled back */
    best_sha_after: CommitShaSchema.nullable(),
    /** populated when action === "rollback" */
    rollback_reason: z.string().max(1000).optional(),
    decided_at: IsoDateTime,
  })
  .strict();
export type Decision = z.infer<typeof DecisionSchema>;

// ---------------------------------------------------------------------------
// rules-registry.jsonl (cross-PR rule lifecycle, append-only)
//
// Discriminated union on `event` so each lifecycle transition carries exactly
// the fields it needs. A monotonic `version_seq` per rule, plus `prev_hash`
// (sha256 of the previous entry for this rule), lets consumers reject stale
// appends via CAS semantics (Codex CRITICAL-5).
// ---------------------------------------------------------------------------

export const RuleStatusSchema = z.enum(['active', 'at_risk', 'removed']);
export type RuleStatus = z.infer<typeof RuleStatusSchema>;

export const RuleRegistryMetricsSchema = z
  .object({
    fire_count: z.number().int().nonnegative(),
    compliance_count: z.number().int().nonnegative(),
    violation_count: z.number().int().nonnegative(),
    contribution_avg: z.number(),
    samples: z.number().int().nonnegative(),
  })
  .strict();
export type RuleRegistryMetrics = z.infer<typeof RuleRegistryMetricsSchema>;

const RuleRegistryCommon = {
  rule_id: RuleIdSchema,
  at: IsoDateTime,
  /**
   * Monotonic version per rule_id. First entry = 1; subsequent = prev + 1.
   * Consumer MUST reject non-monotonic appends.
   */
  version_seq: z.number().int().positive(),
  /**
   * sha256 of the previous entry's serialized JSON (empty string for version_seq=1).
   * CAS token for append ordering.
   */
  prev_hash: z.union([z.literal(''), Sha256Hex]),
} as const;

const RuleRegistryAddedSchema = z
  .object({
    ...RuleRegistryCommon,
    event: z.literal('added'),
    source_pr: PrNumber,
    run_id: RunIdSchema,
    text_hash: Sha256Hex,
    kind: z.enum(['new']),
    status: z.literal('active'),
    check_method: CandidateCheckMethodSchema,
  })
  .strict();

const RuleRegistryUpdatedSchema = z
  .object({
    ...RuleRegistryCommon,
    event: z.literal('updated'),
    source_pr: PrNumber,
    run_id: RunIdSchema,
    text_hash: Sha256Hex,
    kind: z.literal('update'),
    status: RuleStatusSchema,
  })
  .strict();

const RuleRegistryStatusChangedSchema = z
  .object({
    ...RuleRegistryCommon,
    event: z.literal('status_changed'),
    status: RuleStatusSchema,
    source_pr: PrNumber.optional(),
    run_id: RunIdSchema.optional(),
    metrics_snapshot: RuleRegistryMetricsSchema,
  })
  .strict();

const RuleRegistryArchivedSchema = z
  .object({
    ...RuleRegistryCommon,
    event: z.literal('archived'),
    status: z.literal('removed'),
    archive_reason: z.string().min(1).max(500),
    metrics_snapshot: RuleRegistryMetricsSchema,
    source_pr: PrNumber.optional(),
    run_id: RunIdSchema.optional(),
  })
  .strict();

const RuleRegistryRestoredSchema = z
  .object({
    ...RuleRegistryCommon,
    event: z.literal('restored'),
    status: RuleStatusSchema,
    source_pr: PrNumber.optional(),
    run_id: RunIdSchema.optional(),
  })
  .strict();

export const RuleRegistryEntrySchema = z.discriminatedUnion('event', [
  RuleRegistryAddedSchema,
  RuleRegistryUpdatedSchema,
  RuleRegistryStatusChangedSchema,
  RuleRegistryArchivedSchema,
  RuleRegistryRestoredSchema,
]);
export type RuleRegistryEntry = z.infer<typeof RuleRegistryEntrySchema>;

// ---------------------------------------------------------------------------
// Re-exports grouped for consumer ergonomics
// ---------------------------------------------------------------------------

export const Schemas = {
  StateEntry: StateEntrySchema,
  TaskPackage: TaskPackageSchema,
  Manifest: ManifestSchema,
  ChecklistResult: ChecklistResultSchema,
  ChecklistItem: ChecklistItemSchema,
  WorktreeAllocation: WorktreeAllocationSchema,
  ScoreEntry: ScoreEntrySchema,
  ComplianceEntry: ComplianceEntrySchema,
  Candidate: CandidateSchema,
  Candidates: CandidatesSchema,
  ClassificationEntry: ClassificationEntrySchema,
  PairwiseEntry: PairwiseEntrySchema,
  Decision: DecisionSchema,
  RuleRegistryEntry: RuleRegistryEntrySchema,
} as const;

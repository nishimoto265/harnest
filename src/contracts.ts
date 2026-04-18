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

const AgentId = z
  .string()
  .regex(/^a[1-9][0-9]*$/, 'agent id must be "a<positive-integer>" (e.g. a1)');

const PrNumber = z.number().int().positive();

const RunId = z.string().min(1);

const RuleIdSchema = z
  .string()
  .regex(/^[A-Z]+(?:-[A-Z0-9]+)+$/, 'rule id must be UPPER-KEBAB (e.g. AI-GEN-016)');

const PassName = z.enum(['A', 'B']);

// ---------------------------------------------------------------------------
// processed.jsonl — run-state log (existing, kept compatible)
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

export const StateEntrySchema = z.object({
  pr: PrNumber,
  event: z.enum(EVENT_VALUES),
  at: IsoDateTime,
  run_id: RunId,
  /** absolute path to the run directory (optional at `started`) */
  run_dir: z.string().optional(),
  /** step_done only: step number (10/20/30/40/50/60/70) */
  step: z.number().int().positive().optional(),
  /** promoted only */
  adopted: z.boolean().optional(),
  /** failed/timeout/rollback */
  reason: z.string().optional(),
  /** atomic-completion manifest path (Codex H5) — set after step 20/50 */
  manifest: z.string().optional(),
});
export type StateEntry = z.infer<typeof StateEntrySchema>;

// ---------------------------------------------------------------------------
// step 10 output: task-package.json
// ---------------------------------------------------------------------------

export const TaskPackageSchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  title: z.string(),
  /** raw PR body as produced by `gh pr view` */
  body: z.string(),
  base_ref: z.string(), // e.g. "develop"
  base_sha: z.string().regex(/^[a-f0-9]{40}$/, 'base_sha must be a 40-char hex'),
  head_ref: z.string(),
  linked_issue_numbers: z.array(z.number().int().positive()).default([]),
  /** heuristic-mined target file paths from PR body or diff */
  target_files_hint: z.array(z.string()).default([]),
  /** heuristic-mined acceptance criteria bullets (if discoverable) */
  acceptance_hints: z.array(z.string()).default([]),
  /**
   * Reconstructed natural-language task prompt to hand to implementers.
   * Must be instruction-injection safe — agent prompts MUST wrap this
   * in a clearly labeled block when embedding downstream.
   */
  reconstructed_task_prompt: z.string().min(1),
  /** ISO-8601 when this package was built */
  built_at: IsoDateTime,
});
export type TaskPackage = z.infer<typeof TaskPackageSchema>;

// ---------------------------------------------------------------------------
// checklist-result.json (step 20/50 output per-agent)
// ---------------------------------------------------------------------------

/**
 * 3-symbol model from 全体設計.md:
 *   compliant  = [x] agent claims rule is applicable and satisfied
 *   n_a        = [-] agent claims rule is out of scope for this task
 *   exception  = [?] agent claims rule is in scope but does not apply to this specific case (reason required)
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
    /** required when verdict === "exception"; free text in a sanitized field */
    reason: z.string().optional(),
  })
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

export const ChecklistResultSchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  agent: AgentId,
  pass: PassName,
  items: z.array(ChecklistItemSchema),
  /** source rules file path or hash — traceability */
  checklist_version: z.string(),
});
export type ChecklistResult = z.infer<typeof ChecklistResultSchema>;

// ---------------------------------------------------------------------------
// manifest.json (step 20/50 output per-agent, atomic completion marker)
// ---------------------------------------------------------------------------

export const ManifestExitSchema = z.enum(['success', 'error', 'timeout']);

export const ManifestSchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  agent: AgentId,
  pass: PassName,
  started_at: IsoDateTime,
  completed_at: IsoDateTime,
  /** claude CLI exit code (null if killed by timeout) */
  exit_code: z.number().int().nullable(),
  exit_status: ManifestExitSchema,
  /** absolute path to worktree (outside run dir) */
  worktree: z.string(),
  /** commit SHA produced by the agent (auto-commit or explicit); null if nothing committed */
  head_sha: z.string().regex(/^[a-f0-9]{40}$/).nullable(),
  /** sha256 of session.jsonl — integrity proof */
  session_jsonl_sha256: Sha256Hex,
  /** sha256 of diff.patch */
  diff_sha256: Sha256Hex,
  /** sha256 of checklist-result.json, null if absent */
  checklist_result_sha256: Sha256Hex.nullable(),
  /** bytes written by agent during the session, for sanity */
  bytes_written: z.number().int().nonnegative(),
});
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

export const ScoreEntrySchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  pass: PassName,
  agent: AgentId,
  scores: z.object({
    correctness: DimensionScoreSchema,
    design: DimensionScoreSchema,
    idiomatic: DimensionScoreSchema,
    fidelity: DimensionScoreSchema,
    discipline: DimensionScoreSchema,
  }),
  /** precomputed sum for convenience. Must equal Σ(scores.*). Enforced in helper. */
  total: z.number().int().min(5).max(25),
  /**
   * Structured per-dimension reasons. Free text is isolated here
   * (do not embed into downstream prompts without sanitization).
   */
  reasons: z.object({
    correctness: z.string(),
    design: z.string(),
    idiomatic: z.string(),
    fidelity: z.string(),
    discipline: z.string(),
  }),
  /** Which judge model(s) produced this score; rotation tracked for arbitration */
  judged_by: z.object({
    primary: z.string(), // model id, e.g. "claude-opus-4-7"
    secondary: z.string().nullable(),
    arbiter: z.string().nullable(), // Codex arbiter when used
  }),
  scored_at: IsoDateTime,
});
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

export const ComplianceEntrySchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  pass: PassName,
  agent: AgentId,
  rule: RuleIdSchema,
  /** what the agent self-reported (may be "unchecked" if the agent did not mark) */
  self: ChecklistVerdictSchema,
  /** what the judge concluded after cross-checking against diff & real PR */
  verdict: ComplianceVerdictSchema,
  /** path-like evidence (file:line, rule-text quote hash, etc.) */
  evidence: z.string().optional(),
  audited_at: IsoDateTime,
});
export type ComplianceEntry = z.infer<typeof ComplianceEntrySchema>;

// ---------------------------------------------------------------------------
// candidates.json (step 40 output)
// ---------------------------------------------------------------------------

export const CandidateKindSchema = z.enum(['new', 'update', 'duplicate']);
export type CandidateKind = z.infer<typeof CandidateKindSchema>;

export const CandidateCheckMethodSchema = z.enum(['agent', 'grep', 'lint']);

export const CandidateSchema = z.object({
  /** stable provisional id (hash of rule text + PR) — finalized when promoted */
  provisional_id: z.string().min(1),
  kind: CandidateKindSchema,
  /** When kind === "update", the existing rule id being updated */
  updates: RuleIdSchema.optional(),
  /** Human-readable rule statement. Strict limit recommended (~400 chars). */
  statement: z.string().min(1).max(2000),
  /** Evidence from pass1 that this rule would have helped */
  problem: z.string(),
  rationale: z.string(),
  check_method: CandidateCheckMethodSchema.default('agent'),
  /** Auto-generated examples for the rule body (if any) */
  examples: z.array(z.string()).default([]),
});
export type Candidate = z.infer<typeof CandidateSchema>;

export const CandidatesSchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  extracted_at: IsoDateTime,
  candidates: z.array(CandidateSchema),
});
export type Candidates = z.infer<typeof CandidatesSchema>;

export const ClassificationEntrySchema = z.object({
  run_id: RunId,
  provisional_id: z.string(),
  kind: CandidateKindSchema,
  /** similarity score 0..1 against existing registry; present for update/duplicate */
  similarity: z.number().min(0).max(1).optional(),
  matched_rule: RuleIdSchema.optional(),
  rationale: z.string().optional(),
  classified_at: IsoDateTime,
});
export type ClassificationEntry = z.infer<typeof ClassificationEntrySchema>;

// ---------------------------------------------------------------------------
// pairwise.jsonl (step 60 output)
// ---------------------------------------------------------------------------

export const PairwiseWinnerSchema = z.enum(['A', 'B', 'tie']);

export const PairwiseMarginSchema = z.enum(['decisive', 'clear', 'slight']);

export const PairwiseEntrySchema = z.object({
  run_id: RunId,
  pr: PrNumber,
  agent: AgentId, // the agent identity across both passes
  winner: PairwiseWinnerSchema,
  margin: PairwiseMarginSchema,
  reason: z.string(),
  judged_by: z.object({
    primary: z.string(),
    secondary: z.string().nullable(),
    arbiter: z.string().nullable(),
  }),
  judged_at: IsoDateTime,
});
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

export const AppliedRuleSchema = z.object({
  kind: CandidateKindSchema, // new | update | duplicate(dup is rejected, not applied)
  provisional_id: z.string(),
  final_rule_id: RuleIdSchema.optional(), // set once promoted
  updates: RuleIdSchema.optional(),
});

export const DecisionSchema = z.object({
  run_id: RunId,
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
  /** best_branch commit SHA after this decision; null if unchanged or rolled back */
  best_sha_after: z.string().regex(/^[a-f0-9]{40}$/).nullable(),
  /** populated when action === "rollback" */
  rollback_reason: z.string().optional(),
  decided_at: IsoDateTime,
});
export type Decision = z.infer<typeof DecisionSchema>;

// ---------------------------------------------------------------------------
// rules-registry.jsonl (cross-PR rule lifecycle, append-only)
// ---------------------------------------------------------------------------

export const RuleStatusSchema = z.enum(['active', 'at_risk', 'removed']);
export type RuleStatus = z.infer<typeof RuleStatusSchema>;

export const RuleRegistryEventSchema = z.enum([
  'added',
  'updated',
  'status_changed',
  'archived',
  'restored',
]);

export const RuleRegistryMetricsSchema = z.object({
  fire_count: z.number().int().nonnegative(),
  compliance_count: z.number().int().nonnegative(),
  violation_count: z.number().int().nonnegative(),
  contribution_avg: z.number(), // Δ score, pass2 − pass1
  samples: z.number().int().nonnegative(),
});
export type RuleRegistryMetrics = z.infer<typeof RuleRegistryMetricsSchema>;

/**
 * rules-registry.jsonl is append-only.
 * Each entry describes a single lifecycle event for a rule.
 * The latest entry per rule_id defines the current state.
 */
export const RuleRegistryEntrySchema = z.object({
  rule_id: RuleIdSchema,
  event: RuleRegistryEventSchema,
  at: IsoDateTime,
  source_pr: PrNumber.optional(), // PR that triggered this event
  run_id: RunId.optional(),
  /** full rule text hash (sha256) — track canonical text changes across updates */
  text_hash: Sha256Hex.optional(),
  kind: CandidateKindSchema.optional(), // new/update on creation/change events
  status: RuleStatusSchema.optional(), // present on added / status_changed / restored
  check_method: CandidateCheckMethodSchema.optional(),
  metrics_snapshot: RuleRegistryMetricsSchema.optional(),
  archive_reason: z.string().optional(),
});
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
  ScoreEntry: ScoreEntrySchema,
  ComplianceEntry: ComplianceEntrySchema,
  Candidate: CandidateSchema,
  Candidates: CandidatesSchema,
  ClassificationEntry: ClassificationEntrySchema,
  PairwiseEntry: PairwiseEntrySchema,
  Decision: DecisionSchema,
  RuleRegistryEntry: RuleRegistryEntrySchema,
} as const;

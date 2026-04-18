import { describe, expect, it } from 'vitest';

import {
  CandidatesSchema,
  ChecklistItemSchema,
  ChecklistResultSchema,
  ComplianceEntrySchema,
  DecisionSchema,
  ManifestSchema,
  PairwiseEntrySchema,
  RuleRegistryEntrySchema,
  ScoreEntrySchema,
  Schemas,
  StateEntrySchema,
  TaskPackageSchema,
} from '../contracts.ts';

const ISO = '2026-04-18T10:00:00Z';
const SHA40 = 'a'.repeat(40);
const SHA256 = 'b'.repeat(64);

describe('contracts.ts', () => {
  it('TaskPackage accepts minimal valid value with defaults', () => {
    const v = TaskPackageSchema.parse({
      run_id: 'run1',
      pr: 1,
      title: 't',
      body: 'b',
      base_ref: 'develop',
      base_sha: SHA40,
      head_ref: 'feature/x',
      reconstructed_task_prompt: 'do this',
      built_at: ISO,
    });
    expect(v.linked_issue_numbers).toEqual([]);
    expect(v.target_files_hint).toEqual([]);
    expect(v.acceptance_hints).toEqual([]);
  });

  it('TaskPackage rejects non-hex base_sha', () => {
    expect(() =>
      TaskPackageSchema.parse({
        run_id: 'r',
        pr: 1,
        title: 't',
        body: 'b',
        base_ref: 'develop',
        base_sha: 'short',
        head_ref: 'f',
        reconstructed_task_prompt: 'p',
        built_at: ISO,
      }),
    ).toThrow();
  });

  it('Manifest requires sha256 fields', () => {
    ManifestSchema.parse({
      run_id: 'r',
      pr: 1,
      agent: 'a1',
      pass: 'A',
      started_at: ISO,
      completed_at: ISO,
      exit_code: 0,
      exit_status: 'success',
      worktree: '/tmp/wt',
      head_sha: SHA40,
      session_jsonl_sha256: SHA256,
      diff_sha256: SHA256,
      checklist_result_sha256: SHA256,
      bytes_written: 100,
    });
    expect(() =>
      ManifestSchema.parse({
        run_id: 'r',
        pr: 1,
        agent: 'a1',
        pass: 'A',
        started_at: ISO,
        completed_at: ISO,
        exit_code: 0,
        exit_status: 'success',
        worktree: '/tmp/wt',
        head_sha: SHA40,
        session_jsonl_sha256: 'short',
        diff_sha256: SHA256,
        checklist_result_sha256: null,
        bytes_written: 0,
      }),
    ).toThrow(/sha256/);
  });

  it('ChecklistItem requires reason for exception verdict', () => {
    ChecklistItemSchema.parse({ rule: 'AI-GEN-001', verdict: 'compliant' });
    ChecklistItemSchema.parse({ rule: 'AI-GEN-001', verdict: 'n_a' });
    expect(() =>
      ChecklistItemSchema.parse({ rule: 'AI-GEN-001', verdict: 'exception' }),
    ).toThrow(/reason is required/);
    ChecklistItemSchema.parse({
      rule: 'AI-GEN-001',
      verdict: 'exception',
      reason: 'this case is out',
    });
  });

  it('ChecklistResult validates the full structure', () => {
    ChecklistResultSchema.parse({
      run_id: 'r',
      pr: 1,
      agent: 'a1',
      pass: 'A',
      items: [{ rule: 'FR-CSS-001', verdict: 'compliant' }],
      checklist_version: 'v1',
    });
  });

  it('ScoreEntry enforces 1..5 per dimension', () => {
    const base = {
      run_id: 'r',
      pr: 1,
      pass: 'A' as const,
      agent: 'a1',
      scores: { correctness: 3, design: 3, idiomatic: 3, fidelity: 3, discipline: 3 },
      total: 15,
      reasons: {
        correctness: 'ok',
        design: 'ok',
        idiomatic: 'ok',
        fidelity: 'ok',
        discipline: 'ok',
      },
      judged_by: { primary: 'sonnet', secondary: null, arbiter: null },
      scored_at: ISO,
    };
    ScoreEntrySchema.parse(base);
    expect(() => ScoreEntrySchema.parse({ ...base, scores: { ...base.scores, correctness: 6 } })).toThrow();
  });

  it('ComplianceEntry validates rule id format and verdict', () => {
    ComplianceEntrySchema.parse({
      run_id: 'r',
      pr: 1,
      pass: 'A',
      agent: 'a1',
      rule: 'AI-GEN-016',
      self: 'compliant',
      verdict: 'violated',
      audited_at: ISO,
    });
    expect(() =>
      ComplianceEntrySchema.parse({
        run_id: 'r',
        pr: 1,
        pass: 'A',
        agent: 'a1',
        rule: 'lowercase-bad',
        self: 'compliant',
        verdict: 'violated',
        audited_at: ISO,
      }),
    ).toThrow();
  });

  it('Candidates accepts empty array', () => {
    CandidatesSchema.parse({ run_id: 'r', pr: 1, extracted_at: ISO, candidates: [] });
  });

  it('Candidate has default check_method = agent', () => {
    const v = CandidatesSchema.parse({
      run_id: 'r',
      pr: 1,
      extracted_at: ISO,
      candidates: [
        {
          provisional_id: 'p1',
          kind: 'new',
          statement: 's',
          problem: 'p',
          rationale: 'r',
        },
      ],
    });
    expect(v.candidates[0]!.check_method).toBe('agent');
    expect(v.candidates[0]!.examples).toEqual([]);
  });

  it('Pairwise validates winner/margin enums', () => {
    PairwiseEntrySchema.parse({
      run_id: 'r',
      pr: 1,
      agent: 'a1',
      winner: 'B',
      margin: 'clear',
      reason: 'B added null-check',
      judged_by: { primary: 'sonnet', secondary: 'codex', arbiter: 'codex' },
      judged_at: ISO,
    });
    expect(() =>
      PairwiseEntrySchema.parse({
        run_id: 'r',
        pr: 1,
        agent: 'a1',
        winner: 'X',
        margin: 'clear',
        reason: 'r',
        judged_by: { primary: 's', secondary: null, arbiter: null },
        judged_at: ISO,
      }),
    ).toThrow();
  });

  it('Decision defaults applied/rejected to []', () => {
    const v = DecisionSchema.parse({
      run_id: 'r',
      pr: 1,
      action: 'adopt',
      win_count: 2,
      score_delta_avg: 0.7,
      best_sha_after: SHA40,
      decided_at: ISO,
    });
    expect(v.applied).toEqual([]);
    expect(v.rejected).toEqual([]);
  });

  it('RuleRegistryEntry validates event/status enums', () => {
    RuleRegistryEntrySchema.parse({
      rule_id: 'AI-GEN-016',
      event: 'added',
      at: ISO,
      source_pr: 74,
      run_id: 'r',
      text_hash: SHA256,
      kind: 'new',
      status: 'active',
      check_method: 'agent',
    });
  });

  it('StateEntry backward-compatible with existing state.ts usage', () => {
    StateEntrySchema.parse({ pr: 1, event: 'started', at: ISO, run_id: 'r' });
  });

  it('Schemas map matches individual exports', () => {
    expect(Schemas.TaskPackage).toBe(TaskPackageSchema);
    expect(Schemas.Manifest).toBe(ManifestSchema);
    expect(Schemas.Decision).toBe(DecisionSchema);
  });
});

import { describe, expect, it } from 'vitest';

import {
  AgentIdSchema,
  CandidatesSchema,
  ChecklistItemSchema,
  ChecklistResultSchema,
  ComplianceEntrySchema,
  DecisionSchema,
  ManifestSchema,
  PairwiseEntrySchema,
  RuleRegistryEntrySchema,
  RunIdSchema,
  ScoreEntrySchema,
  Schemas,
  StateEntrySchema,
  TaskPackageSchema,
  WorktreeAllocationSchema,
} from '../contracts.ts';

const ISO = '2026-04-18T10:00:00Z';
const SHA40 = 'a'.repeat(40);
const SHA40_B = 'b'.repeat(40);
const SHA256 = 'b'.repeat(64);
const RUN_ID = '2026-04-18-PR74-abc1234';

const validWorktrees = [
  {
    agent: 'a1',
    pass: 'A' as const,
    path: '/tmp/wt-a1',
    branch: 'feature/auto-improve-pr74-pass1-a1',
    base_sha: SHA40,
    head_sha: SHA40,
  },
];

describe('contracts: primitives', () => {
  it('RunIdSchema matches canonical format', () => {
    RunIdSchema.parse('2026-04-18-PR74-a1b2c3d');
    expect(() => RunIdSchema.parse('pr74-20260417T150000Z')).toThrow();
    expect(() => RunIdSchema.parse('random')).toThrow();
  });

  it('AgentIdSchema matches a1..a9+', () => {
    AgentIdSchema.parse('a1');
    AgentIdSchema.parse('a99');
    expect(() => AgentIdSchema.parse('claude-a1')).toThrow();
    expect(() => AgentIdSchema.parse('a0')).toThrow();
    expect(() => AgentIdSchema.parse('A1')).toThrow();
  });
});

describe('contracts: TaskPackage', () => {
  it('accepts minimal valid value', () => {
    const v = TaskPackageSchema.parse({
      run_id: RUN_ID,
      pr: 74,
      title: 't',
      body: 'b',
      base_ref: 'develop',
      base_sha: SHA40,
      head_ref: 'feature/x',
      reconstructed_task_prompt: 'do this',
      agent_ids: ['a1', 'a2', 'a3'],
      worktrees: validWorktrees,
      built_at: ISO,
    });
    expect(v.linked_issue_numbers).toEqual([]);
    expect(v.target_files_hint).toEqual([]);
    expect(v.acceptance_hints).toEqual([]);
  });

  it('rejects unknown keys (.strict)', () => {
    expect(() =>
      TaskPackageSchema.parse({
        run_id: RUN_ID,
        pr: 74,
        title: 't',
        body: 'b',
        base_ref: 'develop',
        base_sha: SHA40,
        head_ref: 'f',
        reconstructed_task_prompt: 'p',
        agent_ids: ['a1'],
        worktrees: validWorktrees,
        built_at: ISO,
        extra: 'xx',
      }),
    ).toThrow();
  });

  it('rejects non-hex base_sha', () => {
    expect(() =>
      TaskPackageSchema.parse({
        run_id: RUN_ID,
        pr: 74,
        title: 't',
        body: 'b',
        base_ref: 'develop',
        base_sha: 'short',
        head_ref: 'f',
        reconstructed_task_prompt: 'p',
        agent_ids: ['a1'],
        worktrees: validWorktrees,
        built_at: ISO,
      }),
    ).toThrow();
  });

  it('requires agent_ids to be non-empty', () => {
    expect(() =>
      TaskPackageSchema.parse({
        run_id: RUN_ID,
        pr: 74,
        title: 't',
        body: 'b',
        base_ref: 'develop',
        base_sha: SHA40,
        head_ref: 'f',
        reconstructed_task_prompt: 'p',
        agent_ids: [],
        worktrees: validWorktrees,
        built_at: ISO,
      }),
    ).toThrow();
  });
});

describe('contracts: WorktreeAllocation', () => {
  it('strict shape required', () => {
    WorktreeAllocationSchema.parse(validWorktrees[0]);
    expect(() =>
      WorktreeAllocationSchema.parse({ ...validWorktrees[0], extra: 'x' }),
    ).toThrow();
  });
});

describe('contracts: Manifest (discriminated union)', () => {
  const baseFields = {
    run_id: RUN_ID,
    pr: 74,
    agent: 'a1',
    pass: 'A' as const,
    started_at: ISO,
    completed_at: ISO,
    worktree: '/tmp/wt',
    session_jsonl_sha256: SHA256,
    diff_sha256: SHA256,
    checklist_result_sha256: SHA256,
    checklist_version: 'v1',
    bytes_written: 100,
  };

  it('success requires exit_code=0 and head_sha', () => {
    ManifestSchema.parse({ ...baseFields, exit_status: 'success', exit_code: 0, head_sha: SHA40 });
    expect(() =>
      ManifestSchema.parse({
        ...baseFields,
        exit_status: 'success',
        exit_code: 1,
        head_sha: SHA40,
      }),
    ).toThrow();
  });

  it('error requires non-zero exit_code', () => {
    ManifestSchema.parse({
      ...baseFields,
      exit_status: 'error',
      exit_code: 1,
      head_sha: null,
    });
    expect(() =>
      ManifestSchema.parse({
        ...baseFields,
        exit_status: 'error',
        exit_code: 0,
        head_sha: null,
      }),
    ).toThrow();
  });

  it('timeout requires exit_code=null', () => {
    ManifestSchema.parse({
      ...baseFields,
      exit_status: 'timeout',
      exit_code: null,
      head_sha: null,
    });
    expect(() =>
      ManifestSchema.parse({
        ...baseFields,
        exit_status: 'timeout',
        exit_code: 0,
        head_sha: null,
      }),
    ).toThrow();
  });
});

describe('contracts: Checklist', () => {
  it('ChecklistItem: exception requires reason', () => {
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

  it('ChecklistResult full structure', () => {
    ChecklistResultSchema.parse({
      run_id: RUN_ID,
      pr: 74,
      agent: 'a1',
      pass: 'A',
      items: [{ rule: 'FR-CSS-001', verdict: 'compliant' }],
      checklist_version: 'v1',
    });
  });
});

describe('contracts: Score / Compliance', () => {
  const judged_by = {
    primary: 'sonnet',
    secondary: null as string | null,
    arbiter: null as string | null,
    verdict_path: 'single' as const,
  };

  it('ScoreEntry 1..5 per dimension + version fields required', () => {
    const base = {
      run_id: RUN_ID,
      pr: 74,
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
      judged_by,
      rubric_version: 'rubric-v1',
      prompt_version: 'prompt-v1',
      scored_at: ISO,
    };
    ScoreEntrySchema.parse(base);
    expect(() =>
      ScoreEntrySchema.parse({ ...base, scores: { ...base.scores, correctness: 6 } }),
    ).toThrow();
  });

  it('ScoreEntry reasons cap at 1000 chars per dim', () => {
    const big = 'x'.repeat(1001);
    expect(() =>
      ScoreEntrySchema.parse({
        run_id: RUN_ID,
        pr: 74,
        pass: 'A',
        agent: 'a1',
        scores: { correctness: 3, design: 3, idiomatic: 3, fidelity: 3, discipline: 3 },
        total: 15,
        reasons: {
          correctness: big,
          design: '',
          idiomatic: '',
          fidelity: '',
          discipline: '',
        },
        judged_by,
        rubric_version: 'v',
        prompt_version: 'v',
        scored_at: ISO,
      }),
    ).toThrow();
  });

  it('ComplianceEntry rule id format enforced', () => {
    ComplianceEntrySchema.parse({
      run_id: RUN_ID,
      pr: 74,
      pass: 'A',
      agent: 'a1',
      rule: 'AI-GEN-016',
      self: 'compliant',
      verdict: 'violated',
      audited_at: ISO,
    });
    expect(() =>
      ComplianceEntrySchema.parse({
        run_id: RUN_ID,
        pr: 74,
        pass: 'A',
        agent: 'a1',
        rule: 'lowercase-bad',
        self: 'compliant',
        verdict: 'violated',
        audited_at: ISO,
      }),
    ).toThrow();
  });
});

describe('contracts: Candidates / Pairwise / Decision', () => {
  it('Candidate default check_method', () => {
    const v = CandidatesSchema.parse({
      run_id: RUN_ID,
      pr: 74,
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

  it('Pairwise winner/margin enums + prompt_version', () => {
    PairwiseEntrySchema.parse({
      run_id: RUN_ID,
      pr: 74,
      agent: 'a1',
      winner: 'B',
      margin: 'clear',
      reason: 'B added null-check',
      judged_by: {
        primary: 'sonnet',
        secondary: 'codex',
        arbiter: 'codex',
        verdict_path: 'arbitrated',
      },
      prompt_version: 'pw-v1',
      judged_at: ISO,
    });
    expect(() =>
      PairwiseEntrySchema.parse({
        run_id: RUN_ID,
        pr: 74,
        agent: 'a1',
        winner: 'X',
        margin: 'clear',
        reason: 'r',
        judged_by: { primary: 's', secondary: null, arbiter: null, verdict_path: 'single' },
        prompt_version: 'v',
        judged_at: ISO,
      }),
    ).toThrow();
  });

  it('Decision requires best_sha_before', () => {
    const v = DecisionSchema.parse({
      run_id: RUN_ID,
      pr: 74,
      action: 'adopt',
      win_count: 2,
      score_delta_avg: 0.7,
      best_sha_before: SHA40,
      best_sha_after: SHA40_B,
      decided_at: ISO,
    });
    expect(v.applied).toEqual([]);
    expect(v.rejected).toEqual([]);
  });
});

describe('contracts: RuleRegistryEntry (discriminated union)', () => {
  it('added requires kind/status/text_hash/source_pr/run_id/check_method', () => {
    RuleRegistryEntrySchema.parse({
      rule_id: 'AI-GEN-016',
      event: 'added',
      at: ISO,
      version_seq: 1,
      prev_hash: '',
      source_pr: 74,
      run_id: RUN_ID,
      text_hash: SHA256,
      kind: 'new',
      status: 'active',
      check_method: 'agent',
    });
  });

  it('status_changed requires metrics_snapshot', () => {
    expect(() =>
      RuleRegistryEntrySchema.parse({
        rule_id: 'AI-GEN-016',
        event: 'status_changed',
        at: ISO,
        version_seq: 2,
        prev_hash: SHA256,
        status: 'at_risk',
      }),
    ).toThrow();
    RuleRegistryEntrySchema.parse({
      rule_id: 'AI-GEN-016',
      event: 'status_changed',
      at: ISO,
      version_seq: 2,
      prev_hash: SHA256,
      status: 'at_risk',
      metrics_snapshot: {
        fire_count: 10,
        compliance_count: 5,
        violation_count: 5,
        contribution_avg: 0.1,
        samples: 10,
      },
    });
  });

  it('archived requires archive_reason and status=removed', () => {
    RuleRegistryEntrySchema.parse({
      rule_id: 'AI-GEN-016',
      event: 'archived',
      at: ISO,
      version_seq: 3,
      prev_hash: SHA256,
      status: 'removed',
      archive_reason: 'compliance < 30% for 3 cycles',
      metrics_snapshot: {
        fire_count: 10,
        compliance_count: 2,
        violation_count: 8,
        contribution_avg: -0.1,
        samples: 10,
      },
    });
    expect(() =>
      RuleRegistryEntrySchema.parse({
        rule_id: 'AI-GEN-016',
        event: 'archived',
        at: ISO,
        version_seq: 3,
        prev_hash: SHA256,
        status: 'active', // wrong
        archive_reason: 'r',
        metrics_snapshot: {
          fire_count: 0,
          compliance_count: 0,
          violation_count: 0,
          contribution_avg: 0,
          samples: 0,
        },
      }),
    ).toThrow();
  });
});

describe('contracts: StateEntry refinements', () => {
  it('step_done requires step', () => {
    expect(() =>
      StateEntrySchema.parse({ pr: 1, event: 'step_done', at: ISO, run_id: RUN_ID }),
    ).toThrow(/step is required/);
    StateEntrySchema.parse({
      pr: 1,
      event: 'step_done',
      at: ISO,
      run_id: RUN_ID,
      step: 20,
    });
  });

  it('non step_done rejects step', () => {
    expect(() =>
      StateEntrySchema.parse({
        pr: 1,
        event: 'started',
        at: ISO,
        run_id: RUN_ID,
        step: 20,
      }),
    ).toThrow(/step must only be set/);
  });

  it('valid started entry', () => {
    StateEntrySchema.parse({ pr: 1, event: 'started', at: ISO, run_id: RUN_ID });
  });
});

describe('Schemas map', () => {
  it('exports match individual schemas', () => {
    expect(Schemas.TaskPackage).toBe(TaskPackageSchema);
    expect(Schemas.Manifest).toBe(ManifestSchema);
    expect(Schemas.Decision).toBe(DecisionSchema);
    expect(Schemas.RuleRegistryEntry).toBe(RuleRegistryEntrySchema);
    expect(Schemas.WorktreeAllocation).toBe(WorktreeAllocationSchema);
  });
});

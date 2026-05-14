package step60_scorepairwise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

type Input struct {
	IO                    internalio.RunContext
	TaskPackage           *contracts.TaskPackage
	ScorableAgents        []contracts.AgentID
	RubricVersion         string
	PromptVersion         string
	RubricPath            string
	Primary               judges.Judge
	Secondary             judges.Judge
	Arbiter               judges.Judge
	PairwiseMode          judges.PairwiseMode
	PairwiseJudge         judges.PairwiseJudge
	PairwiseDecisionJudge judges.PairwiseDecisionJudge
	PairwisePromptVersion string
	CandidateRules        []judges.CandidateRule
	Now                   func() time.Time
}

var (
	ErrNoScorablePass2Agents     = errors.New("step60: no scorable pass2 agents found")
	ErrPass1ScoresIncomplete     = errors.New("step60: pass1 scores incomplete")
	ErrDuplicateComplianceRuleID = errors.New("step60: duplicate compliance rule_id")
	// ErrPass1VersionMismatch fires when step30 scores-A.jsonl / compliance-A.jsonl
	// were generated under a different rubric_version or prompt_version than
	// step60 is currently executing. Pairwise comparisons are only meaningful
	// when pass1 and pass2 share their scoring assumptions, so step60 must
	// fail closed and demand step30 rerun.
	ErrPass1VersionMismatch = errors.New("step60: pass1 score/compliance rubric_version or prompt_version does not match step60")
)

var canonicalDimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

const defaultDisagreementThreshold = 5

type scoreKey struct {
	Agent     contracts.AgentID
	Dimension contracts.Dimension
}

type complianceKey struct {
	Agent  contracts.AgentID
	RuleID string
}

type step60Paths struct {
	Lock            string
	Done            string
	RawReuse        string
	ScoresRaw       string
	ScoresFinal     string
	ComplianceRaw   string
	ComplianceFinal string
	Pairwise        string
}

type step60RawReuseMarker struct {
	CompletedAgents []contracts.AgentID             `json:"completed_agents"`
	Dimensions      []contracts.Dimension           `json:"dimensions"`
	InputHashes     contracts.Step60DoneInputHashes `json:"input_hashes"`
	RawHashes       contracts.StepDoneRawHashes     `json:"raw_hashes"`
	ResolvedAt      time.Time                       `json:"resolved_at"`
}

type scorableAgentRun struct {
	Agent             contracts.AgentID
	JudgeInput        judges.JudgeInput
	OutputSha256      string
	Pass1OutputPath   string
	Pass1OutputSha256 string
}

type finalMetadata struct {
	RunID         contracts.RunID
	Pass          int
	RubricVersion string
	PromptVersion string
	ResolvedAt    time.Time
}

type pass1ComplianceState struct {
	RuleIDs map[contracts.AgentID]map[string]struct{}
	Rows    []contracts.ComplianceEntry
}

type pass1ScoringVersions struct {
	RubricVersion string
	PromptVersion string
}

// Run scores pass2 outputs, derives pass1-vs-pass2 pairwise rows, and writes
// the step60 done marker. It returns ErrNoScorablePass2Agents when no pass2
// manifests are scorable.
func Run(ctx context.Context, in Input) error {
	in, err := applyDefaults(in)
	if err != nil {
		return err
	}

	paths, err := resolveStep60Paths(in.IO)
	if err != nil {
		return err
	}
	lock, err := internalio.AcquireFileLock(paths.Lock)
	if err != nil {
		return fmt.Errorf("step60: acquire lock: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()
	scorableRuns, err := collectScorableAgentRuns(in, declaredScorableAgents(in), len(in.ScorableAgents) > 0)
	if err != nil {
		return err
	}
	expectedAgents := scorableAgentsFromRuns(scorableRuns)
	pass1ScoresByAgent, err := loadPass1Scores(in.IO, in.RubricVersion, in.PromptVersion)
	if err != nil {
		return err
	}
	pass1Compliance, err := loadPass1ComplianceState(in.IO, in.RubricVersion, in.PromptVersion)
	if err != nil {
		return err
	}
	activeComplianceRuleIDs, err := judges.ActiveComplianceRuleIDs(in.RubricPath)
	if err != nil {
		return err
	}
	fallbackComplianceRuleIDs, err := judges.ExpectedComplianceRuleIDs(in.RubricPath)
	if err != nil {
		return err
	}
	expectedComplianceByAgent := expectedComplianceRuleIDsByAgent(
		expectedAgents,
		pass1Compliance.RuleIDs,
		activeComplianceRuleIDs,
		fallbackComplianceRuleIDs,
		in.CandidateRules,
	)
	pass2OutputHashes, err := pass2OutputHashesByAgent(scorableRuns)
	if err != nil {
		return err
	}
	inputHashes, err := step60InputHashes(pass1ScoresByAgent, pass1Compliance.Rows, pass2OutputHashes, in.CandidateRules, expectedComplianceByAgent)
	if err != nil {
		return err
	}
	resetOutputs := false
	allowRawReuse := true
	if _, err := os.Stat(paths.Done); err == nil {
		matches, rawReuseSafe, err := doneMarkerMatchesCurrentState(in.IO, paths, expectedAgents, inputHashes)
		if err != nil {
			return err
		}
		versionsMatch, err := step60VersionsMatch(paths, in.RubricVersion, in.PromptVersion, in.PairwisePromptVersion)
		if err != nil {
			return err
		}
		if matches && versionsMatch {
			return nil
		}
		allowRawReuse = rawReuseSafe
		if err := os.Remove(paths.Done); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: remove stale done marker: %w", err)
		}
		resetOutputs = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("step60: stat done marker: %w", err)
	} else {
		rawReuseSafe, err := rawReuseMarkerMatchesCurrentState(in.IO, paths, expectedAgents, inputHashes)
		if err != nil {
			return err
		}
		allowRawReuse = rawReuseSafe
		resetOutputs = true
	}
	if resetOutputs {
		if err := resetStep60Outputs(paths); err != nil {
			return err
		}
	}
	rawState, err := loadStep60RawState(paths)
	if err != nil {
		return err
	}
	request := stepio.Step60Request{
		TaskPackage:    *in.TaskPackage,
		ScorableAgents: expectedAgents,
		RubricVersion:  in.RubricVersion,
		PromptVersion:  in.PromptVersion,
	}
	if err := request.Validate(); err != nil {
		return err
	}

	resolvedAt := in.Now().UTC()
	meta := finalMetadata{
		RunID:         in.TaskPackage.RunID,
		Pass:          2,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}

	finalScores := make([]contracts.ScoreEntry, 0, len(scorableRuns)*len(canonicalDimensions))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(scorableRuns))
	pass2ScoresByAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(scorableRuns))
	completedAgents := make([]contracts.AgentID, 0, len(scorableRuns))

	for _, run := range scorableRuns {
		outputHash := pass2OutputHashes[run.Agent]
		// F18: derive the expected compliance coverage ONLY from current
		// pass1 inputs and the rubric fallback. Previously we unioned in
		// rule IDs observed in step60's own raw artifacts, which let a
		// stale raw-only rule ID keep satisfying coverage on every resume
		// — judges would be skipped and the stale evidence preserved.
		expectedCompliance := expectedComplianceByAgent[run.Agent]
		run.JudgeInput.ExpectedComplianceRuleIDs = sortedExpectedComplianceRuleIDs(expectedCompliance)
		run.JudgeInput.EnforceExpectedCompliance = true
		run.JudgeInput.CandidateRules = in.CandidateRules
		if allowRawReuse {
			if result, ok, err := tryReuseRawPanelResult(in.IO, rawState, run.Agent, outputHash, in.RubricVersion, in.PromptVersion, expectedCompliance, in.Secondary != nil); err != nil {
				return err
			} else if ok {
				agentScores, agentCompliance, err := appendPanelFinals(paths, meta, result)
				if err != nil {
					return err
				}
				completedAgents = append(completedAgents, run.Agent)
				finalScores = append(finalScores, agentScores...)
				finalCompliance = append(finalCompliance, agentCompliance...)
				pass2ScoresByAgent[run.Agent] = agentScores
				continue
			}
		}

		primaryOutput, err := scoreJudgeOutput(ctx, "primary", in.Primary, run.JudgeInput)
		if err != nil {
			return err
		}

		primaryScores, err := normalizeScores(in.IO, primaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		primaryCompliance, err := normalizeCompliance(in.IO, primaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		primaryRawScores, primaryScoreRefs, err := buildRawScores(primaryScores, outputHash, contracts.JudgeRolePrimary, nil, nil, meta.ResolvedAt)
		if err != nil {
			return err
		}

		var secondaryScores map[contracts.Dimension]contracts.ScoreEntry
		var secondaryCompliance map[string]contracts.ComplianceEntry
		var secondaryRawScores []contracts.RawScoreEntry
		var secondaryScoreRefs map[contracts.Dimension]*contracts.RawJudgeRef
		var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
		var arbiterCompliance map[string]contracts.ComplianceEntry
		if in.Secondary != nil {
			secondaryOutput, err := scoreJudgeOutput(ctx, "secondary", in.Secondary, run.JudgeInput)
			if err != nil {
				return err
			}
			secondaryScores, err = normalizeScores(in.IO, secondaryOutput.Scores, in.RubricVersion, in.PromptVersion)
			if err != nil {
				return err
			}
			secondaryCompliance, err = normalizeCompliance(in.IO, secondaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
			if err != nil {
				return err
			}
			secondaryRawScores, secondaryScoreRefs, err = buildRawScores(secondaryScores, outputHash, contracts.JudgeRoleSecondary, nil, nil, meta.ResolvedAt)
			if err != nil {
				return err
			}

			complianceDisagreements := disputedComplianceRuleIDs(primaryCompliance, secondaryCompliance)
			scoreNeedsArbiter, err := scorecore.PanelDisagrees(primaryRawScores, secondaryRawScores, nil, nil, defaultDisagreementThreshold)
			if err != nil {
				return err
			}
			if in.Arbiter != nil && (scoreNeedsArbiter || len(complianceDisagreements) > 0) {
				arbiterInput := run.JudgeInput
				arbiterInput.ExpectedComplianceRuleIDs = append([]string(nil), complianceDisagreements...)
				arbiterOutput, err := scoreJudgeOutput(ctx, "arbiter", in.Arbiter, arbiterInput)
				if err != nil {
					return err
				}
				arbiterScores, err = normalizeScores(in.IO, arbiterOutput.Scores, in.RubricVersion, in.PromptVersion)
				if err != nil {
					return err
				}
				arbiterCompliance, err = normalizeCompliance(in.IO, arbiterOutput.Compliance, in.RubricVersion, in.PromptVersion)
				if err != nil {
					return err
				}
			}
		}

		agentScores, err := emitScores(paths, meta, run.Agent, outputHash, primaryScores, secondaryScores, arbiterScores, primaryRawScores, secondaryRawScores, primaryScoreRefs, secondaryScoreRefs)
		if err != nil {
			return err
		}
		agentCompliance, err := emitCompliance(paths, meta, run.Agent, outputHash, primaryCompliance, secondaryCompliance, arbiterCompliance)
		if err != nil {
			return err
		}

		if len(agentScores) != len(canonicalDimensions) {
			return fmt.Errorf("step60: incomplete score set for agent=%s: got=%d want=%d", run.Agent, len(agentScores), len(canonicalDimensions))
		}

		completedAgents = append(completedAgents, run.Agent)
		finalScores = append(finalScores, agentScores...)
		finalCompliance = append(finalCompliance, agentCompliance...)
		pass2ScoresByAgent[run.Agent] = agentScores
	}

	pairwiseEntries := make([]contracts.PairwiseEntry, 0, len(completedAgents))
	pairwiseEntries, err = makePairwiseEntries(ctx, in, completedRuns(scorableRuns, completedAgents), pass1ScoresByAgent, pass2ScoresByAgent, resolvedAt)
	if err != nil {
		return err
	}
	for _, entry := range pairwiseEntries {
		if err := appendJSONLWithParentDirSync(paths.Pairwise, entry); err != nil {
			return fmt.Errorf("step60: append pairwise row for agent=%s: %w", entry.AgentA, err)
		}
	}

	if len(finalScores) != len(completedAgents)*len(canonicalDimensions) {
		return fmt.Errorf("step60: final score cardinality mismatch: scores=%d completed_agents=%d", len(finalScores), len(completedAgents))
	}
	if len(pairwiseEntries) != len(completedAgents) {
		return fmt.Errorf("step60: final pairwise cardinality mismatch: pairwise=%d completed_agents=%d", len(pairwiseEntries), len(completedAgents))
	}

	scoresFinalHash, err := hashFinalScores(finalScores)
	if err != nil {
		return fmt.Errorf("step60: hash scores final: %w", err)
	}
	complianceFinalHash, err := hashFinalCompliance(finalCompliance)
	if err != nil {
		return fmt.Errorf("step60: hash compliance final: %w", err)
	}
	pairwiseFinalHash, err := hashFinalPairwise(pairwiseEntries)
	if err != nil {
		return fmt.Errorf("step60: hash pairwise final: %w", err)
	}
	scoresRawHash, err := hashReducedRawScoresFile(in.IO, paths.ScoresRaw)
	if err != nil {
		return fmt.Errorf("step60: hash scores raw: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(in.IO, paths.ComplianceRaw)
	if err != nil {
		return fmt.Errorf("step60: hash compliance raw: %w", err)
	}

	rawHashes := contracts.StepDoneRawHashes{
		ScoresRaw:     scoresRawHash,
		ComplianceRaw: complianceRawHash,
	}
	rawReuseMarker := step60RawReuseMarker{
		CompletedAgents: completedAgents,
		Dimensions:      append([]contracts.Dimension(nil), canonicalDimensions...),
		InputHashes:     inputHashes,
		RawHashes:       rawHashes,
		ResolvedAt:      resolvedAt,
	}
	if err := internalio.WriteJSONAtomic(paths.RawReuse, rawReuseMarker); err != nil {
		return fmt.Errorf("step60: write raw reuse marker: %w", err)
	}

	marker := contracts.Step60DoneMarker{
		CompletedAgents: completedAgents,
		Dimensions:      append([]contracts.Dimension(nil), canonicalDimensions...),
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(len(completedAgents) * len(canonicalDimensions)),
			Compliance: int64(len(finalCompliance)),
			Pairwise:   int64(len(pairwiseEntries)),
		},
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     scoresFinalHash,
			ComplianceFinal: complianceFinalHash,
			PairwiseFinal:   pairwiseFinalHash,
		},
		RawHashes:   rawHashes,
		InputHashes: inputHashes,
		ResolvedAt:  resolvedAt,
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(paths.Done, marker); err != nil {
		return fmt.Errorf("step60: write done marker: %w", err)
	}
	return nil
}

package step40_classify

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func buildCandidates(runIO internalio.RunContext, now time.Time, scores []contracts.ScoreEntry, compliance []contracts.ComplianceEntry, issues []contracts.IssueEntry, registry []contracts.RuleRegistryEntry, registryBase string) ([]builtCandidate, error) {
	latestScores := scorecore.CollapseFinalScores(scores)
	latestCompliance := scorecore.CollapseFinalCompliance(compliance)
	latestIssues := scorecore.CollapseIssues(issues)
	if len(latestScores) == 0 {
		return nil, errors.New("step40_classify: missing or incomplete step30 inputs")
	}

	violations := collectViolations(latestCompliance)
	activeRules, err := activeRulesFromRegistry(registry)
	if err != nil {
		return nil, err
	}
	activeRuleBodies, err := activeRuleBodiesFromRegistry(registry, registryBase, activeRules)
	if err != nil {
		return nil, err
	}
	ruleIDs := make([]string, 0, len(violations))
	for ruleID := range violations {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	candidates := make([]builtCandidate, 0, len(ruleIDs))
	for idx, ruleID := range ruleIDs {
		evidence, err := collectCandidateEvidence(runIO, ruleID, latestCompliance, latestScores)
		if err != nil {
			return nil, err
		}
		candidate, ok, err := buildCandidate(runIO.RunID, now, idx+1, ruleID, violations[ruleID], activeRules[ruleID], activeRuleBodies, evidence)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}

	explicitIssues := collectIssueEvidence(latestIssues)
	issueRuleIDs := make([]string, 0, len(explicitIssues))
	for ruleID := range explicitIssues {
		issueRuleIDs = append(issueRuleIDs, ruleID)
	}
	sort.Strings(issueRuleIDs)
	for _, ruleID := range issueRuleIDs {
		candidate, ok, err := buildExplicitIssueCandidate(runIO.RunID, now, len(candidates)+1, ruleID, activeRules[ruleID], activeRuleBodies, explicitIssues[ruleID])
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(explicitIssues) == 0 {
		scoreConcerns, err := collectScoreConcernEvidence(runIO, latestScores, collectViolatingAgents(latestCompliance))
		if err != nil {
			return nil, err
		}
		scoreConcernRuleIDs := make([]string, 0, len(scoreConcerns))
		for ruleID := range scoreConcerns {
			scoreConcernRuleIDs = append(scoreConcernRuleIDs, ruleID)
		}
		sort.Strings(scoreConcernRuleIDs)
		for _, ruleID := range scoreConcernRuleIDs {
			candidate, ok, err := buildScoreConcernCandidate(runIO.RunID, now, len(candidates)+1, ruleID, activeRules[ruleID], activeRuleBodies, scoreConcerns[ruleID])
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func buildCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, violationCount int, existsInRegistry bool, activeRuleBodies map[string]string, evidence candidateEvidence) (builtCandidate, bool, error) {
	if len(evidence.Compliance) == 0 && len(evidence.Scores) == 0 && len(evidence.Issues) == 0 {
		return builtCandidate{}, false, nil
	}
	return buildLessonCandidate(candidateBuildSpec{
		runID:            runID,
		now:              now,
		index:            index,
		ruleID:           ruleID,
		problem:          fmt.Sprintf("Pass1 recorded %d violation(s) for rule %s.", violationCount, ruleID),
		metadata:         lessonMetadataForEvidence(ruleID, evidence),
		evidence:         evidence,
		existsInRegistry: existsInRegistry,
		activeRuleBodies: activeRuleBodies,
	})
}

func buildScoreConcernCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, existsInRegistry bool, activeRuleBodies map[string]string, evidence candidateEvidence) (builtCandidate, bool, error) {
	if len(evidence.Scores) == 0 && len(evidence.Issues) == 0 {
		return builtCandidate{}, false, nil
	}
	return buildLessonCandidate(candidateBuildSpec{
		runID:            runID,
		now:              now,
		index:            index,
		ruleID:           ruleID,
		problem:          fmt.Sprintf("Pass1 recorded score concern(s) for %s.", ruleID),
		metadata:         lessonMetadataForEvidence(ruleID, evidence),
		evidence:         evidence,
		existsInRegistry: existsInRegistry,
		activeRuleBodies: activeRuleBodies,
	})
}

func buildExplicitIssueCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, existsInRegistry bool, activeRuleBodies map[string]string, issue issueEvidence) (builtCandidate, bool, error) {
	evidence := candidateEvidence{
		Issues:    issue.Issues,
		Guidance:  issue.Guidance,
		Checklist: issue.ChecklistItem,
	}
	if len(evidence.Issues) == 0 {
		return builtCandidate{}, false, nil
	}
	return buildLessonCandidate(candidateBuildSpec{
		runID:   runID,
		now:     now,
		index:   index,
		ruleID:  ruleID,
		problem: fmt.Sprintf("Pass1 judge reported %s issue(s) for %s.", issue.Severity, ruleID),
		metadata: lessons.Metadata{
			Status:     lessons.StatusActive,
			Severity:   issue.Severity,
			Confidence: lessons.ConfidenceMedium,
			Category:   issue.Category,
		},
		evidence:         evidence,
		existsInRegistry: existsInRegistry,
		activeRuleBodies: activeRuleBodies,
	})
}

type candidateBuildSpec struct {
	runID            contracts.RunID
	now              time.Time
	index            int
	ruleID           string
	problem          string
	metadata         lessons.Metadata
	evidence         candidateEvidence
	existsInRegistry bool
	activeRuleBodies map[string]string
}

func buildLessonCandidate(spec candidateBuildSpec) (builtCandidate, bool, error) {
	candidateID := fmt.Sprintf("cand-%s-%03d", spec.runID, spec.index)
	lessonID := lessonIDFromSource(spec.ruleID)
	title := fmt.Sprintf("Experiment lesson for %s", lessonID)
	rationale := candidateRationale(spec.ruleID, spec.evidence)
	checklistItem := checklistItemForEvidence(spec.ruleID, spec.evidence)
	bodyPath := filepath.Join(experimentLessonsDirPath, lessonID+".md")
	draftBody := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          spec.problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, spec.metadata, checklistItem, spec.evidence)

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if matchedRuleID, matchedScore := bestDuplicateMatch(draftBody, spec.activeRuleBodies); matchedRuleID != "" && matchedScore >= 0.9 {
		kind = contracts.CandidateKindDuplicate
		targetRuleID = matchedRuleID
		similarity = int(matchedScore * 100)
	} else if spec.existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = spec.ruleID
		similarity = 90
	}
	body := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          spec.problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, spec.metadata, checklistItem, spec.evidence)
	bodySha256 := sha256Hex([]byte(body))

	candidate := contracts.Candidate{
		CandidateID:        candidateID,
		Kind:               kind,
		TargetRuleID:       targetRuleID,
		Title:              title,
		Problem:            spec.problem,
		Rationale:          rationale,
		ProposedBodyPath:   bodyPath,
		ProposedBodySha256: bodySha256,
	}
	if err := candidate.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	classification := contracts.ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           spec.runID,
		CandidateID:     candidateID,
		Kind:            kind,
		SimilarityScore: similarity,
		MatchedRuleID:   targetRuleID,
		Rationale:       rationale,
		ClassifiedAt:    spec.now,
	}
	if err := classification.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	return builtCandidate{
		Candidate: candidate,
		Body:      body,
		Lesson: lessons.Lesson{
			ID:            lessonID,
			Metadata:      spec.metadata,
			ChecklistItem: checklistItem,
		},
		Classification: classification,
	}, true, nil
}

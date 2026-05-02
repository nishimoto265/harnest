package step40_classify

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

const maxStep40Candidates = 10

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
	explicitIssues := collectIssueEvidence(latestIssues)
	emittedRuleIDs := make(map[string]struct{}, len(ruleIDs)+len(explicitIssues))

	candidates := make([]builtCandidate, 0, len(ruleIDs))
	for idx, ruleID := range ruleIDs {
		evidence, err := collectCandidateEvidence(runIO, ruleID, latestCompliance, latestScores)
		if err != nil {
			return nil, err
		}
		if issue, ok := explicitIssues[ruleID]; ok {
			evidence = mergeIssueIntoCandidateEvidence(evidence, issue)
		}
		candidate, ok, err := buildCandidate(runIO.RunID, now, idx+1, ruleID, violations[ruleID], activeRules[ruleID], activeRuleBodies, evidence)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		emittedRuleIDs[ruleID] = struct{}{}
		candidates = append(candidates, candidate)
	}

	issueRuleIDs := make([]string, 0, len(explicitIssues))
	for ruleID := range explicitIssues {
		if _, emitted := emittedRuleIDs[ruleID]; emitted {
			continue
		}
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
	return selectStep40Candidates(candidates, maxStep40Candidates), nil
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
	candidateID := fmt.Sprintf("cand-%s-%03d", strings.ToLower(string(spec.runID)), spec.index)
	lessonID := lessonIDFromSource(spec.ruleID)
	title := fmt.Sprintf("Experiment lesson for %s", lessonID)
	rationale := candidateRationale(spec.ruleID, spec.evidence)
	checklistItem := checklistItemForEvidence(spec.ruleID, spec.evidence)
	bodyPath := experimentLessonPath(lessonID)
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

func mergeIssueIntoCandidateEvidence(evidence candidateEvidence, issue issueEvidence) candidateEvidence {
	evidence.Issues = uniqueSortedStrings(append(evidence.Issues, issue.Issues...))
	evidence.Guidance = uniqueSortedStrings(append(evidence.Guidance, issue.Guidance...))
	if evidence.Checklist == "" {
		evidence.Checklist = issue.ChecklistItem
	}
	return evidence
}

func selectStep40Candidates(candidates []builtCandidate, limit int) []builtCandidate {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	type rankedCandidate struct {
		candidate builtCandidate
		index     int
	}
	ranked := make([]rankedCandidate, len(candidates))
	for i, candidate := range candidates {
		ranked[i] = rankedCandidate{candidate: candidate, index: i}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		left := ranked[i]
		right := ranked[j]
		if severityRankForLesson(left.candidate.Lesson.Metadata.Severity) != severityRankForLesson(right.candidate.Lesson.Metadata.Severity) {
			return severityRankForLesson(left.candidate.Lesson.Metadata.Severity) < severityRankForLesson(right.candidate.Lesson.Metadata.Severity)
		}
		if evidenceWeight(left.candidate.Body) != evidenceWeight(right.candidate.Body) {
			return evidenceWeight(left.candidate.Body) > evidenceWeight(right.candidate.Body)
		}
		return left.index < right.index
	})
	selected := ranked[:limit]
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].index < selected[j].index
	})
	out := make([]builtCandidate, len(selected))
	for i, item := range selected {
		out[i] = item.candidate
	}
	return out
}

func evidenceWeight(body string) int {
	return strings.Count(body, "- compliance:")*3 +
		strings.Count(body, "- issue:")*2 +
		strings.Count(body, "- scoring:")
}

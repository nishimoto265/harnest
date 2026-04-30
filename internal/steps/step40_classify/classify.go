package step40_classify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
	"github.com/nishimoto265/auto-improve/internal/registryview"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

const (
	candidatesJSONPath       = "40/candidates.json"
	classificationJSONLPath  = "40/classification.jsonl"
	experimentLessonsDirPath = "40/experiment/lessons"
	experimentChecklistPath  = "40/experiment/checklist.md"
	scoresPath               = "30/scores-A.jsonl"
	compliancePath           = "30/compliance-A.jsonl"
	issuesPath               = "30/issues-A.jsonl"
)

var ErrTaskPackageRequired = errors.New("step40_classify: task package is required")

type Config struct {
	IO           internalio.RunContext
	RegistryPath string
	TaskPackage  *contracts.TaskPackage
	Now          func() time.Time
}

type builtCandidate struct {
	Candidate      contracts.Candidate
	Body           string
	Lesson         lessons.Lesson
	Classification contracts.ClassificationEntry
}

func Run(ctx context.Context, cfg Config) (*contracts.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if valid, err := step30Ready(cfg.IO, cfg.TaskPackage); err != nil {
		return nil, err
	} else if !valid {
		return nil, errors.New("step40_classify: step30 done.marker is missing or invalid")
	}

	scores, err := readJSONLAt[contracts.ScoreEntry](cfg.IO, scoresPath)
	if err != nil {
		return nil, err
	}
	compliance, err := readJSONLAt[contracts.ComplianceEntry](cfg.IO, compliancePath)
	if err != nil {
		return nil, err
	}
	issues, err := readOptionalJSONLAt[contracts.IssueEntry](cfg.IO, issuesPath)
	if err != nil {
		return nil, err
	}
	registry, err := internalio.RegistryEntries(cfg.registryPath())
	if err != nil {
		return nil, err
	}

	createdAt := cfg.now()
	built, err := buildCandidates(cfg.IO, createdAt, scores, compliance, issues, registry, filepath.Dir(cfg.registryPath()))
	if err != nil {
		return nil, err
	}
	items := make([]contracts.Candidate, 0, len(built))
	classifications := make([]contracts.ClassificationEntry, 0, len(built))
	for _, item := range built {
		items = append(items, item.Candidate)
		classifications = append(classifications, item.Classification)
	}

	if err := writeCandidateBodies(cfg.IO, built); err != nil {
		return nil, err
	}
	if err := writeExperimentChecklist(cfg.IO, built); err != nil {
		return nil, err
	}
	if err := writeClassificationJSONL(cfg.IO, classifications); err != nil {
		return nil, err
	}

	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          cfg.IO.RunID,
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
		CreatedAt:      createdAt,
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}

	candidatesPath, err := cfg.IO.ResolveRunRelative(candidatesJSONPath)
	if err != nil {
		return nil, err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (cfg Config) validate() error {
	if cfg.TaskPackage == nil {
		return ErrTaskPackageRequired
	}
	if err := cfg.TaskPackage.Validate(); err != nil {
		return err
	}
	if cfg.TaskPackage.RunID != cfg.IO.RunID {
		return fmt.Errorf("step40_classify: task package run_id mismatch: task_package=%s io=%s", cfg.TaskPackage.RunID, cfg.IO.RunID)
	}
	return contracts.EnsureCleanAbsolutePath(cfg.registryPath())
}

func (cfg Config) now() time.Time {
	if cfg.Now == nil {
		return time.Now().UTC()
	}
	return cfg.Now().UTC()
}

func (cfg Config) registryPath() string {
	if cfg.RegistryPath != "" {
		return cfg.RegistryPath
	}
	return cfg.IO.RulesRegistryPath()
}

func readJSONLAt[T any](runIO internalio.RunContext, rel string) ([]T, error) {
	path, err := runIO.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("step40_classify: missing step30 artifact: %s", rel)
		}
		return nil, err
	}
	return internalio.ReadJSONL[T](path)
}

func readOptionalJSONLAt[T any](runIO internalio.RunContext, rel string) ([]T, error) {
	path, err := runIO.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return internalio.ReadJSONL[T](path)
}

func step30Ready(runIO internalio.RunContext, pkg *contracts.TaskPackage) (bool, error) {
	markerPath, err := runIO.ResolveRunRelative("30/done.marker")
	if err != nil {
		return false, err
	}
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	if err != nil {
		return false, nil
	}
	if err := marker.Validate(); err != nil {
		return false, nil
	}
	expectedAgents, known, err := currentPass1ScorableAgents(runIO, pkg)
	if err != nil {
		return false, err
	}
	if known && !slices.Equal(marker.CompletedAgents, expectedAgents) {
		return false, nil
	}
	scoreFinal, err := runIO.ResolveRunRelative(scoresPath)
	if err != nil {
		return false, err
	}
	complianceFinal, err := runIO.ResolveRunRelative(compliancePath)
	if err != nil {
		return false, err
	}
	scoreRaw, err := runIO.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return false, err
	}
	complianceRaw, err := runIO.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return false, err
	}
	return scorecore.VerifyStep30DoneMarker(runIO, scorecore.Step30MarkerPaths{
		ScoreFinal:      scoreFinal,
		ComplianceFinal: complianceFinal,
		ScoreRaw:        scoreRaw,
		ComplianceRaw:   complianceRaw,
	})
}

func currentPass1ScorableAgents(runIO internalio.RunContext, pkg *contracts.TaskPackage) ([]contracts.AgentID, bool, error) {
	if pkg == nil {
		return nil, false, ErrTaskPackageRequired
	}
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees)/2)
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	pass1Agents := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees)/2)
	manifestCount := 0
	for _, wt := range pkg.Worktrees {
		if wt.Pass != 1 {
			continue
		}
		if _, dup := pass1Agents[wt.Agent]; dup {
			continue
		}
		pass1Agents[wt.Agent] = struct{}{}
		if _, dup := seen[wt.Agent]; dup {
			continue
		}
		manifestPath, err := runIO.ManifestPath(1, wt.Agent)
		if err != nil {
			return nil, false, err
		}
		if _, err := os.Stat(manifestPath); err == nil {
			manifestCount++
		} else if err != nil && !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("step40_classify: stat pass1 manifest for agent=%s: %w", wt.Agent, err)
		}
		manifest, err := internalio.LoadScorableManifest(runIO, 1, wt.Agent)
		if err != nil {
			if errors.Is(err, internalio.ErrNotScorable) || os.IsNotExist(err) {
				continue
			}
			return nil, false, fmt.Errorf("step40_classify: load pass1 manifest for agent=%s: %w", wt.Agent, err)
		}
		if manifest == nil {
			continue
		}
		seen[wt.Agent] = struct{}{}
		agents = append(agents, wt.Agent)
	}
	if len(pass1Agents) > 0 && manifestCount == 0 {
		return nil, false, errors.New("step40_classify: pass1 worktrees exist but no pass1 manifests are resolvable")
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents, manifestCount > 0, nil
}

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

func collectViolations(entries []contracts.ComplianceEntry) map[string]int {
	violations := make(map[string]int)
	for _, entry := range entries {
		if !isViolationVerdict(entry.Verdict) {
			continue
		}
		violations[entry.RuleID]++
	}
	return violations
}

func collectIssueEvidence(entries []contracts.IssueEntry) map[string]issueEvidence {
	collected := make(map[string]issueEvidence)
	seen := make(map[string]struct{})
	for _, entry := range entries {
		sourceID := issueSourceID(entry)
		line := fmt.Sprintf("%s/%s/%s: %s", entry.Agent, entry.JudgeRole, entry.Severity, normalizeEvidenceText(entry.Evidence))
		seenKey := sourceID + "\x00" + line
		if _, exists := seen[seenKey]; exists {
			continue
		}
		seen[seenKey] = struct{}{}

		evidence := collected[sourceID]
		if evidence.Category == "" {
			evidence.Category = normalizeIssueCategory(entry.Category)
		}
		if evidence.Severity == "" || severityRankForIssue(entry.Severity) < severityRankForLesson(evidence.Severity) {
			evidence.Severity = issueSeverityToLesson(entry.Severity)
		}
		if evidence.ChecklistItem == "" {
			evidence.ChecklistItem = entry.ChecklistItem
		}
		evidence.Issues = append(evidence.Issues, line)
		if strings.TrimSpace(entry.ProposedLesson) != "" {
			evidence.Guidance = append(evidence.Guidance, normalizeEvidenceText(entry.ProposedLesson))
		}
		collected[sourceID] = evidence
	}
	for sourceID, evidence := range collected {
		collected[sourceID] = finalizeIssueEvidence(evidence)
	}
	return mergeSimilarIssueEvidence(collected)
}

func issueSourceID(entry contracts.IssueEntry) string {
	return "issue-" + lessonIDFromSource(entry.Category+"-"+entry.Title)
}

func mergeSimilarIssueEvidence(collected map[string]issueEvidence) map[string]issueEvidence {
	if len(collected) < 2 {
		return collected
	}
	sourceIDs := make([]string, 0, len(collected))
	for sourceID := range collected {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)

	merged := make(map[string]issueEvidence, len(collected))
	mergedIDs := make([]string, 0, len(collected))
	for _, sourceID := range sourceIDs {
		evidence := collected[sourceID]
		mergedInto := ""
		for _, existingID := range mergedIDs {
			if similarIssueEvidence(existingID, merged[existingID], sourceID, evidence) {
				mergedInto = existingID
				break
			}
		}
		if mergedInto == "" {
			merged[sourceID] = evidence
			mergedIDs = append(mergedIDs, sourceID)
			continue
		}
		merged[mergedInto] = finalizeIssueEvidence(combineIssueEvidence(merged[mergedInto], evidence))
	}
	return merged
}

func similarIssueEvidence(leftID string, left issueEvidence, rightID string, right issueEvidence) bool {
	leftText := issueMergeText(leftID, left)
	rightText := issueMergeText(rightID, right)
	if strings.TrimSpace(leftText) == "" || strings.TrimSpace(rightText) == "" {
		return false
	}
	return tokenSetSimilarity(leftText, rightText) >= 0.35
}

func issueMergeText(sourceID string, evidence issueEvidence) string {
	parts := []string{sourceID, evidence.ChecklistItem}
	parts = append(parts, evidence.Guidance...)
	return strings.Join(parts, " ")
}

func combineIssueEvidence(left, right issueEvidence) issueEvidence {
	out := left
	if out.Category == "" || out.Category == "judge-issue" {
		out.Category = right.Category
	}
	if out.ChecklistItem == "" {
		out.ChecklistItem = right.ChecklistItem
	}
	if severityRankForLesson(right.Severity) < severityRankForLesson(out.Severity) {
		out.Severity = right.Severity
		if right.Category != "" {
			out.Category = right.Category
		}
	}
	out.Issues = append(out.Issues, right.Issues...)
	out.Guidance = append(out.Guidance, right.Guidance...)
	return out
}

func finalizeIssueEvidence(evidence issueEvidence) issueEvidence {
	sort.Strings(evidence.Issues)
	evidence.Guidance = uniqueSortedStrings(evidence.Guidance)
	if len(evidence.Issues) > 5 {
		evidence.Issues = evidence.Issues[:5]
	}
	if len(evidence.Guidance) > 5 {
		evidence.Guidance = evidence.Guidance[:5]
	}
	if evidence.Severity == "" {
		evidence.Severity = lessons.SeverityMedium
	}
	if evidence.Category == "" {
		evidence.Category = "judge-issue"
	}
	return evidence
}

func normalizeIssueCategory(value string) string {
	value = normalizeEvidenceText(value)
	if value == "" {
		return "judge-issue"
	}
	return value
}

func issueSeverityToLesson(severity contracts.IssueSeverity) lessons.Severity {
	switch severity {
	case contracts.IssueSeverityCritical:
		return lessons.SeverityCritical
	case contracts.IssueSeverityHigh:
		return lessons.SeverityHigh
	case contracts.IssueSeverityMedium:
		return lessons.SeverityMedium
	case contracts.IssueSeverityLow:
		return lessons.SeverityLow
	default:
		return lessons.SeverityMedium
	}
}

func severityRankForIssue(severity contracts.IssueSeverity) int {
	return severityRankForLesson(issueSeverityToLesson(severity))
}

func severityRankForLesson(severity lessons.Severity) int {
	switch severity {
	case lessons.SeverityCritical:
		return 0
	case lessons.SeverityHigh:
		return 1
	case lessons.SeverityMedium:
		return 2
	case lessons.SeverityLow:
		return 3
	default:
		return 4
	}
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func isViolationVerdict(verdict contracts.ComplianceVerdict) bool {
	switch verdict {
	case contracts.ComplianceVerdictViolated, contracts.ComplianceVerdictInvalidException, contracts.ComplianceVerdictMissed:
		return true
	default:
		return false
	}
}

func collectViolatingAgents(entries []contracts.ComplianceEntry) map[contracts.AgentID]struct{} {
	agents := make(map[contracts.AgentID]struct{})
	for _, entry := range entries {
		if !isViolationVerdict(entry.Verdict) {
			continue
		}
		agents[entry.Agent] = struct{}{}
	}
	return agents
}

func activeRulesFromRegistry(entries []contracts.RuleRegistryEntry) (map[string]bool, error) {
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(states))
	for ruleID, state := range registryview.Active(states) {
		active[ruleID] = state.Exists
	}
	return active, nil
}

type candidateEvidence struct {
	Compliance []string
	Scores     []string
	Issues     []string
	Guidance   []string
	Checklist  string
}

type issueEvidence struct {
	Severity      lessons.Severity
	Category      string
	ChecklistItem string
	Issues        []string
	Guidance      []string
}

func buildCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, violationCount int, existsInRegistry bool, activeRuleBodies map[string]string, evidence candidateEvidence) (builtCandidate, bool, error) {
	if len(evidence.Compliance) == 0 && len(evidence.Scores) == 0 && len(evidence.Issues) == 0 {
		return builtCandidate{}, false, nil
	}
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	lessonID := lessonIDFromSource(ruleID)
	title := fmt.Sprintf("Experiment lesson for %s", lessonID)
	problem := fmt.Sprintf("Pass1 recorded %d violation(s) for rule %s.", violationCount, ruleID)
	rationale := candidateRationale(ruleID, evidence)
	metadata := lessonMetadataForEvidence(ruleID, evidence)
	checklistItem := checklistItemForEvidence(ruleID, evidence)

	bodyPath := filepath.Join(experimentLessonsDirPath, lessonID+".md")
	draftBody := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if matchedRuleID, matchedScore := bestDuplicateMatch(draftBody, activeRuleBodies); matchedRuleID != "" && matchedScore >= 0.9 {
		kind = contracts.CandidateKindDuplicate
		targetRuleID = matchedRuleID
		similarity = int(matchedScore * 100)
	} else if existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = ruleID
		similarity = 90
	}
	body := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)
	bodySha256 := sha256Hex([]byte(body))

	candidate := contracts.Candidate{
		CandidateID:        candidateID,
		Kind:               kind,
		TargetRuleID:       targetRuleID,
		Title:              title,
		Problem:            problem,
		Rationale:          rationale,
		ProposedBodyPath:   bodyPath,
		ProposedBodySha256: bodySha256,
	}
	if err := candidate.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	classification := contracts.ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           runID,
		CandidateID:     candidateID,
		Kind:            kind,
		SimilarityScore: similarity,
		MatchedRuleID:   targetRuleID,
		Rationale:       rationale,
		ClassifiedAt:    now,
	}
	if err := classification.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	return builtCandidate{
		Candidate: candidate,
		Body:      body,
		Lesson: lessons.Lesson{
			ID:            lessonID,
			Metadata:      metadata,
			ChecklistItem: checklistItem,
		},
		Classification: classification,
	}, true, nil
}

func buildScoreConcernCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, existsInRegistry bool, activeRuleBodies map[string]string, evidence candidateEvidence) (builtCandidate, bool, error) {
	if len(evidence.Scores) == 0 && len(evidence.Issues) == 0 {
		return builtCandidate{}, false, nil
	}
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	lessonID := lessonIDFromSource(ruleID)
	title := fmt.Sprintf("Experiment lesson for %s", lessonID)
	problem := fmt.Sprintf("Pass1 recorded score concern(s) for %s.", ruleID)
	rationale := candidateRationale(ruleID, evidence)
	metadata := lessonMetadataForEvidence(ruleID, evidence)
	checklistItem := checklistItemForEvidence(ruleID, evidence)
	bodyPath := filepath.Join(experimentLessonsDirPath, lessonID+".md")
	draftBody := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if matchedRuleID, matchedScore := bestDuplicateMatch(draftBody, activeRuleBodies); matchedRuleID != "" && matchedScore >= 0.9 {
		kind = contracts.CandidateKindDuplicate
		targetRuleID = matchedRuleID
		similarity = int(matchedScore * 100)
	} else if existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = ruleID
		similarity = 90
	}
	body := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)
	bodySha256 := sha256Hex([]byte(body))

	candidate := contracts.Candidate{
		CandidateID:        candidateID,
		Kind:               kind,
		TargetRuleID:       targetRuleID,
		Title:              title,
		Problem:            problem,
		Rationale:          rationale,
		ProposedBodyPath:   bodyPath,
		ProposedBodySha256: bodySha256,
	}
	if err := candidate.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	classification := contracts.ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           runID,
		CandidateID:     candidateID,
		Kind:            kind,
		SimilarityScore: similarity,
		MatchedRuleID:   targetRuleID,
		Rationale:       rationale,
		ClassifiedAt:    now,
	}
	if err := classification.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	return builtCandidate{
		Candidate: candidate,
		Body:      body,
		Lesson: lessons.Lesson{
			ID:            lessonID,
			Metadata:      metadata,
			ChecklistItem: checklistItem,
		},
		Classification: classification,
	}, true, nil
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
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	lessonID := lessonIDFromSource(ruleID)
	title := fmt.Sprintf("Experiment lesson for %s", lessonID)
	problem := fmt.Sprintf("Pass1 judge reported %s issue(s) for %s.", issue.Severity, ruleID)
	rationale := candidateRationale(ruleID, evidence)
	metadata := lessons.Metadata{
		Status:     lessons.StatusActive,
		Severity:   issue.Severity,
		Confidence: lessons.ConfidenceMedium,
		Category:   issue.Category,
	}
	checklistItem := checklistItemForEvidence(ruleID, evidence)
	bodyPath := filepath.Join(experimentLessonsDirPath, lessonID+".md")
	draftBody := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if matchedRuleID, matchedScore := bestDuplicateMatch(draftBody, activeRuleBodies); matchedRuleID != "" && matchedScore >= 0.9 {
		kind = contracts.CandidateKindDuplicate
		targetRuleID = matchedRuleID
		similarity = int(matchedScore * 100)
	} else if existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = ruleID
		similarity = 90
	}
	body := experimentLessonBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, lessonID, metadata, checklistItem, evidence)
	bodySha256 := sha256Hex([]byte(body))

	candidate := contracts.Candidate{
		CandidateID:        candidateID,
		Kind:               kind,
		TargetRuleID:       targetRuleID,
		Title:              title,
		Problem:            problem,
		Rationale:          rationale,
		ProposedBodyPath:   bodyPath,
		ProposedBodySha256: bodySha256,
	}
	if err := candidate.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	classification := contracts.ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           runID,
		CandidateID:     candidateID,
		Kind:            kind,
		SimilarityScore: similarity,
		MatchedRuleID:   targetRuleID,
		Rationale:       rationale,
		ClassifiedAt:    now,
	}
	if err := classification.Validate(); err != nil {
		return builtCandidate{}, false, err
	}

	return builtCandidate{
		Candidate: candidate,
		Body:      body,
		Lesson: lessons.Lesson{
			ID:            lessonID,
			Metadata:      metadata,
			ChecklistItem: checklistItem,
		},
		Classification: classification,
	}, true, nil
}

func activeRuleBodiesFromRegistry(entries []contracts.RuleRegistryEntry, registryBase string, activeRules map[string]bool) (map[string]string, error) {
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	bodies := make(map[string]string, len(activeRules))
	for ruleID := range activeRules {
		state, ok := states[ruleID]
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(registryBase, state.RulePath))
		if err != nil {
			return nil, fmt.Errorf("step40_classify: read rule sidecar rule_id=%s: %w", ruleID, err)
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return nil, fmt.Errorf("step40_classify: rule sidecar sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
		bodies[ruleID] = string(body)
	}
	return bodies, nil
}

func bestDuplicateMatch(candidateBody string, activeRuleBodies map[string]string) (string, float64) {
	bestRuleID := ""
	bestScore := 0.0
	normalizedCandidate := normalizeRuleContent(candidateBody)
	if strings.TrimSpace(normalizedCandidate) == "" {
		return "", 0
	}
	ruleIDs := make([]string, 0, len(activeRuleBodies))
	for ruleID := range activeRuleBodies {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		body := activeRuleBodies[ruleID]
		normalizedBody := normalizeRuleContent(body)
		if strings.TrimSpace(normalizedBody) == "" {
			continue
		}
		score := tokenSetSimilarity(normalizedCandidate, normalizedBody)
		if score > bestScore || (score == bestScore && score > 0 && (bestRuleID == "" || ruleID < bestRuleID)) {
			bestRuleID = ruleID
			bestScore = score
		}
	}
	return bestRuleID, bestScore
}

func tokenSetSimilarity(left, right string) float64 {
	leftSet := normalizedTokenSet(left)
	rightSet := normalizedTokenSet(right)
	if len(leftSet) == 0 && len(rightSet) == 0 {
		return 1
	}
	intersection := 0
	union := make(map[string]struct{}, len(leftSet)+len(rightSet))
	for token := range leftSet {
		union[token] = struct{}{}
	}
	for token := range rightSet {
		if _, ok := leftSet[token]; ok {
			intersection++
		}
		union[token] = struct{}{}
	}
	return float64(intersection) / float64(len(union))
}

func normalizeRuleContent(value string) string {
	lines := strings.Split(value, "\n")
	normalized := make([]string, 0, len(lines))
	section := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "- source_rule_id:"):
			continue
		case strings.HasPrefix(trimmed, "- classification:"):
			continue
		case trimmed == "## Problem":
			section = "problem"
			continue
		case trimmed == "## Rationale":
			section = "rationale"
			continue
		}
		if section == "problem" && strings.HasPrefix(trimmed, "Pass1 recorded ") && strings.Contains(trimmed, " violation(s) for rule ") {
			continue
		}
		if section == "rationale" && strings.HasPrefix(trimmed, "Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for ") {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return strings.Join(normalized, "\n")
}

func normalizedTokenSet(value string) map[string]struct{} {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "\n", " ")
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		set[field] = struct{}{}
	}
	return set
}

func candidateBodyMarkdown(candidate contracts.Candidate) string {
	return candidateBodyMarkdownWithEvidence(candidate, candidateEvidence{})
}

func candidateBodyMarkdownWithEvidence(candidate contracts.Candidate, evidence candidateEvidence) string {
	sourceID := sourceIDFromCandidate(candidate)
	lessonID := lessonIDFromSource(sourceID)
	metadata := lessonMetadataForEvidence(sourceID, evidence)
	checklistItem := checklistItemForEvidence(sourceID, evidence)
	return experimentLessonBodyMarkdown(candidate, lessonID, metadata, checklistItem, evidence)
}

func experimentLessonBodyMarkdown(candidate contracts.Candidate, lessonID string, metadata lessons.Metadata, checklistItem string, evidence candidateEvidence) string {
	sourceID := sourceIDFromCandidate(candidate)
	var body strings.Builder
	fmt.Fprintf(&body, "---\nstatus: %s\nseverity: %s\nconfidence: %s\ncategory: %s\n---\n\n", metadata.Status, metadata.Severity, metadata.Confidence, metadata.Category)
	fmt.Fprintf(&body, "# %s\n\n", lessonID)
	fmt.Fprintf(&body, "- candidate_id: %s\n- source_id: %s\n- classification: %s\n", candidate.CandidateID, sourceID, candidate.Kind)
	if candidate.TargetRuleID != "" {
		fmt.Fprintf(&body, "- target_rule_id: %s\n", candidate.TargetRuleID)
	}
	fmt.Fprintf(&body, "\n## Checklist Item\n\n%s\n\n", checklistItem)
	fmt.Fprintf(&body, "## Problem\n\n%s\n\n", candidate.Problem)
	fmt.Fprintf(&body, "## Evidence\n\n")
	if len(evidence.Compliance) > 0 || len(evidence.Scores) > 0 || len(evidence.Issues) > 0 {
		for _, line := range evidence.Compliance {
			fmt.Fprintf(&body, "- compliance: %s\n", line)
		}
		for _, line := range evidence.Scores {
			fmt.Fprintf(&body, "- scoring: %s\n", line)
		}
		for _, line := range evidence.Issues {
			fmt.Fprintf(&body, "- issue: %s\n", line)
		}
	} else {
		body.WriteString("- no concrete evidence captured\n")
	}
	fmt.Fprintf(&body, "\n## Guidance\n\n")
	for _, line := range guidanceLines(sourceID, evidence) {
		fmt.Fprintf(&body, "- %s\n", line)
	}
	fmt.Fprintf(&body, "\n## Exceptions\n\nIf the task explicitly requires behavior that conflicts with this lesson, document the exception and rationale.\n")
	fmt.Fprintf(&body, "\n## Merge Notes\n\nBefore adding another lesson, compare existing lessons by source, checklist item, problem, guidance, and evidence. Prefer updating an existing lesson when it covers the same failure mode.\n")
	return body.String()
}

func sourceIDFromCandidate(candidate contracts.Candidate) string {
	if candidate.TargetRuleID != "" {
		return candidate.TargetRuleID
	}
	if sourceID := strings.TrimPrefix(candidate.Title, "Experiment lesson for "); sourceID != candidate.Title {
		return sourceID
	}
	if sourceID := strings.TrimPrefix(candidate.Title, "Rule candidate for "); sourceID != candidate.Title {
		return sourceID
	}
	if base := strings.TrimSuffix(filepath.Base(candidate.ProposedBodyPath), filepath.Ext(candidate.ProposedBodyPath)); base != "." && base != "" {
		return base
	}
	return candidate.CandidateID
}

func lessonIDFromSource(sourceID string) string {
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	var out strings.Builder
	lastDash := false
	for _, r := range sourceID {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	id := strings.Trim(out.String(), "-")
	if id == "" {
		id = "lesson"
	}
	if len(id) > 72 {
		sum := sha256.Sum256([]byte(sourceID))
		prefix := strings.Trim(id[:56], "-")
		id = fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(sum[:])[:12])
	}
	if err := lessons.ValidateID(id); err != nil {
		sum := sha256.Sum256([]byte(sourceID))
		id = "lesson-" + hex.EncodeToString(sum[:])[:12]
	}
	return id
}

func lessonMetadataForEvidence(sourceID string, evidence candidateEvidence) lessons.Metadata {
	return lessons.Metadata{
		Status:     lessons.StatusActive,
		Severity:   lessonSeverityForEvidence(sourceID, evidence),
		Confidence: lessons.ConfidenceMedium,
		Category:   lessonCategoryForSource(sourceID),
	}
}

func lessonSeverityForEvidence(sourceID string, evidence candidateEvidence) lessons.Severity {
	if len(evidence.Compliance) > 0 {
		return lessons.SeverityHigh
	}
	if len(evidence.Issues) > 0 {
		return lessons.SeverityMedium
	}
	for _, line := range evidence.Scores {
		switch {
		case strings.Contains(line, "/"+string(contracts.DimensionCorrectness)+" "),
			strings.Contains(line, "/"+string(contracts.DimensionFidelity)+" "),
			strings.Contains(line, "/"+string(contracts.DimensionDiscipline)+" "):
			return lessons.SeverityHigh
		case strings.Contains(line, "/"+string(contracts.DimensionMaintainability)+" "):
			return lessons.SeverityMedium
		}
	}
	return lessons.SeverityLow
}

func lessonCategoryForSource(sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	if strings.HasPrefix(sourceID, "issue-") {
		return "judge-issue"
	}
	if strings.HasPrefix(sourceID, "score-") {
		return "score-feedback"
	}
	return "compliance"
}

func checklistItemForEvidence(sourceID string, evidence candidateEvidence) string {
	if strings.TrimSpace(evidence.Checklist) != "" {
		return shortenChecklistItem(evidence.Checklist)
	}
	if len(evidence.Compliance) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Satisfy %s based on pass1 evidence: %s", sourceID, stripEvidencePrefix(evidence.Compliance[0])))
	}
	if len(evidence.Scores) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Avoid repeating this pass1 issue: %s", stripEvidencePrefix(evidence.Scores[0])))
	}
	if len(evidence.Issues) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Avoid repeating this pass1 issue: %s", stripEvidencePrefix(evidence.Issues[0])))
	}
	return shortenChecklistItem(fmt.Sprintf("Avoid repeating the pass1 finding for %s.", sourceID))
}

func stripEvidencePrefix(value string) string {
	if idx := strings.Index(value, ": "); idx >= 0 && idx+2 < len(value) {
		return value[idx+2:]
	}
	return value
}

func shortenChecklistItem(value string) string {
	value = normalizeEvidenceText(value)
	const maxLen = 180
	if len(value) <= maxLen {
		return value
	}
	value = strings.TrimSpace(value[:maxLen-3])
	return strings.TrimRight(value, " .,;:") + "..."
}

func writeCandidateBodies(runIO internalio.RunContext, candidates []builtCandidate) error {
	for _, item := range candidates {
		path, err := runIO.ResolveRunRelative(item.Candidate.ProposedBodyPath)
		if err != nil {
			return err
		}
		if sha256Hex([]byte(item.Body)) != item.Candidate.ProposedBodySha256 {
			return fmt.Errorf("step40_classify: candidate body sha mismatch: candidate_id=%s", item.Candidate.CandidateID)
		}
		if err := internalio.WriteAtomic(path, []byte(item.Body)); err != nil {
			return err
		}
	}
	return nil
}

func writeExperimentChecklist(runIO internalio.RunContext, candidates []builtCandidate) error {
	items := make([]lessons.Lesson, 0, len(candidates))
	for _, item := range candidates {
		if item.Candidate.Kind == contracts.CandidateKindDuplicate {
			continue
		}
		items = append(items, item.Lesson)
	}
	path, err := runIO.ResolveRunRelative(experimentChecklistPath)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte(lessons.RenderChecklist(items)))
}

func collectCandidateEvidence(runIO internalio.RunContext, ruleID string, compliance []contracts.ComplianceEntry, scores []contracts.ScoreEntry) (candidateEvidence, error) {
	evidence := candidateEvidence{
		Compliance: make([]string, 0, 3),
		Scores:     make([]string, 0, 2),
	}
	seenCompliance := map[string]struct{}{}
	matchingAgents := make(map[contracts.AgentID]struct{})
	for _, entry := range compliance {
		if entry.RuleID != ruleID || !isViolationVerdict(entry.Verdict) {
			continue
		}
		matchingAgents[entry.Agent] = struct{}{}
	}
	for _, entry := range compliance {
		if entry.RuleID != ruleID || !isViolationVerdict(entry.Verdict) {
			continue
		}
		text, ok, err := substantiveEvidenceText(runIO, entry.Rationale, entry.RationaleOverflowRef)
		if err != nil {
			return candidateEvidence{}, err
		}
		if !ok {
			continue
		}
		if _, exists := seenCompliance[text]; exists {
			continue
		}
		seenCompliance[text] = struct{}{}
		evidence.Compliance = append(evidence.Compliance, text)
		if len(evidence.Compliance) == 3 {
			break
		}
	}
	scoreEvidence, err := collectScoreEvidence(runIO, scores, matchingAgents)
	if err != nil {
		return candidateEvidence{}, err
	}
	for _, line := range scoreEvidence {
		evidence.Scores = append(evidence.Scores, line)
		if len(evidence.Scores) == 2 {
			break
		}
	}
	return evidence, nil
}

func collectScoreEvidence(runIO internalio.RunContext, scores []contracts.ScoreEntry, matchingAgents map[contracts.AgentID]struct{}) ([]string, error) {
	lines := make([]string, 0, len(scores))
	seen := map[string]struct{}{}
	for _, score := range scores {
		if len(matchingAgents) > 0 {
			if _, ok := matchingAgents[score.Agent]; !ok {
				continue
			}
		}
		text, ok, err := substantiveScoreConcernText(runIO, score.Reasons, score.ReasonsOverflowRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		line := fmt.Sprintf("%s/%s: %s", score.Agent, score.Dimension, text)
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines, nil
}

func collectScoreConcernEvidence(runIO internalio.RunContext, scores []contracts.ScoreEntry, ignoredAgents map[contracts.AgentID]struct{}) (map[string]candidateEvidence, error) {
	concerns := make(map[string]candidateEvidence)
	seen := map[string]struct{}{}
	for _, score := range scores {
		if _, ignored := ignoredAgents[score.Agent]; ignored {
			continue
		}
		text, ok, err := substantiveScoreConcernText(runIO, score.Reasons, score.ReasonsOverflowRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, concern := range scoreConcernSentences(text) {
			if isNonActionableScoreConcern(concern) {
				continue
			}
			ruleID := scoreConcernRuleID(concern)
			line := fmt.Sprintf("%s/%s score %d: %s", score.Agent, score.Dimension, score.Score, concern)
			seenKey := ruleID + "\x00" + line
			if _, exists := seen[seenKey]; exists {
				continue
			}
			seen[seenKey] = struct{}{}
			evidence := concerns[ruleID]
			evidence.Scores = append(evidence.Scores, line)
			concerns[ruleID] = evidence
		}
	}
	for ruleID, evidence := range concerns {
		sort.Strings(evidence.Scores)
		if len(evidence.Scores) > 3 {
			evidence.Scores = evidence.Scores[:3]
		}
		concerns[ruleID] = evidence
	}
	return concerns, nil
}

func scoreConcernRuleID(concern string) string {
	if category := categorizedScoreConcernRuleID(concern); category != "" {
		return category
	}
	return "score-" + lessonIDFromSource(concern)
}

func categorizedScoreConcernRuleID(concern string) string {
	lower := strings.ToLower(normalizeEvidenceText(concern))
	switch {
	case strings.Contains(lower, "meta tag") && strings.Contains(lower, "client component"):
		return "score-client-component-meta-tags"
	case (strings.Contains(lower, "nearly identical") || strings.Contains(lower, "nearly-identical") || strings.Contains(lower, "duplicate")) && (strings.Contains(lower, "error.tsx") || strings.Contains(lower, "error handlers")):
		return "score-deduplicate-route-group-error-handlers"
	case strings.Contains(lower, "header/footer") || strings.Contains(lower, "inlinedheader"):
		return "score-avoid-inline-layout-duplication"
	case strings.Contains(lower, "sentry setup") && strings.Contains(lower, "unified"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "three-group error handler strategy"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "component duplication across layouts"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "proxy") && (strings.Contains(lower, "not_found") || strings.Contains(lower, "404 rewrite")):
		return "score-extract-proxy-not-found-logic"
	case strings.Contains(lower, "design system tokens"):
		return "score-use-design-system-tokens"
	case strings.Contains(lower, "cutleryicon"):
		return "score-verify-errorstate-icon-dependencies"
	default:
		return ""
	}
}

func substantiveEvidenceText(runIO internalio.RunContext, value string, overflow *contracts.OverflowRef) (string, bool, error) {
	if trimmed, ok := normalizedSubstantiveEvidenceText(value); ok {
		return trimmed, true, nil
	}
	if overflow != nil {
		sidecar, err := internalio.ReadSidecar(runIO, *overflow)
		if err != nil {
			return "", false, err
		}
		if trimmed, ok := normalizedSubstantiveEvidenceText(sidecar); ok {
			return trimmed, true, nil
		}
	}
	return "", false, nil
}

func substantiveScoreConcernText(runIO internalio.RunContext, value string, overflow *contracts.OverflowRef) (string, bool, error) {
	text, ok, err := substantiveEvidenceText(runIO, value, overflow)
	if err != nil || !ok {
		return text, ok, err
	}
	if isNonConcernScoreText(text) {
		return "", false, nil
	}
	return text, true, nil
}

func scoreConcernSentences(value string) []string {
	text := normalizeEvidenceText(value)
	if text == "" {
		return nil
	}
	parts := splitConcernSentences(text)
	concerns := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = trimConcernPrefix(part)
		if part == "" || !isConcernSentence(part) {
			continue
		}
		key := strings.ToLower(part)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		concerns = append(concerns, part)
	}
	if len(concerns) == 0 && isConcernSentence(text) {
		concerns = append(concerns, trimConcernPrefix(text))
	}
	return concerns
}

func splitConcernSentences(value string) []string {
	replacer := strings.NewReplacer(
		". Minor:", ".\nMinor:",
		". Minor issue:", ".\nMinor issue:",
		". Potential issue:", ".\nPotential issue:",
		". Main concern:", ".\nMain concern:",
		". Significant drawback:", ".\nSignificant drawback:",
		". However,", ".\nHowever,",
		". However:", ".\nHowever:",
		". Could improve", ".\nCould improve",
		". Could benefit", ".\nCould benefit",
		". Missing:", ".\nMissing:",
	)
	value = replacer.Replace(value)
	value = strings.ReplaceAll(value, ". ", ".\n")
	raw := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n'
	})
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func trimConcernPrefix(value string) string {
	value = strings.TrimSpace(value)
	prefixes := []string{
		"Minor issue:",
		"Minor:",
		"Potential issue:",
		"Main concern:",
		"Significant drawback:",
		"However,",
		"However:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
	}
	return value
}

func isConcernSentence(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	if lower == "" || isNonConcernScoreText(lower) {
		return false
	}
	if categorizedScoreConcernRuleID(lower) != "" {
		return true
	}
	markers := []string{
		"minor",
		"issue",
		"drawback",
		"could improve",
		"could benefit",
		"could strengthen",
		"could include",
		"would improve",
		"worth documenting",
		"lack",
		"missing",
		"without",
		"cannot",
		"duplicat",
		"hardcoded",
		"maintenance burden",
		"potential",
		"violates",
		"deviation",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isNonActionableScoreConcern(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	markers := []string{
		"cannot be confirmed",
		"full fidelity cannot",
		"unknown",
		"lack of visibility",
		"patch lacks commit message",
		"without explicit requirements visible",
		"no logic errors detected",
		"separation of concerns",
		"minor deduction",
		"tests verify behavior",
		"workaround but necessary",
		"likely necessary",
		"necessary due to",
		"despite valid reasoning",
		"commit message explaining",
		"reduce duplication",
		"not semantically incorrect",
		"clear code structure with helpful comments",
		"helpful comments explaining",
		"justified by",
		"is justified",
		"jsdoc",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func normalizeEvidenceText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizedSubstantiveEvidenceText(value string) (string, bool) {
	trimmed := normalizeEvidenceText(value)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "stub "):
		return "", false
	case strings.Contains(lower, "placeholder"):
		return "", false
	case strings.HasPrefix(lower, "todo"):
		return "", false
	case strings.HasPrefix(lower, "phase 0 deterministic classify"):
		return "", false
	default:
		return trimmed, true
	}
}

func isNonConcernScoreText(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	switch lower {
	case "none", "none.", "n/a", "na", "not applicable", "no issue", "no issues", "no concern", "no concerns", "no material concern", "no material concerns", "no material scoring concern", "no material scoring concerns":
		return true
	}
	prefixes := []string{
		"no material scoring concern.",
		"no material scoring concerns.",
		"no material concern.",
		"no material concerns.",
		"no issue.",
		"no issues.",
		"no concern.",
		"no concerns.",
		"looks good",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func candidateRationale(ruleID string, evidence candidateEvidence) string {
	parts := make([]string, 0, 2)
	if len(evidence.Compliance) > 0 {
		parts = append(parts, fmt.Sprintf("%d compliance violation rationale(s)", len(evidence.Compliance)))
	}
	if len(evidence.Scores) > 0 {
		parts = append(parts, fmt.Sprintf("%d score reason(s)", len(evidence.Scores)))
	}
	if len(evidence.Issues) > 0 {
		parts = append(parts, fmt.Sprintf("%d explicit issue finding(s)", len(evidence.Issues)))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Derived from %s for %s.", strings.Join(parts, " and "), ruleID)
}

func guidanceLines(ruleID string, evidence candidateEvidence) []string {
	lines := make([]string, 0, len(evidence.Compliance)+len(evidence.Scores))
	for _, line := range evidence.Guidance {
		lines = append(lines, fmt.Sprintf("Apply this proposed lesson: %s", line))
	}
	for _, line := range evidence.Compliance {
		lines = append(lines, fmt.Sprintf("When handling %s, prevent the behavior implied by this violation evidence: %s", ruleID, line))
	}
	for _, line := range evidence.Scores {
		lines = append(lines, fmt.Sprintf("Address this pass1 scoring concern while implementing the task: %s", line))
	}
	for _, line := range evidence.Issues {
		lines = append(lines, fmt.Sprintf("Address this explicit pass1 issue while implementing the task: %s", line))
	}
	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf("Use this lesson as the concrete failure mode to avoid for %s.", ruleID))
	}
	return lines
}

func writeClassificationJSONL(runIO internalio.RunContext, classifications []contracts.ClassificationEntry) error {
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		return err
	}

	var buffer bytes.Buffer
	for _, entry := range classifications {
		if _, err := contracts.MarshalStrict(entry); err != nil {
			return err
		}
		payload, err := contracts.CanonicalMarshal(entry)
		if err != nil {
			return err
		}
		if len(payload)+1 > internalio.JSONLMaxLineBytes {
			return internalio.ErrEntryTooLarge
		}
		if _, err := buffer.Write(payload); err != nil {
			return err
		}
		if err := buffer.WriteByte('\n'); err != nil {
			return err
		}
	}
	return internalio.WriteAtomic(path, buffer.Bytes())
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

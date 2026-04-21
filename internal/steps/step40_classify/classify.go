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
	"github.com/nishimoto265/auto-improve/internal/registryview"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

const (
	candidatesJSONPath      = "40/candidates.json"
	classificationJSONLPath = "40/classification.jsonl"
	scoresPath              = "30/scores-A.jsonl"
	compliancePath          = "30/compliance-A.jsonl"
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
	registry, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](cfg.registryPath())
	if err != nil {
		return nil, err
	}

	createdAt := cfg.now()
	built, err := buildCandidates(cfg.IO.RunID, createdAt, scores, compliance, registry, filepath.Dir(cfg.registryPath()))
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

func step30Ready(runIO internalio.RunContext, pkg *contracts.TaskPackage) (bool, error) {
	expectedAgents, known, err := currentPass1ScorableAgents(runIO, pkg)
	if err != nil {
		return false, err
	}
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
	manifestCount := 0
	for _, wt := range pkg.Worktrees {
		if wt.Pass != 1 {
			continue
		}
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
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents, manifestCount > 0, nil
}

func buildCandidates(runID contracts.RunID, now time.Time, scores []contracts.ScoreEntry, compliance []contracts.ComplianceEntry, registry []contracts.RuleRegistryEntry, registryBase string) ([]builtCandidate, error) {
	if len(compliance) == 0 {
		return []builtCandidate{}, nil
	}
	if len(scores) == 0 {
		return nil, errors.New("step40_classify: missing or incomplete step30 inputs")
	}

	violations := collectViolations(compliance)
	if len(violations) == 0 {
		return []builtCandidate{}, nil
	}

	activeRules, err := activeRulesFromRegistry(registry)
	if err != nil {
		return nil, err
	}
	activeRuleBodies, err := activeRuleBodiesFromRegistry(registry, registryBase, activeRules)
	if err != nil {
		return nil, err
	}
	scoreEvidence := collectScoreEvidence(scores)
	ruleIDs := make([]string, 0, len(violations))
	for ruleID := range violations {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	candidates := make([]builtCandidate, 0, len(ruleIDs))
	for idx, ruleID := range ruleIDs {
		evidence := collectCandidateEvidence(ruleID, compliance, scoreEvidence)
		candidate, ok, err := buildCandidate(runID, now, idx+1, ruleID, violations[ruleID], activeRules[ruleID], activeRuleBodies, evidence)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
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

func isViolationVerdict(verdict contracts.ComplianceVerdict) bool {
	switch verdict {
	case contracts.ComplianceVerdictViolated, contracts.ComplianceVerdictInvalidException, contracts.ComplianceVerdictMissed:
		return true
	default:
		return false
	}
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
}

func buildCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, violationCount int, existsInRegistry bool, activeRuleBodies map[string]string, evidence candidateEvidence) (builtCandidate, bool, error) {
	if len(evidence.Compliance) == 0 && len(evidence.Scores) == 0 {
		return builtCandidate{}, false, nil
	}
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	title := fmt.Sprintf("Rule candidate for %s", ruleID)
	problem := fmt.Sprintf("Pass1 recorded %d violation(s) for rule %s.", violationCount, ruleID)
	rationale := candidateRationale(ruleID, evidence)

	bodyPath := filepath.Join("40", "candidates", candidateID+".md")
	draftBody := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, evidence)

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
	body := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	}, evidence)
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
		Candidate:      candidate,
		Body:           body,
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
	for ruleID, body := range activeRuleBodies {
		normalizedBody := normalizeRuleContent(body)
		if strings.TrimSpace(normalizedBody) == "" {
			continue
		}
		score := tokenSetSimilarity(normalizedCandidate, normalizedBody)
		if score > bestScore {
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
	ruleID := candidate.TargetRuleID
	if ruleID == "" {
		ruleID = strings.TrimPrefix(candidate.Title, "Rule candidate for ")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n- source_rule_id: %s\n- classification: %s\n\n## Problem\n%s\n\n## Rationale\n%s\n", candidate.Title, ruleID, candidate.Kind, candidate.Problem, candidate.Rationale)
	if len(evidence.Compliance) > 0 || len(evidence.Scores) > 0 {
		body.WriteString("\n## Evidence\n")
		for _, line := range evidence.Compliance {
			fmt.Fprintf(&body, "- compliance: %s\n", line)
		}
		for _, line := range evidence.Scores {
			fmt.Fprintf(&body, "- scoring: %s\n", line)
		}
		body.WriteString("\n## Proposed rule\n")
		for _, line := range proposedRuleLines(ruleID, evidence) {
			fmt.Fprintf(&body, "- %s\n", line)
		}
	}
	return body.String()
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

func collectCandidateEvidence(ruleID string, compliance []contracts.ComplianceEntry, scoreEvidence []string) candidateEvidence {
	evidence := candidateEvidence{
		Compliance: make([]string, 0, 3),
		Scores:     make([]string, 0, 2),
	}
	seenCompliance := map[string]struct{}{}
	for _, entry := range compliance {
		if entry.RuleID != ruleID || !isViolationVerdict(entry.Verdict) {
			continue
		}
		text, ok := substantiveEvidenceText(entry.Rationale)
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
	for _, line := range scoreEvidence {
		evidence.Scores = append(evidence.Scores, line)
		if len(evidence.Scores) == 2 {
			break
		}
	}
	return evidence
}

func collectScoreEvidence(scores []contracts.ScoreEntry) []string {
	lines := make([]string, 0, len(scores))
	seen := map[string]struct{}{}
	for _, score := range scores {
		text, ok := substantiveEvidenceText(score.Reasons)
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
	return lines
}

func substantiveEvidenceText(value string) (string, bool) {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
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

func candidateRationale(ruleID string, evidence candidateEvidence) string {
	parts := make([]string, 0, 2)
	if len(evidence.Compliance) > 0 {
		parts = append(parts, fmt.Sprintf("%d compliance violation rationale(s)", len(evidence.Compliance)))
	}
	if len(evidence.Scores) > 0 {
		parts = append(parts, fmt.Sprintf("%d score reason(s)", len(evidence.Scores)))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Derived from %s for %s.", strings.Join(parts, " and "), ruleID)
}

func proposedRuleLines(ruleID string, evidence candidateEvidence) []string {
	lines := make([]string, 0, len(evidence.Compliance)+len(evidence.Scores))
	for _, line := range evidence.Compliance {
		lines = append(lines, fmt.Sprintf("When handling %s, enforce the behavior implied by this violation evidence: %s", ruleID, line))
	}
	for _, line := range evidence.Scores {
		lines = append(lines, fmt.Sprintf("Keep the implementation aligned with this scoring concern while addressing %s: %s", ruleID, line))
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

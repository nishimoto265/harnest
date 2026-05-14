package judges

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/nishimoto265/harnest/internal/agents"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
)

//go:embed prompts/step30-score.tmpl prompts/step60-score-pass2.tmpl prompts/step60-pairwise.tmpl prompts/step60-pairwise-decision.tmpl
var cliJudgePromptFS embed.FS

const defaultCLIJudgeTimeout = 15 * time.Minute
const cliJudgePromptVersion = "cli-judge-v1"

type cliJudge struct {
	role    Role
	profile agents.Profile
	timeout time.Duration
	now     func() time.Time
}

type modelJudgeScore struct {
	Dimension string `json:"dimension"`
	Score     int    `json:"score"`
	Reason    string `json:"reason"`
}

type modelJudgeCompliance struct {
	RuleID    string `json:"rule_id"`
	Verdict   string `json:"verdict"`
	Rationale string `json:"rationale,omitempty"`
}

type modelJudgeIssue struct {
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Title          string `json:"title"`
	Evidence       string `json:"evidence"`
	ProposedLesson string `json:"proposed_lesson"`
	ChecklistItem  string `json:"checklist_item"`
}

type modelJudgeResponse struct {
	Scores     []modelJudgeScore      `json:"scores"`
	Compliance []modelJudgeCompliance `json:"compliance"`
	Issues     []modelJudgeIssue      `json:"issues,omitempty"`
}

type cliJudgePromptData struct {
	Role                      Role
	Input                     JudgeInput
	Dimensions                []string
	ComplianceRuleIDs         []string
	EnforceExpectedCompliance bool
	CandidateRules            []CandidateRule
}

func NewCLIJudge(profile agents.Profile, role Role) Judge {
	return cliJudge{
		role:    role,
		profile: profile,
		timeout: defaultCLIJudgeTimeout,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (j cliJudge) ScoreOutput(ctx context.Context, input JudgeInput) (JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return JudgeOutput{}, err
	}
	workspace, err := prepareCLIJudgeWorkspace(input, j.profile.Provider)
	if err != nil {
		return JudgeOutput{}, err
	}
	defer workspace.cleanup()

	promptText, err := renderCLIJudgePrompt(j.role, workspace.input)
	if err != nil {
		return JudgeOutput{}, err
	}
	responsePath, err := runCLIJudge(ctx, j.profile, workspace.workdir, promptText, j.timeout)
	if err != nil {
		return JudgeOutput{}, err
	}
	defer os.Remove(responsePath)
	response, err := readModelJudgeResponse(responsePath)
	if err != nil {
		return JudgeOutput{}, err
	}
	return j.toJudgeOutput(input, response)
}

type cliJudgeWorkspace struct {
	input   JudgeInput
	workdir string
	cleanup func()
}

func prepareCLIJudgeWorkspace(input JudgeInput, provider agents.Provider) (cliJudgeWorkspace, error) {
	workspace, err := agentrunner.PrepareReadOnlyWorkspace(provider, filepath.Dir(input.OutputPath), "auto-improve-judge-workdir-*", []agentrunner.WorkspaceFile{
		{Key: "output", SourcePath: input.OutputPath, TargetName: "output.patch"},
		{Key: "rubric", SourcePath: input.RubricPath, TargetName: "rubric.md"},
	})
	if err != nil {
		return cliJudgeWorkspace{}, err
	}
	bundled := input
	bundled.OutputPath = workspace.Files["output"]
	bundled.RubricPath = workspace.Files["rubric"]
	return cliJudgeWorkspace{
		input:   bundled,
		workdir: workspace.Workdir,
		cleanup: workspace.Cleanup,
	}, nil
}

func (j cliJudge) JudgePromptVersion() string {
	payload := struct {
		PromptVersion string          `json:"prompt_version"`
		Role          Role            `json:"role"`
		Provider      agents.Provider `json:"provider"`
		Binary        string          `json:"binary"`
		NodeBinary    string          `json:"node_binary,omitempty"`
		Args          []string        `json:"args"`
		Step30Hash    string          `json:"step30_hash"`
		Step60Hash    string          `json:"step60_hash"`
	}{
		PromptVersion: cliJudgePromptVersion,
		Role:          j.role,
		Provider:      j.profile.Provider,
		Binary:        j.profile.Binary,
		NodeBinary:    j.profile.NodeBinary,
		Args:          append([]string(nil), j.profile.Args...),
		Step30Hash:    embeddedPromptHash("prompts/step30-score.tmpl"),
		Step60Hash:    embeddedPromptHash("prompts/step60-score-pass2.tmpl"),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return cliJudgePromptVersion
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s-%s", cliJudgePromptVersion, j.profile.Provider, hex.EncodeToString(sum[:])[:12])
}

func embeddedPromptHash(name string) string {
	data, err := cliJudgePromptFS.ReadFile(name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func renderCLIJudgePrompt(role Role, input JudgeInput) (string, error) {
	templateName := "prompts/step30-score.tmpl"
	if input.Pass == 2 {
		templateName = "prompts/step60-score-pass2.tmpl"
	}
	tmpl, err := template.New(filepath.Base(templateName)).Option("missingkey=error").ParseFS(cliJudgePromptFS, templateName)
	if err != nil {
		return "", err
	}
	dimensions := make([]string, 0, len(allDimensions))
	for _, dimension := range allDimensions {
		dimensions = append(dimensions, string(dimension))
	}
	complianceRuleIDs := input.ExpectedComplianceRuleIDs
	if len(complianceRuleIDs) == 0 && !input.EnforceExpectedCompliance {
		var err error
		complianceRuleIDs, err = ExpectedComplianceRuleIDs(input.RubricPath)
		if err != nil {
			return "", err
		}
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, cliJudgePromptData{
		Role:                      role,
		Input:                     input,
		Dimensions:                dimensions,
		ComplianceRuleIDs:         complianceRuleIDs,
		EnforceExpectedCompliance: input.EnforceExpectedCompliance,
		CandidateRules:            sanitizeCandidateRules(input.CandidateRules),
	}); err != nil {
		return "", err
	}
	return out.String(), nil
}

func sanitizeCandidateRules(rules []CandidateRule) []CandidateRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]CandidateRule, len(rules))
	for i, rule := range rules {
		out[i] = CandidateRule{
			ID:           internalio.SanitizeForPromptEmbedding(rule.ID),
			Kind:         internalio.SanitizeForPromptEmbedding(rule.Kind),
			TargetRuleID: internalio.SanitizeForPromptEmbedding(rule.TargetRuleID),
			Title:        internalio.SanitizeForPromptEmbedding(rule.Title),
			Body: internalio.SanitizeForPromptEmbedding(rule.Body, internalio.SafeTextOptions{
				Label: "experiment_lesson",
				Fence: true,
			}),
		}
	}
	return out
}

func runCLIJudge(ctx context.Context, profile agents.Profile, workdir, promptText string, timeout time.Duration) (string, error) {
	command, err := agentrunner.PrepareReadOnlyCommand(profile, workdir)
	if err != nil {
		return "", err
	}
	defer command.Cleanup()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := agentrunner.RunCommand(timeoutCtx, agentrunner.CommandRequest{
		Binary:      command.Binary,
		Args:        command.Args,
		Workdir:     command.Workdir,
		Prompt:      promptText,
		SessionPath: command.SessionPath,
		Timeout:     timeout,
		Provider:    command.Provider,
		Env:         command.Env,
		ErrPrefix:   "judge",
	})
	if err != nil {
		return "", err
	}
	if err := validateCLIJudgeCommandResult(result); err != nil {
		return "", err
	}
	return command.ResponsePath, nil
}

func validateCLIJudgeCommandResult(result agentrunner.CommandResult) error {
	switch {
	case result.TimedOut:
		return fmt.Errorf("judges: CLI judge timed out: stdout=%q stderr=%q", snippetForError(result.StdoutSnippet), snippetForError(result.StderrSnippet))
	case result.ExitCode != 0:
		return fmt.Errorf("judges: CLI judge exited with code %d: stdout=%q stderr=%q", result.ExitCode, snippetForError(result.StdoutSnippet), snippetForError(result.StderrSnippet))
	case result.CleanupErr != nil:
		return fmt.Errorf("judges: CLI judge cleanup failed: %w", result.CleanupErr)
	default:
		return nil
	}
}

func snippetForError(value []byte) string {
	return strings.TrimSpace(string(value))
}

func readModelJudgeResponse(path string) (modelJudgeResponse, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return modelJudgeResponse{}, err
	}
	var response modelJudgeResponse
	payload := extractJSONObject(bytes.TrimSpace(data))
	if err := json.Unmarshal(payload, &response); err == nil && len(response.Scores) > 0 {
		return response, nil
	}
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &wrapper); err == nil && strings.TrimSpace(wrapper.Result) != "" {
		payload = extractJSONObject([]byte(wrapper.Result))
		if err := json.Unmarshal(payload, &response); err != nil {
			return modelJudgeResponse{}, err
		}
		return response, nil
	}
	return modelJudgeResponse{}, fmt.Errorf("judges: parse CLI judge output")
}

func extractJSONObject(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start >= 0 && end >= start {
		return []byte(text[start : end+1])
	}
	return []byte(text)
}

func (j cliJudge) toJudgeOutput(input JudgeInput, response modelJudgeResponse) (JudgeOutput, error) {
	verdictPath := contracts.VerdictPathSingle
	if j.role == RoleArbiter {
		verdictPath = contracts.VerdictPathArbitrated
	}
	resolvedAt := j.now().UTC()
	scores := make([]contracts.ScoreEntry, 0, len(response.Scores))
	for _, score := range response.Scores {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     contracts.Dimension(score.Dimension),
			Score:         score.Score,
			Reasons:       truncateRunes(strings.TrimSpace(score.Reason), 1000),
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: cliJudgePromptVersion,
			ResolvedAt:    resolvedAt,
		})
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(response.Compliance))
	for _, row := range response.Compliance {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        row.RuleID,
			Verdict:       contracts.ComplianceVerdict(row.Verdict),
			Rationale:     truncateRunes(strings.TrimSpace(row.Rationale), 500),
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: cliJudgePromptVersion,
			ResolvedAt:    resolvedAt,
		})
	}
	compliance = normalizeExpectedComplianceEntries(input, compliance, verdictPath, resolvedAt)
	issues := make([]Issue, 0, len(response.Issues))
	for _, row := range response.Issues {
		issue, ok := normalizeModelJudgeIssue(row)
		if !ok {
			continue
		}
		issues = append(issues, issue)
	}
	output := JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
		Arbiter:    j.role == RoleArbiter,
		Issues:     issues,
	}
	return output, output.ValidateFor(input)
}

func normalizeExpectedComplianceEntries(input JudgeInput, entries []contracts.ComplianceEntry, verdictPath contracts.VerdictPath, resolvedAt time.Time) []contracts.ComplianceEntry {
	if len(input.ExpectedComplianceRuleIDs) == 0 {
		return entries
	}
	expected := make(map[string]struct{}, len(input.ExpectedComplianceRuleIDs))
	for _, ruleID := range input.ExpectedComplianceRuleIDs {
		expected[ruleID] = struct{}{}
	}
	byRule := make(map[string][]contracts.ComplianceEntry, len(input.ExpectedComplianceRuleIDs))
	for _, entry := range entries {
		if _, ok := expected[entry.RuleID]; ok {
			byRule[entry.RuleID] = append(byRule[entry.RuleID], entry)
		}
	}
	out := make([]contracts.ComplianceEntry, 0, len(entries))
	for _, ruleID := range input.ExpectedComplianceRuleIDs {
		if rows := byRule[ruleID]; len(rows) > 0 {
			out = append(out, rows...)
			continue
		}
		out = append(out, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictMissed,
			Rationale:     "Judge omitted the required compliance row for this rule; treating it as missed coverage.",
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: cliJudgePromptVersion,
			ResolvedAt:    resolvedAt,
		})
	}
	return out
}

func normalizeModelJudgeIssue(row modelJudgeIssue) (Issue, bool) {
	severity, ok := normalizeIssueSeverity(row.Severity)
	if !ok {
		return Issue{}, false
	}
	title := truncateRunes(strings.TrimSpace(row.Title), 160)
	evidence := truncateRunes(strings.TrimSpace(row.Evidence), 700)
	if title == "" || evidence == "" {
		return Issue{}, false
	}
	category := truncateRunes(strings.TrimSpace(row.Category), 80)
	if category == "" {
		category = "general"
	}
	proposedLesson := truncateRunes(strings.TrimSpace(row.ProposedLesson), 700)
	if proposedLesson == "" {
		proposedLesson = evidence
	}
	checklistItem := truncateRunes(strings.TrimSpace(row.ChecklistItem), 220)
	if checklistItem == "" {
		checklistItem = title
	}
	return Issue{
		Severity:       severity,
		Category:       category,
		Title:          title,
		Evidence:       evidence,
		ProposedLesson: proposedLesson,
		ChecklistItem:  checklistItem,
	}, true
}

func normalizeIssueSeverity(value string) (contracts.IssueSeverity, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "crit":
		return contracts.IssueSeverityCritical, true
	case "high":
		return contracts.IssueSeverityHigh, true
	case "medium", "middle", "mid":
		return contracts.IssueSeverityMedium, true
	case "low":
		return contracts.IssueSeverityLow, true
	default:
		return "", false
	}
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

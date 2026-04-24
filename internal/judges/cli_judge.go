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

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

//go:embed prompts/step30-score.tmpl prompts/step60-score-pass2.tmpl
var cliJudgePromptFS embed.FS

const defaultCLIJudgeTimeout = 2 * time.Minute
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

type modelJudgeResponse struct {
	Scores     []modelJudgeScore      `json:"scores"`
	Compliance []modelJudgeCompliance `json:"compliance"`
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
	promptText, err := renderCLIJudgePrompt(j.role, input)
	if err != nil {
		return JudgeOutput{}, err
	}
	workdir := filepath.Dir(input.OutputPath)
	binary, prefixArgs, err := agentrunner.PrepareProviderBinary(j.profile.Provider, j.profile.Binary)
	if err != nil {
		return JudgeOutput{}, err
	}
	responsePath, err := runCLIJudge(ctx, binary, prefixArgs, j.profile, workdir, promptText, j.timeout)
	if err != nil {
		return JudgeOutput{}, err
	}
	response, err := readModelJudgeResponse(responsePath)
	if err != nil {
		return JudgeOutput{}, err
	}
	return j.toJudgeOutput(input, response)
}

func (j cliJudge) JudgePromptVersion() string {
	payload := struct {
		PromptVersion string          `json:"prompt_version"`
		Role          Role            `json:"role"`
		Provider      agents.Provider `json:"provider"`
		Binary        string          `json:"binary"`
		Args          []string        `json:"args"`
		Step30Hash    string          `json:"step30_hash"`
		Step60Hash    string          `json:"step60_hash"`
	}{
		PromptVersion: cliJudgePromptVersion,
		Role:          j.role,
		Provider:      j.profile.Provider,
		Binary:        j.profile.Binary,
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
				Label: "candidate_rule",
				Fence: true,
			}),
		}
	}
	return out
}

func runCLIJudge(ctx context.Context, binary string, prefixArgs []string, profile agents.Profile, workdir, promptText string, timeout time.Duration) (string, error) {
	sessionPath, err := tempJudgeFile("session")
	if err != nil {
		return "", err
	}
	outputPath, err := tempJudgeFile("output")
	if err != nil {
		return "", err
	}
	defer func() {
		if profile.Provider != agents.ProviderClaude {
			_ = os.Remove(sessionPath)
		}
		if profile.Provider != agents.ProviderCodex {
			_ = os.Remove(outputPath)
		}
	}()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{}, prefixArgs...)
	switch profile.Provider {
	case agents.ProviderCodex:
		codexArgs, err := codexJudgeExecArgs(profile.Args, workdir, outputPath)
		if err != nil {
			return "", err
		}
		args = append(args, codexArgs...)
	case agents.ProviderClaude:
		claudeArgs, err := claudeJudgeExecArgs(profile.Args, workdir)
		if err != nil {
			return "", err
		}
		args = append(args, claudeArgs...)
	default:
		return "", fmt.Errorf("judges: CLI provider %q is not implemented yet", profile.Provider)
	}
	result, err := agentrunner.RunCommand(timeoutCtx, agentrunner.CommandRequest{
		Binary:      binary,
		Args:        args,
		Workdir:     workdir,
		Prompt:      promptText,
		SessionPath: sessionPath,
		Timeout:     timeout,
		ErrPrefix:   "judge",
	})
	if err != nil {
		return "", err
	}
	if err := validateCLIJudgeCommandResult(result); err != nil {
		return "", err
	}
	switch profile.Provider {
	case agents.ProviderCodex:
		return outputPath, nil
	case agents.ProviderClaude:
		return sessionPath, nil
	default:
		return "", fmt.Errorf("judges: CLI provider %q is not implemented yet", profile.Provider)
	}
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

func codexJudgeExecArgs(profileArgs []string, workdir, outputPath string) ([]string, error) {
	if err := agents.ValidateJudgeProfileArgs(agents.ProviderCodex, profileArgs); err != nil {
		return nil, err
	}
	args := []string{
		"exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"-C", workdir,
	}
	args = append(args, profileArgs...)
	args = append(args, "-o", outputPath, "-")
	return args, nil
}

func claudeJudgeExecArgs(profileArgs []string, workdir string) ([]string, error) {
	if err := agents.ValidateJudgeProfileArgs(agents.ProviderClaude, profileArgs); err != nil {
		return nil, err
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--allowedTools", "Read",
		"--cwd", workdir,
	}
	args = append(args, profileArgs...)
	return args, nil
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

func tempJudgeFile(prefix string) (string, error) {
	file, err := os.CreateTemp("", "auto-improve-"+prefix+"-*.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
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
			Reasons:       score.Reason,
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
			Rationale:     row.Rationale,
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: cliJudgePromptVersion,
			ResolvedAt:    resolvedAt,
		})
	}
	output := JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
		Arbiter:    j.role == RoleArbiter,
	}
	return output, output.ValidateFor(input)
}

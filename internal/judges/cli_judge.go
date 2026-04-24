package judges

import (
	"bytes"
	"context"
	"embed"
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
	Role              Role
	Input             JudgeInput
	Dimensions        []string
	ComplianceRuleIDs []string
	CandidateRules    []CandidateRule
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
	if len(complianceRuleIDs) == 0 {
		var err error
		complianceRuleIDs, err = ExpectedComplianceRuleIDs(input.RubricPath)
		if err != nil {
			return "", err
		}
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, cliJudgePromptData{
		Role:              role,
		Input:             input,
		Dimensions:        dimensions,
		ComplianceRuleIDs: complianceRuleIDs,
		CandidateRules:    sanitizeCandidateRules(input.CandidateRules),
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
		args = append(args, agentrunner.CodexExecArgs(workdir)...)
		args = append(args, profile.Args...)
		args = append(args, "-o", outputPath, "-")
	case agents.ProviderClaude:
		args = append(args, "-p", "--output-format", "json", "--allowedTools", "Read", "--cwd", workdir)
		args = append(args, profile.Args...)
	default:
		return "", fmt.Errorf("judges: CLI provider %q is not implemented yet", profile.Provider)
	}
	_, err = agentrunner.RunCommand(timeoutCtx, agentrunner.CommandRequest{
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
	switch profile.Provider {
	case agents.ProviderCodex:
		return outputPath, nil
	case agents.ProviderClaude:
		return sessionPath, nil
	default:
		return "", fmt.Errorf("judges: CLI provider %q is not implemented yet", profile.Provider)
	}
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
			PromptVersion: "cli-judge-v1",
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
			PromptVersion: "cli-judge-v1",
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

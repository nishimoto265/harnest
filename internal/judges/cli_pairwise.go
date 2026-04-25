package judges

import (
	"bytes"
	"context"
	"crypto/sha256"
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

type cliPairwiseJudge struct {
	profile agents.Profile
	purpose string
	timeout time.Duration
	now     func() time.Time
}

type modelPairwiseDimensionVote struct {
	Dimension string `json:"dimension"`
	Winner    string `json:"winner"`
	Reason    string `json:"reason,omitempty"`
}

type modelPairwiseResponse struct {
	Winner         string                       `json:"winner"`
	Margin         string                       `json:"margin"`
	Justification  string                       `json:"justification,omitempty"`
	DimensionVotes []modelPairwiseDimensionVote `json:"dimension_votes,omitempty"`
	FatalIssues    []string                     `json:"fatal_issues,omitempty"`
}

type modelPairwiseAgentDecision struct {
	Agent         string `json:"agent"`
	Winner        string `json:"winner"`
	Margin        string `json:"margin"`
	Justification string `json:"justification,omitempty"`
}

type modelPairwiseDecisionResponse struct {
	Decision       string                       `json:"decision"`
	Justification  string                       `json:"justification,omitempty"`
	AgentDecisions []modelPairwiseAgentDecision `json:"agent_decisions,omitempty"`
}

type cliPairwisePromptData struct {
	Input      PairwiseInput
	TaskPrompt string
}

type cliPairwiseDecisionPromptData struct {
	Input      PairwiseDecisionInput
	TaskPrompt string
}

func (j cliPairwiseJudge) ComparePairwise(ctx context.Context, input PairwiseInput) (PairwiseComparison, error) {
	if err := validatePairwiseInput(input); err != nil {
		return PairwiseComparison{}, err
	}
	promptText, err := renderCLIPairwisePrompt(input)
	if err != nil {
		return PairwiseComparison{}, err
	}
	responsePath, err := j.run(ctx, input.A.OutputPath, promptText)
	if err != nil {
		return PairwiseComparison{}, err
	}
	response, err := readModelPairwiseResponse(responsePath)
	if err != nil {
		return PairwiseComparison{}, err
	}
	return modelPairwiseToComparison(input, response)
}

func (j cliPairwiseJudge) DecidePairwise(ctx context.Context, input PairwiseDecisionInput) (PairwiseDecision, error) {
	if err := validatePairwiseDecisionInput(input); err != nil {
		return PairwiseDecision{}, err
	}
	promptText, err := renderCLIPairwiseDecisionPrompt(input)
	if err != nil {
		return PairwiseDecision{}, err
	}
	anchorPath := input.RubricPath
	if len(input.Pairs) > 0 {
		anchorPath = input.Pairs[0].A.OutputPath
	}
	responsePath, err := j.run(ctx, anchorPath, promptText)
	if err != nil {
		return PairwiseDecision{}, err
	}
	response, err := readModelPairwiseDecisionResponse(responsePath)
	if err != nil {
		return PairwiseDecision{}, err
	}
	return modelPairwiseToDecision(input, response)
}

func (j cliPairwiseJudge) PairwiseJudgePromptVersion() string {
	payload := struct {
		PromptVersion string          `json:"prompt_version"`
		Purpose       string          `json:"purpose"`
		Provider      agents.Provider `json:"provider"`
		Binary        string          `json:"binary"`
		Args          []string        `json:"args"`
		PairwiseHash  string          `json:"pairwise_hash"`
		DecisionHash  string          `json:"decision_hash"`
	}{
		PromptVersion: pairwisePromptVersion,
		Purpose:       j.purpose,
		Provider:      j.profile.Provider,
		Binary:        j.profile.Binary,
		Args:          append([]string(nil), j.profile.Args...),
		PairwiseHash:  embeddedPromptHash("prompts/step60-pairwise.tmpl"),
		DecisionHash:  embeddedPromptHash("prompts/step60-pairwise-decision.tmpl"),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return pairwisePromptVersion
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s-%s", pairwisePromptVersion, j.profile.Provider, hex.EncodeToString(sum[:])[:12])
}

func (j cliPairwiseJudge) run(ctx context.Context, anchorPath, promptText string) (string, error) {
	workdir := filepath.Dir(anchorPath)
	binary, prefixArgs, err := agentrunner.PrepareProviderBinary(j.profile.Provider, j.profile.Binary)
	if err != nil {
		return "", err
	}
	return runCLIJudge(ctx, binary, prefixArgs, j.profile, workdir, promptText, j.timeout)
}

func renderCLIPairwisePrompt(input PairwiseInput) (string, error) {
	tmpl, err := template.New("step60-pairwise.tmpl").Option("missingkey=error").ParseFS(cliJudgePromptFS, "prompts/step60-pairwise.tmpl")
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, cliPairwisePromptData{
		Input:      input,
		TaskPrompt: sanitizeTaskPrompt(input.TaskPrompt),
	}); err != nil {
		return "", err
	}
	return out.String(), nil
}

func renderCLIPairwiseDecisionPrompt(input PairwiseDecisionInput) (string, error) {
	tmpl, err := template.New("step60-pairwise-decision.tmpl").Option("missingkey=error").ParseFS(cliJudgePromptFS, "prompts/step60-pairwise-decision.tmpl")
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, cliPairwiseDecisionPromptData{
		Input:      input,
		TaskPrompt: sanitizeTaskPrompt(input.TaskPrompt),
	}); err != nil {
		return "", err
	}
	return out.String(), nil
}

func sanitizeTaskPrompt(prompt string) string {
	return internalio.SanitizeForPromptEmbedding(prompt, internalio.SafeTextOptions{
		Label: "task_prompt",
		Fence: true,
	})
}

func readModelPairwiseResponse(path string) (modelPairwiseResponse, error) {
	data, err := readModelJSONPayload(path)
	if err != nil {
		return modelPairwiseResponse{}, err
	}
	var response modelPairwiseResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return modelPairwiseResponse{}, err
	}
	if response.Winner == "" {
		return modelPairwiseResponse{}, fmt.Errorf("judges: pairwise output missing winner")
	}
	return response, nil
}

func readModelPairwiseDecisionResponse(path string) (modelPairwiseDecisionResponse, error) {
	data, err := readModelJSONPayload(path)
	if err != nil {
		return modelPairwiseDecisionResponse{}, err
	}
	var response modelPairwiseDecisionResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return modelPairwiseDecisionResponse{}, err
	}
	if response.Decision == "" {
		return modelPairwiseDecisionResponse{}, fmt.Errorf("judges: pairwise decision output missing decision")
	}
	return response, nil
}

func readModelJSONPayload(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	payload := extractJSONObject(bytes.TrimSpace(data))
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &wrapper); err == nil && strings.TrimSpace(wrapper.Result) != "" {
		payload = extractJSONObject([]byte(wrapper.Result))
	}
	return payload, nil
}

func modelPairwiseToComparison(input PairwiseInput, response modelPairwiseResponse) (PairwiseComparison, error) {
	comparison := PairwiseComparison{
		Agent:         input.Agent,
		Order:         input.Order,
		Winner:        contracts.PairwiseWinner(response.Winner),
		Margin:        contracts.PairwiseMargin(response.Margin),
		Justification: response.Justification,
		FatalIssues:   append([]string(nil), response.FatalIssues...),
	}
	if comparison.Margin == "" {
		comparison.Margin = contracts.PairwiseMarginSlight
	}
	for _, vote := range response.DimensionVotes {
		comparison.DimensionVotes = append(comparison.DimensionVotes, PairwiseDimensionVote{
			Dimension: contracts.Dimension(vote.Dimension),
			Winner:    contracts.PairwiseWinner(vote.Winner),
			Reason:    vote.Reason,
		})
	}
	if err := validateComparison(comparison); err != nil {
		return PairwiseComparison{}, err
	}
	return comparison, nil
}

func modelPairwiseToDecision(input PairwiseDecisionInput, response modelPairwiseDecisionResponse) (PairwiseDecision, error) {
	decision := PairwiseDecision{
		Action:        PairwiseDecisionAction(response.Decision),
		Justification: response.Justification,
	}
	for _, row := range response.AgentDecisions {
		margin := contracts.PairwiseMargin(row.Margin)
		if margin == "" {
			margin = contracts.PairwiseMarginSlight
		}
		decision.AgentDecisions = append(decision.AgentDecisions, PairwiseAgentDecision{
			Agent:         contracts.AgentID(row.Agent),
			Winner:        contracts.PairwiseWinner(row.Winner),
			Margin:        margin,
			Justification: row.Justification,
		})
	}
	if len(decision.AgentDecisions) == 0 {
		derived, err := NewScoreDerivedPairwiseDecisionJudge().DecidePairwise(context.Background(), input)
		if err != nil {
			return PairwiseDecision{}, err
		}
		decision.AgentDecisions = derived.AgentDecisions
	}
	if err := validateDecision(decision); err != nil {
		return PairwiseDecision{}, err
	}
	return decision, nil
}

func validatePairwiseInput(input PairwiseInput) error {
	if err := contracts.EnsureCleanAbsolutePath(input.RubricPath); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(input.A.OutputPath); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(input.B.OutputPath); err != nil {
		return err
	}
	if input.Agent == "" {
		return fmt.Errorf("judges: pairwise agent is required")
	}
	return nil
}

func validatePairwiseDecisionInput(input PairwiseDecisionInput) error {
	if err := contracts.EnsureCleanAbsolutePath(input.RubricPath); err != nil {
		return err
	}
	if len(input.Pairs) == 0 {
		return fmt.Errorf("judges: pairwise decision requires at least one pair")
	}
	return nil
}

func validateComparison(comparison PairwiseComparison) error {
	if comparison.Winner != contracts.PairwiseWinnerA && comparison.Winner != contracts.PairwiseWinnerB && comparison.Winner != contracts.PairwiseWinnerTie {
		return fmt.Errorf("judges: invalid pairwise winner %q", comparison.Winner)
	}
	if comparison.Margin != contracts.PairwiseMarginDecisive && comparison.Margin != contracts.PairwiseMarginClear && comparison.Margin != contracts.PairwiseMarginSlight {
		return fmt.Errorf("judges: invalid pairwise margin %q", comparison.Margin)
	}
	return nil
}

func validateDecision(decision PairwiseDecision) error {
	switch decision.Action {
	case PairwiseDecisionAdopt, PairwiseDecisionReject, PairwiseDecisionInconclusive:
	default:
		return fmt.Errorf("judges: invalid pairwise decision %q", decision.Action)
	}
	for _, row := range decision.AgentDecisions {
		if row.Agent == "" {
			return fmt.Errorf("judges: pairwise decision agent is required")
		}
		if row.Winner != contracts.PairwiseWinnerA && row.Winner != contracts.PairwiseWinnerB && row.Winner != contracts.PairwiseWinnerTie {
			return fmt.Errorf("judges: invalid pairwise decision winner %q", row.Winner)
		}
	}
	return nil
}

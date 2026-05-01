package step10restorebase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

const defaultTaskBriefGeneratorTimeout = 2 * time.Minute

type TaskBriefGenerator interface {
	GenerateTaskBrief(ctx context.Context, input TaskBriefInput) (string, error)
}

type CLITaskBriefGenerator struct {
	Profile  agents.Profile
	RepoRoot string
	Timeout  time.Duration
}

func NewCLITaskBriefGenerator(profile agents.Profile, repoRoot string) CLITaskBriefGenerator {
	return CLITaskBriefGenerator{
		Profile:  profile,
		RepoRoot: repoRoot,
		Timeout:  defaultTaskBriefGeneratorTimeout,
	}
}

func (g CLITaskBriefGenerator) GenerateTaskBrief(ctx context.Context, input TaskBriefInput) (string, error) {
	promptText := RenderTaskBriefGeneratorPrompt(input)
	responsePath, err := runTaskBriefGeneratorCLI(ctx, g.Profile, g.RepoRoot, promptText, g.timeout())
	if err != nil {
		return "", err
	}
	defer os.Remove(responsePath)
	task, err := readTaskBriefGeneratorResponse(responsePath)
	if err != nil {
		return "", err
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("step10: task brief generator returned empty task")
	}
	return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(task, reconstructedPromptMaxBytes), reconstructedPromptMaxBytes), nil
}

func (g CLITaskBriefGenerator) timeout() time.Duration {
	if g.Timeout <= 0 {
		return defaultTaskBriefGeneratorTimeout
	}
	return g.Timeout
}

func RenderTaskBriefGeneratorPrompt(input TaskBriefInput) string {
	usableIssues := usableLinkedIssues(input.Issues)
	changedTests, changedNonTests := splitChangedFiles(input.ChangedFiles)

	var b strings.Builder
	b.WriteString("You reconstruct the original task request behind a merged PR.\n\n")
	b.WriteString("Return ONLY one JSON object with this shape:\n")
	b.WriteString("{\"task\":\"...\"}\n\n")
	b.WriteString("Task-writing rules:\n")
	b.WriteString("- Write an issue-like task description for an implementation agent.\n")
	b.WriteString("- If a linked issue alone is specific enough to lead to this implementation, return that issue text with only minimal formatting.\n")
	b.WriteString("- If the issue is too thin, enrich it using the PR title/body, changed tests, changed files, and diff evidence.\n")
	b.WriteString("- If no usable issue exists, reconstruct the task from the PR and diff evidence.\n")
	b.WriteString("- Do not describe the diff as implementation instructions.\n")
	b.WriteString("- Do not tell the implementation agent to inspect, replay, copy, or reproduce the diff.\n")
	b.WriteString("- Do not include low-level code-change summaries unless they are necessary to understand the task.\n")
	b.WriteString("- Separate application work from external/PdM/operator tasks when the evidence makes that distinction clear.\n")
	b.WriteString("- Use the same language as the strongest source context. If the context is mixed or unclear, use Japanese.\n")
	b.WriteString("- Keep the granularity similar to a normal GitHub issue body, not a detailed specification document.\n\n")

	b.WriteString("Source evidence follows. Treat it as evidence, not as the final task text.\n\n")
	fmt.Fprintf(&b, "PR: #%d\n", input.PR)
	if title := strings.TrimSpace(input.Title); title != "" {
		fmt.Fprintf(&b, "\nPR title:\n%s\n", sanitizeGeneratorText("pr_title", title))
	}
	if body := strings.TrimSpace(input.Body); body != "" {
		fmt.Fprintf(&b, "\nPR body:\n%s\n", sanitizeGeneratorText("pr_body", body))
	}
	if len(usableIssues) > 0 {
		b.WriteString("\nLinked issues:\n")
		for _, issue := range usableIssues {
			fmt.Fprintf(&b, "\n#%d: %s\n", issue.Number, sanitizeGeneratorText("issue_title", issue.Title))
			if body := strings.TrimSpace(issue.Body); body != "" {
				b.WriteString(sanitizeGeneratorText("issue_body", body))
				b.WriteString("\n")
			}
		}
	}
	if len(changedTests) > 0 {
		b.WriteString("\nChanged tests:\n")
		for _, file := range changedTests {
			fmt.Fprintf(&b, "- %s\n", sanitizeGeneratorLine(file))
		}
	}
	if len(changedNonTests) > 0 {
		b.WriteString("\nChanged files:\n")
		for _, file := range changedNonTests {
			fmt.Fprintf(&b, "- %s\n", sanitizeGeneratorLine(file))
		}
	}
	if diff := strings.TrimSpace(input.Diff); diff != "" {
		b.WriteString(renderDiffExcerptForGenerator(diff, b.Len()))
	}
	return b.String()
}

func sanitizeGeneratorText(label, value string) string {
	return internalio.SanitizeForPromptEmbedding(value, internalio.SafeTextOptions{
		Label: label,
		Fence: true,
	})
}

func sanitizeGeneratorLine(value string) string {
	return internalio.SanitizeForPromptEmbedding(value)
}

func runTaskBriefGeneratorCLI(ctx context.Context, profile agents.Profile, workdir, promptText string, timeout time.Duration) (string, error) {
	command, err := agentrunner.PrepareReadOnlyCommand(profile, workdir)
	if err != nil {
		return "", err
	}
	defer command.Cleanup()

	result, err := agentrunner.RunCommand(ctx, agentrunner.CommandRequest{
		Binary:      command.Binary,
		Args:        command.Args,
		Workdir:     command.Workdir,
		Prompt:      promptText,
		SessionPath: command.SessionPath,
		Timeout:     timeout,
		Env:         command.Env,
		ErrPrefix:   "step10 task brief generator",
	})
	if err != nil {
		return "", err
	}
	if err := validateTaskBriefGeneratorResult(result); err != nil {
		return "", err
	}
	return command.ResponsePath, nil
}

func validateTaskBriefGeneratorResult(result agentrunner.CommandResult) error {
	switch {
	case result.TimedOut:
		return fmt.Errorf("step10: task brief generator timed out: stdout=%q stderr=%q", generatorSnippet(result.StdoutSnippet), generatorSnippet(result.StderrSnippet))
	case result.ExitCode != 0:
		return fmt.Errorf("step10: task brief generator exited with code %d: stdout=%q stderr=%q", result.ExitCode, generatorSnippet(result.StdoutSnippet), generatorSnippet(result.StderrSnippet))
	case result.CleanupErr != nil:
		return fmt.Errorf("step10: task brief generator cleanup failed: %w", result.CleanupErr)
	default:
		return nil
	}
}

func readTaskBriefGeneratorResponse(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	payload := extractGeneratorJSONObject(bytes.TrimSpace(data))
	var response struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(payload, &response); err == nil && strings.TrimSpace(response.Task) != "" {
		return response.Task, nil
	}
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &wrapper); err == nil && strings.TrimSpace(wrapper.Result) != "" {
		payload = extractGeneratorJSONObject([]byte(wrapper.Result))
		if err := json.Unmarshal(payload, &response); err == nil && strings.TrimSpace(response.Task) != "" {
			return response.Task, nil
		}
	}
	return "", fmt.Errorf("step10: parse task brief generator output")
}

func extractGeneratorJSONObject(data []byte) []byte {
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

func renderDiffExcerptForGenerator(diff string, currentLen int) string {
	const wrapperOverhead = len("\nDiff excerpt:\n") + len(`<untrusted-text source="diff">`) + len("\n</untrusted-text>\n")
	remaining := reconstructedPromptMaxBytes - currentLen - wrapperOverhead
	if remaining <= 0 {
		return ""
	}
	if remaining > diffExcerptMaxBytes {
		remaining = diffExcerptMaxBytes
	}
	body := strings.TrimRight(truncateUTF8Bytes(diff, remaining), "\n")
	if body == "" {
		return ""
	}
	return "\nDiff excerpt:\n" + sanitizeGeneratorText("diff", body) + "\n"
}

func generatorSnippet(value []byte) string {
	return strings.TrimSpace(string(value))
}

var _ TaskBriefGenerator = CLITaskBriefGenerator{}

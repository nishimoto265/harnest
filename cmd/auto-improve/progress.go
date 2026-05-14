package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/nishimoto265/harnest/internal/processenv"
	"github.com/spf13/cobra"
)

const (
	progressWatchInterval       = time.Second
	progressRenderInterval      = 200 * time.Millisecond
	progressDataRefreshInterval = time.Second
)

type cliOutputOptions struct {
	JSON    bool
	Quiet   bool
	Verbose bool
}

func bindOutputFlags(cmd *cobra.Command, opts *cliOutputOptions) {
	cmd.PersistentFlags().BoolVar(&opts.JSON, "json", false, "Emit machine-readable progress events")
	cmd.PersistentFlags().BoolVar(&opts.Quiet, "quiet", false, "Suppress human progress output")
	cmd.PersistentFlags().BoolVar(&opts.Verbose, "verbose", false, "Emit more detailed human progress output")
}

func validateOutputOptions(opts cliOutputOptions) error {
	if opts.JSON && opts.Quiet {
		return commandExitError{code: 2, msg: cliErrorPrefix() + " --json and --quiet are mutually exclusive"}
	}
	if opts.Quiet && opts.Verbose {
		return commandExitError{code: 2, msg: cliErrorPrefix() + " --quiet and --verbose are mutually exclusive"}
	}
	return nil
}

type progressAwareRunner interface {
	SetProgressObserver(orchestrator.ProgressObserver)
}

func attachProgressReporter(runner pipelineRunner, reporter *cliProgressReporter) {
	if reporter == nil {
		return
	}
	if aware, ok := runner.(progressAwareRunner); ok {
		aware.SetProgressObserver(reporter)
	}
}

type cliProgressReporter struct {
	mode      string
	verbose   bool
	live      bool
	out       io.Writer
	tty       *os.File
	encoder   *json.Encoder
	liveState liveProgressState

	mu             sync.Mutex
	watchMu        sync.Mutex
	watchers       map[string]context.CancelFunc
	watchWG        sync.WaitGroup
	liveLoopMu     sync.Mutex
	liveLoopCancel context.CancelFunc
	liveLoopDone   chan struct{}
	pulse          int
	liveFrame      int
	lastFrameLines int
}

func newCLIProgressReporter(cmd *cobra.Command, opts cliOutputOptions) *cliProgressReporter {
	if opts.Quiet {
		return nil
	}
	if opts.JSON {
		return &cliProgressReporter{
			mode:    "json",
			out:     cmd.OutOrStdout(),
			encoder: json.NewEncoder(cmd.OutOrStdout()),
		}
	}
	if !opts.Verbose {
		if tty, err := openProgressTTY(); err == nil {
			_, _ = fmt.Fprint(tty, "\x1b[?25l")
			return &cliProgressReporter{
				mode:     "human",
				live:     true,
				out:      tty,
				tty:      tty,
				watchers: map[string]context.CancelFunc{},
			}
		}
	}
	return &cliProgressReporter{
		mode:     "human",
		verbose:  opts.Verbose,
		out:      cmd.ErrOrStderr(),
		watchers: map[string]context.CancelFunc{},
	}
}

func openProgressTTY() (*os.File, error) {
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return nil, os.ErrInvalid
	}
	return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
}

func (r *cliProgressReporter) OnProgress(ctx context.Context, event orchestrator.ProgressEvent) {
	if r == nil {
		return
	}
	if r.mode == "json" {
		r.mu.Lock()
		defer r.mu.Unlock()
		_ = r.encoder.Encode(event)
		return
	}
	r.onHumanProgress(ctx, event)
}

func (r *cliProgressReporter) Close() {
	if r == nil {
		return
	}
	r.stopLiveSection()
	r.watchMu.Lock()
	for key, cancel := range r.watchers {
		cancel()
		delete(r.watchers, key)
	}
	r.watchMu.Unlock()
	r.watchWG.Wait()
	if r.live {
		r.mu.Lock()
		if r.lastFrameLines > 0 {
			_, _ = fmt.Fprint(r.out, "\n")
			r.lastFrameLines = 0
		}
		_, _ = fmt.Fprint(r.out, "\x1b[?25h\x1b[0m")
		r.mu.Unlock()
	}
	if r.tty != nil {
		_ = r.tty.Close()
	}
}

func (r *cliProgressReporter) onHumanProgress(ctx context.Context, event orchestrator.ProgressEvent) {
	if r.live {
		r.onLiveProgress(ctx, event)
		return
	}
	r.onAppendProgress(event)
}

func (r *cliProgressReporter) onAppendProgress(event orchestrator.ProgressEvent) {
	switch event.Event {
	case orchestrator.ProgressRunStart:
		r.printf("%s: PR%d run %s\n", cliCommandName, event.PR, event.RunID)
		if r.verbose {
			r.printf("run dir: %s\n", event.RunDir)
		}
	case orchestrator.ProgressRunDone:
		r.stopAllWatchers()
		if event.Error != "" {
			r.printf("%s: PR%d failed: %s\n", cliCommandName, event.PR, event.Error)
			return
		}
		r.printf("%s: PR%d finished\n", cliCommandName, event.PR)
	case orchestrator.ProgressStepStart:
		r.printf("\n%s\n", stepHeading(event.Step))
		if event.Step == contracts.FailedStep20 || event.Step == contracts.FailedStep50 {
			r.printAgentSnapshot(event, false)
			r.startAgentWatcher(event)
		}
	case orchestrator.ProgressStepDone:
		if event.Step == contracts.FailedStep20 || event.Step == contracts.FailedStep50 {
			r.stopAgentWatcher(event)
			r.printAgentSnapshot(event, true)
			return
		}
		r.printStepSummary(event)
	case orchestrator.ProgressStepSkip:
		r.printf("skip: %s\n", event.Message)
	case orchestrator.ProgressAgentStart, orchestrator.ProgressAgentDone:
		if r.verbose {
			r.printAgentEvent(event)
		}
	}
}

type liveProgressState struct {
	RunID   contracts.RunID
	PR      int
	RunDir  string
	Step    contracts.FailedStep
	Status  string
	Summary []string
	Rows    []agentSnapshot
	Footer  string
}

func runningLiveSectionState(event orchestrator.ProgressEvent) liveProgressState {
	state := liveProgressState{
		RunID:  event.RunID,
		PR:     event.PR,
		RunDir: event.RunDir,
		Step:   event.Step,
		Status: "running",
	}
	if isAgentProgressStep(event.Step) {
		state.Rows = readAgentSnapshots(event.RunDir, event.Pass)
		return state
	}
	state.Summary = runningStepSummaryLines(event)
	return state
}

func doneLiveSectionState(event orchestrator.ProgressEvent) liveProgressState {
	state := liveProgressState{
		RunID:  event.RunID,
		PR:     event.PR,
		RunDir: event.RunDir,
		Step:   event.Step,
		Status: "done",
	}
	if isAgentProgressStep(event.Step) {
		state.Rows = readAgentSnapshots(event.RunDir, event.Pass)
		return state
	}
	state.Summary = stepSummaryLines(event)
	return state
}

func isAgentProgressStep(step contracts.FailedStep) bool {
	return step == contracts.FailedStep20 || step == contracts.FailedStep50
}

func (r *cliProgressReporter) onLiveProgress(_ context.Context, event orchestrator.ProgressEvent) {
	switch event.Event {
	case orchestrator.ProgressRunStart:
		r.commitLiveLines([]string{
			fmt.Sprintf("%s  PR%d  run %s", productName, event.PR, event.RunID),
		})
		r.setLiveBase(event)
	case orchestrator.ProgressRunDone:
		r.stopLiveSection()
		r.stopAllWatchers()
		if event.Error != "" {
			r.commitLiveLines([]string{fmt.Sprintf("%s: PR%d failed: %s", cliCommandName, event.PR, event.Error)})
			return
		}
		r.commitLiveLines([]string{fmt.Sprintf("%s: PR%d finished", cliCommandName, event.PR)})
	case orchestrator.ProgressStepStart:
		r.startLiveSection(event)
	case orchestrator.ProgressStepDone:
		r.stopLiveSection()
		r.commitLiveSection(doneLiveSectionState(event))
	case orchestrator.ProgressStepSkip:
		r.stopLiveSection()
		r.commitLiveSection(liveProgressState{
			RunID:   event.RunID,
			PR:      event.PR,
			RunDir:  event.RunDir,
			Step:    event.Step,
			Status:  "skipped",
			Summary: []string{"skip: " + event.Message},
		})
	}
}

func (r *cliProgressReporter) startLiveSection(event orchestrator.ProgressEvent) {
	r.stopLiveSection()
	r.updateLive(func(state *liveProgressState) {
		*state = runningLiveSectionState(event)
	})

	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.liveLoopMu.Lock()
	r.liveLoopCancel = cancel
	r.liveLoopDone = done
	r.liveLoopMu.Unlock()

	go func() {
		defer close(done)
		renderTicker := time.NewTicker(progressRenderInterval)
		defer renderTicker.Stop()
		refreshTicker := time.NewTicker(progressDataRefreshInterval)
		defer refreshTicker.Stop()

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-renderTicker.C:
				r.renderLive()
			case <-refreshTicker.C:
				state := runningLiveSectionState(event)
				r.updateLive(func(current *liveProgressState) {
					*current = state
				})
			}
		}
	}()
}

func (r *cliProgressReporter) stopLiveSection() {
	r.liveLoopMu.Lock()
	cancel := r.liveLoopCancel
	done := r.liveLoopDone
	r.liveLoopCancel = nil
	r.liveLoopDone = nil
	r.liveLoopMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (r *cliProgressReporter) setLiveBase(event orchestrator.ProgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveState = liveProgressState{
		RunID:  event.RunID,
		PR:     event.PR,
		RunDir: event.RunDir,
	}
}

func (r *cliProgressReporter) updateLive(mutator func(*liveProgressState)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mutator(&r.liveState)
	r.renderLiveLocked()
}

func (r *cliProgressReporter) commitLiveLines(lines []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLiveFrameLocked()
	if len(lines) > 0 {
		_, _ = fmt.Fprint(r.out, strings.Join(lines, "\n"))
		_, _ = fmt.Fprint(r.out, "\n\n")
	}
}

func (r *cliProgressReporter) commitLiveSection(state liveProgressState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := r.liveLinesForStateLocked(state)
	r.clearLiveFrameLocked()
	if len(lines) > 0 {
		_, _ = fmt.Fprint(r.out, strings.Join(lines, "\n"))
		_, _ = fmt.Fprint(r.out, "\n\n")
	}
	r.liveState = liveProgressState{
		RunID:  state.RunID,
		PR:     state.PR,
		RunDir: state.RunDir,
	}
}

func (r *cliProgressReporter) renderLive() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renderLiveLocked()
}

func (r *cliProgressReporter) renderLiveLocked() {
	r.liveFrame++
	lines := r.liveLinesLocked()
	if len(lines) == 0 {
		return
	}
	r.clearLiveFrameLocked()
	_, _ = fmt.Fprint(r.out, strings.Join(lines, "\n"))
	_, _ = fmt.Fprint(r.out, "\n")
	r.lastFrameLines = len(lines)
}

func (r *cliProgressReporter) clearLiveFrameLocked() {
	if r.lastFrameLines == 0 {
		return
	}
	_, _ = fmt.Fprintf(r.out, "\x1b[%dA\r\x1b[J", r.lastFrameLines)
	r.lastFrameLines = 0
}

func (r *cliProgressReporter) liveLinesLocked() []string {
	return r.liveLinesForStateLocked(r.liveState)
}

func (r *cliProgressReporter) liveLinesForStateLocked(state liveProgressState) []string {
	if state.RunID == "" && state.Step == "" {
		return nil
	}
	status := state.Status
	if status == "" {
		status = "running"
	}
	if status == "running" {
		status = shimmerText("running", r.liveFrame) + " " + spinnerFrame(r.liveFrame)
	}
	var lines []string
	if state.Step != "" {
		lines = append(lines, fmt.Sprintf("%s  %s", stepHeading(state.Step), status))
	} else {
		lines = append(lines, status)
	}
	if len(state.Rows) > 0 {
		lines = append(lines, "")
		lines = append(lines, agentSnapshotLines(state.Rows)...)
	}
	if len(state.Summary) > 0 {
		lines = append(lines, "")
		lines = append(lines, state.Summary...)
	}
	if state.Footer != "" {
		lines = append(lines, "", state.Footer)
	}
	return lines
}

func shimmerText(text string, frame int) string {
	if os.Getenv("NO_COLOR") != "" {
		return text
	}
	runes := []rune(text)
	var b strings.Builder
	for i, r := range runes {
		distance := (i - frame) % len(runes)
		if distance < 0 {
			distance += len(runes)
		}
		level := 105
		switch distance {
		case 0:
			level = 245
		case 1, len(runes) - 1:
			level = 190
		case 2, len(runes) - 2:
			level = 145
		}
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%c", level, level, level, r)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func spinnerFrame(frame int) string {
	frames := []string{"◐", "◓", "◑", "◒"}
	return frames[frame%len(frames)]
}

func (r *cliProgressReporter) printf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = fmt.Fprintf(r.out, format, args...)
}

func (r *cliProgressReporter) startAgentWatcher(event orchestrator.ProgressEvent) {
	key := watcherKey(event)
	r.watchMu.Lock()
	if _, exists := r.watchers[key]; exists {
		r.watchMu.Unlock()
		return
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	r.watchers[key] = cancel
	r.watchWG.Add(1)
	r.watchMu.Unlock()

	go func() {
		defer r.watchWG.Done()
		ticker := time.NewTicker(progressWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if r.live {
					rows := readAgentSnapshots(event.RunDir, event.Pass)
					r.updateLive(func(state *liveProgressState) {
						state.Rows = rows
					})
				} else {
					r.printAgentSnapshot(event, false)
				}
			}
		}
	}()
}

func (r *cliProgressReporter) stopAgentWatcher(event orchestrator.ProgressEvent) {
	key := watcherKey(event)
	r.watchMu.Lock()
	cancel := r.watchers[key]
	delete(r.watchers, key)
	r.watchMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *cliProgressReporter) stopAllWatchers() {
	r.watchMu.Lock()
	for key, cancel := range r.watchers {
		cancel()
		delete(r.watchers, key)
	}
	r.watchMu.Unlock()
}

func watcherKey(event orchestrator.ProgressEvent) string {
	return fmt.Sprintf("%s:%s", event.RunID, event.Step)
}

func stepHeading(step contracts.FailedStep) string {
	switch step {
	case contracts.FailedStep10:
		return "[10] マージ済みPRからタスクを再生成"
	case contracts.FailedStep20:
		return "[20] pass1: 現行Harnessで3エージェントが実装中"
	case contracts.FailedStep30:
		return "[30] pass1 の実装を採点"
	case contracts.FailedStep40:
		return "[40] 採点結果から lesson / checklist を生成"
	case contracts.FailedStep50:
		return "[50] pass2: 改善後Harnessで再実装"
	case contracts.FailedStep60:
		return "[60] pass2 の実装を採点し pairwise 比較"
	case contracts.FailedStep70:
		return "[70] 採用可否を判断"
	default:
		return fmt.Sprintf("[%s] running", step)
	}
}

func (r *cliProgressReporter) printAgentEvent(event orchestrator.ProgressEvent) {
	state := "start"
	if event.Event == orchestrator.ProgressAgentDone {
		state = "done"
	}
	if event.Error != "" {
		r.printf("  %s %s error: %s\n", event.Agent, state, event.Error)
		return
	}
	if event.Message != "" {
		r.printf("  %s %s: %s\n", event.Agent, state, event.Message)
		return
	}
	r.printf("  %s %s\n", event.Agent, state)
}

func (r *cliProgressReporter) printAgentSnapshot(event orchestrator.ProgressEvent, final bool) {
	rows := readAgentSnapshots(event.RunDir, event.Pass)
	if len(rows) == 0 {
		return
	}
	label := "status"
	if final {
		label = "result"
	}
	r.printf("%s %s\n", r.nextPulse(), label)
	for _, line := range agentSnapshotLines(rows) {
		r.printf("%s\n", line)
	}
}

func agentSnapshotLines(rows []agentSnapshot) []string {
	lines := []string{"agent  status    time    activity                         diff      checklist"}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%-5s  %-8s  %-6s  %-32s %-9s %s",
			row.Agent,
			row.Status,
			row.Elapsed,
			truncateDisplay(row.Activity, 32),
			truncateDisplay(row.Diff, 9),
			row.Checklist,
		))
	}
	return lines
}

func (r *cliProgressReporter) nextPulse() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	states := []string{"[.  ]", "[.. ]", "[...]"}
	pulse := states[r.pulse%len(states)]
	r.pulse++
	return pulse
}

func (r *cliProgressReporter) printStepSummary(event orchestrator.ProgressEvent) {
	for _, line := range stepSummaryLines(event) {
		r.printf("%s\n", line)
	}
}

func stepSummaryLines(event orchestrator.ProgressEvent) []string {
	switch event.Step {
	case contracts.FailedStep10:
		return taskSummaryLines(event.RunDir)
	case contracts.FailedStep30:
		return scoreSummaryLines(event.RunDir, "30/scores-A.jsonl", "30/issues-A.jsonl")
	case contracts.FailedStep40:
		return candidateSummaryLines(event.RunDir)
	case contracts.FailedStep60:
		return passComparisonSummaryLines(event.RunDir)
	case contracts.FailedStep70:
		return decisionSummaryLines(event.RunDir)
	default:
		return []string{"done"}
	}
}

func runningStepSummaryLines(event orchestrator.ProgressEvent) []string {
	switch event.Step {
	case contracts.FailedStep10:
		return summaryOrRunningMessage(taskSummaryLines(event.RunDir), "status: タスク文を生成中")
	case contracts.FailedStep30:
		return summaryOrRunningMessage(scoreProgressLines(event.RunDir, 1, "30/scores-A.jsonl", "30/issues-A.jsonl", ""), "status: pass1 の score / issue を生成中")
	case contracts.FailedStep40:
		return summaryOrRunningMessage(candidateSummaryLines(event.RunDir), "status: lesson / checklist を生成中")
	case contracts.FailedStep60:
		return summaryOrRunningMessage(scoreProgressLines(event.RunDir, 2, "60/scores-B.jsonl", "", "60/pairwise.jsonl"), "status: pass2 採点と pairwise 比較中")
	case contracts.FailedStep70:
		return summaryOrRunningMessage(decisionSummaryLines(event.RunDir), "status: 採用可否を判断中")
	default:
		return []string{"status: 実行中"}
	}
}

func summaryOrRunningMessage(summary []string, message string) []string {
	if len(summary) == 0 || (len(summary) == 1 && summary[0] == "done") {
		return []string{message}
	}
	return summary
}

func taskSummaryLines(runDir string) []string {
	pkg, ok := readJSONFile[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	if !ok {
		return []string{"done"}
	}
	lines := []string{fmt.Sprintf("target PR: PR%d / %s", pkg.PR, pkg.Title)}
	taskLines := displayTaskBrief(pkg.ReconstructedTaskPrompt)
	if len(taskLines) > 0 {
		lines = append(lines, "task:")
		for _, line := range taskLines {
			lines = append(lines, "  "+line)
		}
	}
	return lines
}

func displayTaskBrief(prompt string) []string {
	const maxLines = 4
	var lines []string
	section := ""
	for _, raw := range strings.Split(strings.TrimSpace(prompt), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			section = normalizeTaskDisplaySection(line)
			if strings.HasPrefix(strings.ToLower(section), "issue ") {
				title := strings.TrimSpace(strings.TrimPrefix(line, "#"))
				if title != "" {
					lines = appendTaskDisplayLine(lines, title, maxLines)
				}
			}
			continue
		}
		if isTaskDisplaySourceSection(section) {
			continue
		}
		if isTaskDisplayBoilerplate(line) {
			continue
		}
		lines = appendTaskDisplayLine(lines, line, maxLines)
		if len(lines) >= maxLines {
			break
		}
	}
	if len(lines) > 0 {
		return lines
	}
	for _, raw := range strings.Split(strings.TrimSpace(prompt), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || isTaskDisplayBoilerplate(line) {
			continue
		}
		return []string{truncateDisplay(line, 110)}
	}
	return nil
}

func normalizeTaskDisplaySection(line string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
}

func isTaskDisplaySourceSection(section string) bool {
	switch {
	case strings.Contains(section, "background"),
		strings.Contains(section, "source context"),
		strings.Contains(section, "pr context"),
		strings.Contains(section, "changed files"),
		strings.Contains(section, "changed tests"),
		strings.Contains(section, "linked issues"):
		return true
	default:
		return false
	}
}

func isTaskDisplayBoilerplate(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "-")))
	switch {
	case strings.HasPrefix(lower, "avoid unrelated refactors"),
		strings.HasPrefix(lower, "use the linked issue"),
		strings.HasPrefix(lower, "use the pr body"),
		strings.HasPrefix(lower, "the original issue text is unavailable"),
		strings.HasPrefix(lower, "title:"),
		strings.HasPrefix(lower, "body:"):
		return true
	default:
		return false
	}
}

func appendTaskDisplayLine(lines []string, line string, maxLines int) []string {
	if len(lines) >= maxLines {
		return lines
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
	line = strings.TrimSpace(line)
	if line == "" {
		return lines
	}
	return append(lines, truncateDisplay(line, 110))
}

func scoreSummaryLines(runDir, scoreRel, issueRel string) []string {
	var lines []string
	scores := readScoreAverages(filepath.Join(runDir, scoreRel))
	if len(scores) > 0 {
		lines = append(lines, "agent  score")
		for _, score := range scores {
			lines = append(lines, fmt.Sprintf("%-5s  %.1f", score.Agent, score.Average))
		}
	}
	issues := readIssueCounts(filepath.Join(runDir, issueRel))
	if issues.Total > 0 {
		lines = append(lines, fmt.Sprintf("observed issues: critical=%d high=%d medium=%d low=%d total=%d",
			issues.Critical, issues.High, issues.Medium, issues.Low, issues.Total))
	}
	if len(lines) == 0 {
		return []string{"done"}
	}
	return lines
}

func candidateSummaryLines(runDir string) []string {
	candidates, ok := readJSONFile[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	if !ok {
		return []string{"done"}
	}
	lines := []string{fmt.Sprintf("candidates: %d", len(candidates.Candidates))}
	for i, candidate := range candidates.Candidates {
		if i >= 5 {
			lines = append(lines, fmt.Sprintf("  ... %d more", len(candidates.Candidates)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s", candidate.Kind, candidate.Title))
	}
	return lines
}

func passComparisonSummaryLines(runDir string) []string {
	var lines []string
	pass1 := readScoreAverageMap(filepath.Join(runDir, "30", "scores-A.jsonl"))
	pass2 := readScoreAverageMap(filepath.Join(runDir, "60", "scores-B.jsonl"))
	agents := sortedScoreAgents(pass1, pass2)
	if len(agents) > 0 {
		lines = append(lines, "agent  pass1   pass2   delta")
		for _, agent := range agents {
			left, leftOK := pass1[agent]
			right, rightOK := pass2[agent]
			if !leftOK || !rightOK {
				continue
			}
			lines = append(lines, fmt.Sprintf("%-5s  %-6.1f  %-6.1f  %+0.1f", agent, left, right, right-left))
		}
	}
	pairwise := readPairwiseCounts(filepath.Join(runDir, "60", "pairwise.jsonl"))
	if pairwise.Total > 0 {
		lines = append(lines, fmt.Sprintf("pairwise: pass1=%d pass2=%d tie=%d total=%d", pairwise.Pass1, pairwise.Pass2, pairwise.Tie, pairwise.Total))
	}
	if len(lines) == 0 {
		return []string{"done"}
	}
	return lines
}

func decisionSummaryLines(runDir string) []string {
	decision, ok := readJSONFile[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	if !ok {
		return []string{"done"}
	}
	switch value := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return []string{fmt.Sprintf("result: accepted target=%s", shortSHA(value.TargetSha)), "decision: improved harness adopted"}
	case *contracts.DecisionAdopt:
		if value != nil {
			return []string{fmt.Sprintf("result: accepted target=%s", shortSHA(value.TargetSha)), "decision: improved harness adopted"}
		}
	case contracts.DecisionReject:
		return []string{fmt.Sprintf("decision: reject reason=%s", value.Reason)}
	case *contracts.DecisionReject:
		if value != nil {
			return []string{fmt.Sprintf("decision: reject reason=%s", value.Reason)}
		}
	case contracts.DecisionNoop:
		return []string{fmt.Sprintf("decision: noop reason=%s", value.Reason)}
	case *contracts.DecisionNoop:
		if value != nil {
			return []string{fmt.Sprintf("decision: noop reason=%s", value.Reason)}
		}
	case contracts.DecisionRollback:
		return []string{fmt.Sprintf("decision: rollback reason=%s step=%s", value.RollbackReason, value.FailedStep)}
	case *contracts.DecisionRollback:
		if value != nil {
			return []string{fmt.Sprintf("decision: rollback reason=%s step=%s", value.RollbackReason, value.FailedStep)}
		}
	default:
		return []string{fmt.Sprintf("decision: %s", decision.Action)}
	}
	return []string{fmt.Sprintf("decision: %s", decision.Action)}
}

type agentSnapshot struct {
	Agent     string
	Status    string
	Elapsed   string
	Activity  string
	Diff      string
	Checklist string
}

func readAgentSnapshots(runDir string, pass int) []agentSnapshot {
	agents := agentsForPass(runDir, pass)
	rows := make([]agentSnapshot, 0, len(agents))
	for _, agent := range agents {
		rows = append(rows, readAgentSnapshot(runDir, pass, agent))
	}
	return rows
}

func agentsForPass(runDir string, pass int) []string {
	pkg, ok := readJSONFile[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	if !ok {
		return []string{"a1", "a2", "a3"}
	}
	seen := map[string]struct{}{}
	var agents []string
	for _, allocation := range pkg.Worktrees {
		if allocation.Pass != pass {
			continue
		}
		agent := string(allocation.Agent)
		if _, exists := seen[agent]; exists {
			continue
		}
		seen[agent] = struct{}{}
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	if len(agents) == 0 {
		return []string{"a1", "a2", "a3"}
	}
	return agents
}

func readAgentSnapshot(runDir string, pass int, agent string) agentSnapshot {
	dir := filepath.Join(runDir, passDirName(pass), agent)
	row := agentSnapshot{
		Agent:     agent,
		Status:    "pending",
		Elapsed:   "-",
		Activity:  fallbackActivity(pass, false),
		Diff:      "-",
		Checklist: "-",
	}
	if manifest, ok := readJSONFile[contracts.Manifest](filepath.Join(dir, "manifest.json")); ok {
		row.Status, row.Elapsed, row.Activity = manifestSummary(manifest)
		row.Diff = diffSummary(filepath.Join(dir, "diff.patch"))
		row.Checklist = checklistSummary(filepath.Join(dir, "checklist-result.json"))
		return row
	}
	state := readResumeState(filepath.Join(dir, ".resume-state.json"))
	if state.StartedAt.IsZero() {
		if info, err := os.Stat(filepath.Join(dir, ".heartbeat")); err == nil {
			state.StartedAt = info.ModTime().UTC()
			state.LastHeartbeat = info.ModTime().UTC()
		}
	}
	if !state.StartedAt.IsZero() {
		row.Status = heartbeatStatus(filepath.Join(dir, ".heartbeat"))
		row.Elapsed = durationDisplay(time.Since(state.StartedAt))
		row.Activity = tailSessionActivity(filepath.Join(dir, "session.jsonl"), "")
		if activity, diff := runningWorktreeSummary(runDir, pass, agent); activity != "" || diff != "" {
			if row.Activity == "" || row.Activity == "session.jsonl 更新中" {
				row.Activity = activity
			}
			if diff != "" {
				row.Diff = diff
			}
		}
		if row.Activity == "" {
			row.Activity = fallbackActivity(pass, true)
		}
	}
	if checklist := checklistSummary(filepath.Join(dir, "checklist-result.json")); checklist != "-" {
		row.Checklist = checklist
	}
	if diff := diffSummary(filepath.Join(dir, "diff.patch")); diff != "-" {
		row.Diff = diff
	}
	return row
}

func runningWorktreeSummary(runDir string, pass int, agent string) (string, string) {
	allocation, ok := worktreeAllocation(runDir, pass, agent)
	if !ok {
		return "", ""
	}
	statusOutput, ok := runGitDisplay(allocation.Path, "status", "--short")
	if !ok {
		return "", ""
	}
	statusLines := nonEmptyLines(statusOutput)
	activity := statusActivity(statusLines)
	diff := worktreeDiffSummary(allocation.Path, len(statusLines))
	return activity, diff
}

func worktreeAllocation(runDir string, pass int, agent string) (contracts.WorktreeAllocation, bool) {
	pkg, ok := readJSONFile[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	if !ok {
		return contracts.WorktreeAllocation{}, false
	}
	for _, allocation := range pkg.Worktrees {
		if allocation.Pass == pass && string(allocation.Agent) == agent {
			return allocation, true
		}
	}
	return contracts.WorktreeAllocation{}, false
}

func runGitDisplay(worktree string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmdArgs := append([]string{"-C", worktree}, args...)
	cmd, err := processenv.TrustedCommandContext(ctx, "git", cmdArgs...)
	if err != nil {
		return "", false
	}
	cmd.Env = processenv.GitLocalEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func statusActivity(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	first := lines[0]
	path := strings.TrimSpace(first)
	if len(first) > 3 {
		path = strings.TrimSpace(first[3:])
	}
	switch {
	case strings.HasPrefix(first, "??"):
		return "新規作成: " + path
	case strings.Contains(first[:min(len(first), 2)], "D"):
		return "削除中: " + path
	case strings.Contains(first[:min(len(first), 2)], "R"):
		return "移動中: " + path
	default:
		return "編集中: " + path
	}
}

func worktreeDiffSummary(worktree string, changedFiles int) string {
	output, ok := runGitDisplay(worktree, "diff", "--numstat", "HEAD", "--", ".")
	if !ok {
		return ""
	}
	additions := 0
	deletions := 0
	files := 0
	for _, line := range nonEmptyLines(output) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		add, addErr := strconv.Atoi(fields[0])
		del, delErr := strconv.Atoi(fields[1])
		if addErr != nil || delErr != nil {
			files++
			continue
		}
		additions += add
		deletions += del
		files++
	}
	if files > 0 {
		return fmt.Sprintf("+%d -%d", additions, deletions)
	}
	if changedFiles > 0 {
		return fmt.Sprintf("%d files", changedFiles)
	}
	return ""
}

func passDirName(pass int) string {
	if pass == 2 {
		return "50-pass2"
	}
	return "20-pass1"
}

type resumeStateLite struct {
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

func readResumeState(path string) resumeStateLite {
	data, err := os.ReadFile(path)
	if err != nil {
		return resumeStateLite{}
	}
	var state resumeStateLite
	if err := json.Unmarshal(data, &state); err != nil {
		return resumeStateLite{}
	}
	return state
}

func heartbeatStatus(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "running"
	}
	if time.Since(info.ModTime()) > 5*time.Minute {
		return "stale"
	}
	return "running"
}

func manifestSummary(manifest contracts.Manifest) (string, string, string) {
	switch value := manifest.Value.(type) {
	case contracts.ManifestSuccess:
		return "done", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), "checklist確認済み"
	case *contracts.ManifestSuccess:
		if value == nil {
			return "done", "-", "完了"
		}
		return "done", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), "checklist確認済み"
	case contracts.ManifestError:
		return "error", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), errorActivity(value.Reason, value.Detail)
	case *contracts.ManifestError:
		if value == nil {
			return "error", "-", "error"
		}
		return "error", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), errorActivity(value.Reason, value.Detail)
	case contracts.ManifestTimeout:
		return "timeout", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), "timeout"
	case *contracts.ManifestTimeout:
		if value == nil {
			return "timeout", "-", "timeout"
		}
		return "timeout", durationDisplay(value.FinishedAt.Sub(value.StartedAt)), "timeout"
	default:
		return string(manifest.Kind), "-", string(manifest.Kind)
	}
}

func errorActivity(reason, detail string) string {
	if detail == "" {
		return reason
	}
	return reason + ": " + detail
}

func fallbackActivity(pass int, running bool) string {
	if pass == 2 {
		if running {
			return "改善後Harnessで再実装中"
		}
		return "pass2 待機中"
	}
	if running {
		return "現行Harnessで実装中"
	}
	return "pass1 待機中"
}

func tailSessionActivity(path, fallback string) string {
	data, err := readFileTail(path, 32*1024)
	if err != nil || len(data) == 0 {
		return fallback
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var last string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		last = line
	}
	if last == "" {
		return fallback
	}
	if activity := activityFromJSONLine(last); activity != "" {
		return activity
	}
	if strings.HasPrefix(last, "{") {
		return "session.jsonl 更新中"
	}
	return last
}

func activityFromJSONLine(line string) string {
	var payload any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"message", "text", "content", "summary", "event", "type"} {
		if value := findStringKey(payload, key); value != "" {
			return value
		}
	}
	return ""
}

func findStringKey(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[key]; ok {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		for _, child := range typed {
			if text := findStringKey(child, key); text != "" {
				return text
			}
		}
	case []any:
		for _, child := range typed {
			if text := findStringKey(child, key); text != "" {
				return text
			}
		}
	}
	return ""
}

func readFileTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func diffSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return "-"
	}
	files := 0
	additions := 0
	deletions := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletions++
		}
	}
	if files == 0 && additions == 0 && deletions == 0 {
		return "-"
	}
	return fmt.Sprintf("+%d -%d", additions, deletions)
}

func checklistSummary(path string) string {
	result, ok := readJSONFile[contracts.ChecklistResult](path)
	if !ok {
		return "-"
	}
	total := len(result.Items)
	if total == 0 {
		return "0/0"
	}
	compliant := 0
	exceptions := 0
	for _, item := range result.Items {
		switch item.Verdict {
		case contracts.ChecklistItemCompliant, contracts.ChecklistItemNA:
			compliant++
		case contracts.ChecklistItemException:
			exceptions++
		}
	}
	if exceptions > 0 {
		return fmt.Sprintf("%d/%d ex:%d", compliant, total, exceptions)
	}
	return fmt.Sprintf("%d/%d", compliant, total)
}

type scoreAverage struct {
	Agent   string
	Average float64
}

type scoreProgress struct {
	Dimensions int
	Average    float64
}

const scoreDimensionTarget = 5

func scoreProgressLines(runDir string, pass int, scoreRel, issueRel, pairwiseRel string) []string {
	agents := agentsForPass(runDir, pass)
	if len(agents) == 0 {
		return []string{"done"}
	}
	scoreByAgent := readScoreProgress(filepath.Join(runDir, scoreRel))
	issuesByAgent := map[string]int{}
	if issueRel != "" {
		issuesByAgent = readIssueCountsByAgent(filepath.Join(runDir, issueRel))
	}
	pairwiseByAgent := map[string]string{}
	if pairwiseRel != "" {
		pairwiseByAgent = readPairwiseByAgent(filepath.Join(runDir, pairwiseRel))
	}

	var lines []string
	if pairwiseRel != "" {
		pass1 := readScoreAverageMap(filepath.Join(runDir, "30", "scores-A.jsonl"))
		lines = append(lines, "agent  status     pass1   pass2   pairwise")
		for _, agent := range agents {
			progress := scoreByAgent[agent]
			pass1Score, pass1OK := pass1[agent]
			lines = append(lines, fmt.Sprintf("%-5s  %-9s  %-6s  %-6s  %s",
				agent,
				scoreStatus(progress, pairwiseByAgent[agent]),
				scoreDisplay(pass1Score, pass1OK),
				scoreDisplay(progress.Average, progress.Dimensions > 0),
				valueOrDash(pairwiseByAgent[agent]),
			))
		}
		return lines
	}

	lines = append(lines, "agent  status     score   dims   issues")
	for _, agent := range agents {
		progress := scoreByAgent[agent]
		lines = append(lines, fmt.Sprintf("%-5s  %-9s  %-6s  %-5s  %s",
			agent,
			scoreStatus(progress, ""),
			scoreDisplay(progress.Average, progress.Dimensions > 0),
			dimensionDisplay(progress.Dimensions),
			countDisplay(issuesByAgent[agent]),
		))
	}
	return lines
}

func readScoreProgress(path string) map[string]scoreProgress {
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return map[string]scoreProgress{}
	}
	byAgent := map[string]map[contracts.Dimension]int{}
	for _, row := range rows {
		agent := string(row.Agent)
		if byAgent[agent] == nil {
			byAgent[agent] = map[contracts.Dimension]int{}
		}
		byAgent[agent][row.Dimension] = row.Score
	}
	out := map[string]scoreProgress{}
	for agent, dimensions := range byAgent {
		if len(dimensions) == 0 {
			continue
		}
		sum := 0
		for _, score := range dimensions {
			sum += score
		}
		out[agent] = scoreProgress{
			Dimensions: len(dimensions),
			Average:    math.Round((float64(sum)/float64(len(dimensions)))*10) / 10,
		}
	}
	return out
}

func readIssueCountsByAgent(path string) map[string]int {
	rows, err := internalio.ReadJSONL[contracts.IssueEntry](path)
	if err != nil {
		return map[string]int{}
	}
	seen := map[string]struct{}{}
	out := map[string]int{}
	for _, row := range rows {
		key := string(row.Agent) + "\x00" + row.IssueID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out[string(row.Agent)]++
	}
	return out
}

func readPairwiseByAgent(path string) map[string]string {
	rows, err := internalio.ReadJSONL[contracts.PairwiseEntry](path)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, row := range rows {
		out[string(row.AgentA)] = pairwiseWinnerDisplay(row.Winner)
	}
	return out
}

func scoreStatus(progress scoreProgress, pairwise string) string {
	switch {
	case pairwise != "":
		return "compared"
	case progress.Dimensions >= scoreDimensionTarget:
		return "scored"
	case progress.Dimensions > 0:
		return "scoring"
	default:
		return "waiting"
	}
}

func pairwiseWinnerDisplay(winner contracts.PairwiseWinner) string {
	switch winner {
	case contracts.PairwiseWinnerA:
		return "pass1"
	case contracts.PairwiseWinnerB:
		return "pass2"
	case contracts.PairwiseWinnerTie:
		return "tie"
	default:
		return string(winner)
	}
}

func scoreDisplay(score float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f", score)
}

func dimensionDisplay(dimensions int) string {
	if dimensions == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", dimensions, scoreDimensionTarget)
}

func countDisplay(count int) string {
	if count == 0 {
		return "-"
	}
	return strconv.Itoa(count)
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func readScoreAverages(path string) []scoreAverage {
	scoreMap := readScoreAverageMap(path)
	agents := make([]string, 0, len(scoreMap))
	for agent := range scoreMap {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	out := make([]scoreAverage, 0, len(agents))
	for _, agent := range agents {
		out = append(out, scoreAverage{Agent: agent, Average: scoreMap[agent]})
	}
	return out
}

func readScoreAverageMap(path string) map[string]float64 {
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil
	}
	byAgent := map[string]map[contracts.Dimension]int{}
	for _, row := range rows {
		agent := string(row.Agent)
		if byAgent[agent] == nil {
			byAgent[agent] = map[contracts.Dimension]int{}
		}
		byAgent[agent][row.Dimension] = row.Score
	}
	averages := map[string]float64{}
	for agent, dimensions := range byAgent {
		if len(dimensions) == 0 {
			continue
		}
		sum := 0
		for _, score := range dimensions {
			sum += score
		}
		averages[agent] = math.Round((float64(sum)/float64(len(dimensions)))*10) / 10
	}
	return averages
}

func sortedScoreAgents(left, right map[string]float64) []string {
	seen := map[string]struct{}{}
	for agent := range left {
		seen[agent] = struct{}{}
	}
	for agent := range right {
		seen[agent] = struct{}{}
	}
	agents := make([]string, 0, len(seen))
	for agent := range seen {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	return agents
}

type issueCounts struct {
	Critical int
	High     int
	Medium   int
	Low      int
	Total    int
}

func readIssueCounts(path string) issueCounts {
	rows, err := internalio.ReadJSONL[contracts.IssueEntry](path)
	if err != nil {
		return issueCounts{}
	}
	var counts issueCounts
	seen := map[string]struct{}{}
	for _, row := range rows {
		if _, exists := seen[row.IssueID]; exists {
			continue
		}
		seen[row.IssueID] = struct{}{}
		counts.Total++
		switch row.Severity {
		case contracts.IssueSeverityCritical:
			counts.Critical++
		case contracts.IssueSeverityHigh:
			counts.High++
		case contracts.IssueSeverityMedium:
			counts.Medium++
		case contracts.IssueSeverityLow:
			counts.Low++
		}
	}
	return counts
}

type pairwiseCounts struct {
	Pass1 int
	Pass2 int
	Tie   int
	Total int
}

func readPairwiseCounts(path string) pairwiseCounts {
	rows, err := internalio.ReadJSONL[contracts.PairwiseEntry](path)
	if err != nil {
		return pairwiseCounts{}
	}
	var counts pairwiseCounts
	for _, row := range rows {
		counts.Total++
		switch row.Winner {
		case contracts.PairwiseWinnerA:
			counts.Pass1++
		case contracts.PairwiseWinnerB:
			counts.Pass2++
		case contracts.PairwiseWinnerTie:
			counts.Tie++
		}
	}
	return counts
}

func readJSONFile[T any](path string) (T, bool) {
	var zero T
	value, err := internalio.ReadJSON[T](path)
	if err != nil {
		return zero, false
	}
	return value, true
}

func durationDisplay(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%02d:%02d", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

func truncateDisplay(text string, max int) string {
	text = oneLine(text)
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func oneLine(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	return strings.Join(strings.Fields(text), " ")
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

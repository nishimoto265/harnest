package orchestrator

import (
	"context"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
)

type ProgressEventKind string

const (
	ProgressRunStart   ProgressEventKind = "run_start"
	ProgressRunDone    ProgressEventKind = "run_done"
	ProgressStepStart  ProgressEventKind = "step_start"
	ProgressStepDone   ProgressEventKind = "step_done"
	ProgressStepSkip   ProgressEventKind = "step_skip"
	ProgressAgentStart ProgressEventKind = "agent_start"
	ProgressAgentDone  ProgressEventKind = "agent_done"
)

type ProgressEvent struct {
	Event   ProgressEventKind    `json:"event"`
	RunID   contracts.RunID      `json:"run_id,omitempty"`
	PR      int                  `json:"pr,omitempty"`
	Step    contracts.FailedStep `json:"step,omitempty"`
	Pass    int                  `json:"pass,omitempty"`
	Agent   contracts.AgentID    `json:"agent,omitempty"`
	RunDir  string               `json:"run_dir,omitempty"`
	Message string               `json:"message,omitempty"`
	Error   string               `json:"error,omitempty"`
	At      time.Time            `json:"at"`
}

type ProgressObserver interface {
	OnProgress(context.Context, ProgressEvent)
}

func (o *Orchestrator) SetProgressObserver(observer ProgressObserver) {
	o.progress = observer
}

func (o *Orchestrator) emitProgress(ctx context.Context, event ProgressEvent) {
	if o.progress == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	o.progress.OnProgress(ctx, event)
}

func progressPassForStep(step contracts.FailedStep) int {
	switch step {
	case contracts.FailedStep20, contracts.FailedStep30, contracts.FailedStep40:
		return 1
	case contracts.FailedStep50, contracts.FailedStep60:
		return 2
	default:
		return 0
	}
}

func progressStepEvent(kind ProgressEventKind, run *StepRunContext, step contracts.FailedStep) ProgressEvent {
	return ProgressEvent{
		Event:  kind,
		RunID:  run.IO.RunID,
		PR:     run.PR,
		Step:   step,
		Pass:   progressPassForStep(step),
		RunDir: run.IO.RunDir(),
	}
}

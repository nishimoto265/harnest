package state

import (
	"errors"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

const processedDetailsDir = "processed-details"

var ErrPartialStateLine = errors.New("state: partial processed.jsonl line")

type NextAction string

const (
	NextActionFreshStart          NextAction = "fresh_start"
	NextActionResume              NextAction = "resume"
	NextActionNeedsManualRecovery NextAction = "needs_manual_recovery"
)

type Reader struct {
	path string
}

type Writer struct {
	path   string
	runDir string
}

type ResumeRequest struct {
	PR    int
	RunID contracts.RunID
	Step  contracts.FailedStep
}

type LatestRun struct {
	PR        int
	RunID     contracts.RunID
	Step      contracts.FailedStep
	LastEvent *contracts.StateEntry
	Action    NextAction
}

func NewReader(ctx internalio.RunContext) Reader {
	return Reader{path: ctx.ProcessedPath()}
}

func NewReaderPath(path string) (Reader, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return Reader{}, err
	}
	return Reader{path: path}, nil
}

func NewWriter(ctx internalio.RunContext) Writer {
	return Writer{
		path:   ctx.ProcessedPath(),
		runDir: ctx.RunDir(),
	}
}

func NewWriterPath(path string) (Writer, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return Writer{}, err
	}
	return Writer{path: path}, nil
}

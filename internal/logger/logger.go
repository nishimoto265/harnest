package logger

import (
	"io"
	"log/slog"
	"os"
)

const (
	FieldRunID = "run_id"
	FieldStep  = "step"
	FieldAgent = "agent"
	FieldPass  = "pass"
)

var output io.Writer = os.Stdout

func New(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: level,
	}))
}

func WithRunContext(base *slog.Logger, runID string, step int, agent string, pass int) *slog.Logger {
	return base.With(
		slog.String(FieldRunID, runID),
		slog.Int(FieldStep, step),
		slog.String(FieldAgent, agent),
		slog.Int(FieldPass, pass),
	)
}

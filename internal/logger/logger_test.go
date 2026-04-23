package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_FiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	restore := setOutputForTest(&buf)
	defer restore()

	log := New(slog.LevelWarn)
	log.Info("drop-info")
	log.Warn("keep-warn")

	lines := loggedLines(buf.String())
	require.Len(t, lines, 1)

	record := decodeLogRecord(t, lines[0])
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, "keep-warn", record["msg"])
}

func TestWithRunContext_AddsStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	restore := setOutputForTest(&buf)
	defer restore()

	log := WithRunContext(New(slog.LevelInfo), "2026-04-21-PR42-abcdef0", 20, "a2", 1)
	log.Info("implement")

	lines := loggedLines(buf.String())
	require.Len(t, lines, 1)

	record := decodeLogRecord(t, lines[0])
	assert.Equal(t, "2026-04-21-PR42-abcdef0", record[FieldRunID])
	assert.EqualValues(t, 20, record[FieldStep])
	assert.Equal(t, "a2", record[FieldAgent])
	assert.EqualValues(t, 1, record[FieldPass])
}

func setOutputForTest(w *bytes.Buffer) func() {
	previous := output
	output = w
	return func() {
		output = previous
	}
}

func loggedLines(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func decodeLogRecord(t *testing.T, line string) map[string]any {
	t.Helper()

	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &record))
	return record
}

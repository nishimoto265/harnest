package interruption

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyInterruption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exitCode int
		fixture  string
		want     InterruptionKind
	}{
		{name: "rate limit", exitCode: 429, fixture: "rate_limit.txt", want: InterruptionKindRateLimit},
		{name: "budget", exitCode: 1, fixture: "budget.txt", want: InterruptionKindBudget},
		{name: "context", exitCode: 1, fixture: "context.txt", want: InterruptionKindContext},
		{name: "signal", exitCode: 143, fixture: "signal.txt", want: InterruptionKindSignal},
		{name: "unknown fallback", exitCode: 1, fixture: "unknown.txt", want: InterruptionKindUnknown},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stderrSnippet := readFixture(t, tt.fixture)
			assert.Equal(t, tt.want, ClassifyInterruption(tt.exitCode, stderrSnippet))
		})
	}
}

func TestClassifyReturnsNoneForSuccessfulExit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, InterruptionKindNone, Classify(0, nil, nil))
}

func readFixture(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	path := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "interruption", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

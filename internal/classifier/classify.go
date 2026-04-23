package classifier

import "github.com/nishimoto265/auto-improve/internal/interruption"

type InterruptionKind = interruption.InterruptionKind

const (
	InterruptionKindNone      = interruption.InterruptionKindNone
	InterruptionKindRateLimit = interruption.InterruptionKindRateLimit
	InterruptionKindBudget    = interruption.InterruptionKindBudget
	InterruptionKindContext   = interruption.InterruptionKindContext
	InterruptionKindSignal    = interruption.InterruptionKindSignal
	InterruptionKindUnknown   = interruption.InterruptionKindUnknown
)

func ClassifyInterruption(exitCode int, stderrSnippet string) InterruptionKind {
	return interruption.ClassifyInterruption(exitCode, stderrSnippet)
}

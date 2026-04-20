package interruption

import "strings"

type InterruptionKind string

const (
	InterruptionKindNone      InterruptionKind = "none"
	InterruptionKindRateLimit InterruptionKind = "rate_limit"
	InterruptionKindBudget    InterruptionKind = "budget"
	InterruptionKindContext   InterruptionKind = "context"
	InterruptionKindSignal    InterruptionKind = "signal"
	InterruptionKindUnknown   InterruptionKind = "unknown"
)

func Classify(exitCode int, stdout, stderr []byte) InterruptionKind {
	if exitCode == 0 {
		return InterruptionKindNone
	}
	if kind := ClassifyInterruption(exitCode, string(stderr)); kind != InterruptionKindUnknown {
		return kind
	}
	return ClassifyInterruption(exitCode, string(stdout))
}

func ClassifyInterruption(exitCode int, stderrSnippet string) InterruptionKind {
	snippet := strings.ToLower(stderrSnippet)

	switch {
	case containsAny(snippet, "rate limit", "rate_limit", "too many requests", "quota", "overloaded") || exitCode == 429:
		return InterruptionKindRateLimit
	case containsAny(snippet, "budget exhausted", "budget exceeded", "insufficient credit", "credit balance is too low", "billing hard limit"):
		return InterruptionKindBudget
	case containsAny(snippet, "context length", "context window", "maximum context", "too many tokens", "prompt is too long", "maximum tokens"):
		return InterruptionKindContext
	case exitCode >= 128 && exitCode <= 159 || containsAny(snippet, "signal: terminated", "signal: interrupt", "signal: killed", "terminated by signal", "interrupted by signal"):
		return InterruptionKindSignal
	default:
		return InterruptionKindUnknown
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

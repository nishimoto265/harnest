package processenv

import (
	"os"
	"strings"
)

func Sanitize(extra ...string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	for _, item := range env {
		if blockedEnv(item) {
			continue
		}
		out = append(out, item)
	}
	out = append(out, extra...)
	return out
}

func blockedEnv(item string) bool {
	key, _, ok := strings.Cut(item, "=")
	if !ok {
		return false
	}
	switch {
	case strings.HasPrefix(key, "GIT_"):
		return true
	case strings.HasPrefix(key, "GH_"):
		return key != "GH_TOKEN"
	default:
		return false
	}
}

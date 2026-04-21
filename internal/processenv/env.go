package processenv

import (
	"os"
	"strings"
)

var blockedPrefixes = []string{
	"GIT_DIR=",
	"GIT_WORK_TREE=",
	"GIT_INDEX_FILE=",
	"GIT_COMMON_DIR=",
	"GH_REPO=",
}

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
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

package processenv

import (
	"os"
	"strings"
)

const trustedPATH = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"

func Sanitize(extra ...string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	indexByKey := make(map[string]int, len(env)+len(extra))
	appendAllowed := func(item string, replace bool) {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "PATH" || !allowedEnvKey(key) {
			return
		}
		if idx, exists := indexByKey[key]; exists {
			if replace {
				out[idx] = item
			}
			return
		}
		indexByKey[key] = len(out)
		out = append(out, item)
	}
	for _, item := range env {
		appendAllowed(item, false)
	}
	for _, item := range extra {
		appendAllowed(item, true)
	}
	out = append(out, "PATH="+trustedPATH)
	return out
}

func allowedEnvKey(key string) bool {
	switch {
	case key == "HOME":
		return true
	case key == "USER":
		return true
	case key == "LANG":
		return true
	case key == "LC_ALL":
		return true
	case key == "TZ":
		return true
	case key == "TMPDIR":
		return true
	case strings.HasPrefix(key, "AUTO_IMPROVE_"):
		return true
	case strings.HasPrefix(key, "FAKE_"):
		return true
	case strings.HasPrefix(key, "PROMPT_"):
		return true
	case key == "REAL_GIT":
		return true
	default:
		return false
	}
}

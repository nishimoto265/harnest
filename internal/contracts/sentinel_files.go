package contracts

import "strings"

const (
	needsRecoverySentinelSuffix        = ".json"
	needsRecoverySentinelAbortedSuffix = ".aborted.json"
	needsRecoverySentinelClearedSuffix = ".cleared.json"
)

func NeedsRecoverySentinelFilename(runID RunID) string {
	return string(runID) + needsRecoverySentinelSuffix
}

func NeedsRecoverySentinelAbortedFilename(runID RunID) string {
	return string(runID) + needsRecoverySentinelAbortedSuffix
}

func NeedsRecoverySentinelClearedFilename(runID RunID) string {
	return string(runID) + needsRecoverySentinelClearedSuffix
}

func IsNeedsRecoverySentinelFilename(name string) bool {
	switch {
	case strings.HasSuffix(name, needsRecoverySentinelAbortedSuffix):
		return true
	case strings.HasSuffix(name, needsRecoverySentinelClearedSuffix):
		return false
	default:
		return strings.HasSuffix(name, needsRecoverySentinelSuffix)
	}
}

func SentinelRunIDFromFilename(name string) RunID {
	name = strings.TrimSuffix(name, needsRecoverySentinelClearedSuffix)
	name = strings.TrimSuffix(name, needsRecoverySentinelAbortedSuffix)
	name = strings.TrimSuffix(name, needsRecoverySentinelSuffix)
	return RunID(name)
}

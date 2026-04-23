package scorecore

// RowsMatchVersion reports whether every row in the slice carries the supplied
// rubric/prompt version pair.
func RowsMatchVersion[T any](rows []T, versionOf func(T) (string, string), rubricVersion, promptVersion string) bool {
	for _, row := range rows {
		rowRubricVersion, rowPromptVersion := versionOf(row)
		if rowRubricVersion != rubricVersion || rowPromptVersion != promptVersion {
			return false
		}
	}
	return true
}

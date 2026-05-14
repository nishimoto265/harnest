package step40_classify

import (
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

func substantiveEvidenceText(runIO internalio.RunContext, value string, overflow *contracts.OverflowRef) (string, bool, error) {
	if trimmed, ok := normalizedSubstantiveEvidenceText(value); ok {
		return trimmed, true, nil
	}
	if overflow != nil {
		sidecar, err := internalio.ReadSidecar(runIO, *overflow)
		if err != nil {
			return "", false, err
		}
		if trimmed, ok := normalizedSubstantiveEvidenceText(sidecar); ok {
			return trimmed, true, nil
		}
	}
	return "", false, nil
}

func substantiveScoreConcernText(runIO internalio.RunContext, value string, overflow *contracts.OverflowRef) (string, bool, error) {
	text, ok, err := substantiveEvidenceText(runIO, value, overflow)
	if err != nil || !ok {
		return text, ok, err
	}
	if isNonConcernScoreText(text) {
		return "", false, nil
	}
	return text, true, nil
}

func scoreConcernSentences(value string) []string {
	text := normalizeEvidenceText(value)
	if text == "" {
		return nil
	}
	parts := splitConcernSentences(text)
	concerns := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = trimConcernPrefix(part)
		if part == "" || !isConcernSentence(part) {
			continue
		}
		key := strings.ToLower(part)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		concerns = append(concerns, part)
	}
	if len(concerns) == 0 && isConcernSentence(text) {
		concerns = append(concerns, trimConcernPrefix(text))
	}
	return concerns
}

func splitConcernSentences(value string) []string {
	replacer := strings.NewReplacer(
		". Minor:", ".\nMinor:",
		". Minor issue:", ".\nMinor issue:",
		". Potential issue:", ".\nPotential issue:",
		". Main concern:", ".\nMain concern:",
		". Significant drawback:", ".\nSignificant drawback:",
		". However,", ".\nHowever,",
		". However:", ".\nHowever:",
		". Could improve", ".\nCould improve",
		". Could benefit", ".\nCould benefit",
		". Missing:", ".\nMissing:",
	)
	value = replacer.Replace(value)
	value = strings.ReplaceAll(value, ". ", ".\n")
	raw := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n'
	})
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func trimConcernPrefix(value string) string {
	value = strings.TrimSpace(value)
	prefixes := []string{
		"Minor issue:",
		"Minor:",
		"Potential issue:",
		"Main concern:",
		"Significant drawback:",
		"However,",
		"However:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
	}
	return value
}

func isConcernSentence(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	if lower == "" || isNonConcernScoreText(lower) {
		return false
	}
	if categorizedScoreConcernRuleID(lower) != "" {
		return true
	}
	markers := []string{
		"minor",
		"issue",
		"drawback",
		"could improve",
		"could benefit",
		"could strengthen",
		"could include",
		"would improve",
		"worth documenting",
		"lack",
		"missing",
		"without",
		"cannot",
		"duplicat",
		"hardcoded",
		"maintenance burden",
		"potential",
		"violates",
		"deviation",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isNonActionableScoreConcern(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	markers := []string{
		"cannot be confirmed",
		"full fidelity cannot",
		"unknown",
		"lack of visibility",
		"patch lacks commit message",
		"without explicit requirements visible",
		"no logic errors detected",
		"separation of concerns",
		"minor deduction",
		"tests verify behavior",
		"workaround but necessary",
		"likely necessary",
		"necessary due to",
		"despite valid reasoning",
		"commit message explaining",
		"reduce duplication",
		"not semantically incorrect",
		"clear code structure with helpful comments",
		"helpful comments explaining",
		"justified by",
		"is justified",
		"jsdoc",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func normalizeEvidenceText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizedSubstantiveEvidenceText(value string) (string, bool) {
	trimmed := normalizeEvidenceText(value)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "stub "):
		return "", false
	case strings.Contains(lower, "placeholder"):
		return "", false
	case strings.HasPrefix(lower, "todo"):
		return "", false
	case strings.HasPrefix(lower, "phase 0 deterministic classify"):
		return "", false
	default:
		return trimmed, true
	}
}

func isNonConcernScoreText(value string) bool {
	lower := strings.ToLower(normalizeEvidenceText(value))
	switch lower {
	case "none", "none.", "n/a", "na", "not applicable", "no issue", "no issues", "no concern", "no concerns", "no material concern", "no material concerns", "no material scoring concern", "no material scoring concerns":
		return true
	}
	prefixes := []string{
		"no material scoring concern.",
		"no material scoring concerns.",
		"no material concern.",
		"no material concerns.",
		"no issue.",
		"no issues.",
		"no concern.",
		"no concerns.",
		"looks good",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

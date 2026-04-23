package prompt

import "path/filepath"

type TemplateName string

const (
	TemplateStep20Implement TemplateName = "step20-implement.tmpl"
	TemplateStep30Score     TemplateName = "step30-score.tmpl"
	TemplateStep40Classify  TemplateName = "step40-classify.tmpl"
	TemplateStep50Implement TemplateName = "step50-implement-pass2.tmpl"
	TemplateStep60Score     TemplateName = "step60-score-pass2.tmpl"
	TemplateStep70Decide    TemplateName = "step70-decide.tmpl"
)

func All() []TemplateName {
	return []TemplateName{
		TemplateStep20Implement,
		TemplateStep30Score,
		TemplateStep40Classify,
		TemplateStep50Implement,
		TemplateStep60Score,
		TemplateStep70Decide,
	}
}

func (name TemplateName) RelativePath() string {
	return filepath.Join("prompts", string(name))
}

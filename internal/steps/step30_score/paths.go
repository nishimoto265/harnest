package step30_score

import (
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

type stepPathsResult struct {
	MarkerPath      string
	LockPath        string
	ScoreFinal      string
	ComplianceFinal string
	IssueFinal      string
	ScoreRaw        string
	ComplianceRaw   string
	MarkerPaths     scorecore.Step30MarkerPaths
}

func stepPaths(runCtx internalio.RunContext) (stepPathsResult, error) {
	marker, err := runCtx.ResolveRunRelative("30/done.marker")
	if err != nil {
		return stepPathsResult{}, err
	}
	lockPath, err := runCtx.ResolveRunRelative("30/.step30.lock")
	if err != nil {
		return stepPathsResult{}, err
	}
	scoreFinal, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	complianceFinal, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	issueFinal, err := runCtx.ResolveRunRelative("30/issues-A.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	scoreRaw, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	complianceRaw, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	return stepPathsResult{
		MarkerPath:      marker,
		LockPath:        lockPath,
		ScoreFinal:      scoreFinal,
		ComplianceFinal: complianceFinal,
		IssueFinal:      issueFinal,
		ScoreRaw:        scoreRaw,
		ComplianceRaw:   complianceRaw,
		MarkerPaths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinal,
			ComplianceFinal: complianceFinal,
			ScoreRaw:        scoreRaw,
			ComplianceRaw:   complianceRaw,
		},
	}, nil
}

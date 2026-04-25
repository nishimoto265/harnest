package judges

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
)

const pairwisePromptVersion = "cli-pairwise-v1"

type PairwiseMode string

const (
	PairwiseModeSingle PairwiseMode = "single"
	PairwiseModeBasic  PairwiseMode = "basic"
	PairwiseModeStrict PairwiseMode = "strict"
)

type PairwiseJudge interface {
	ComparePairwise(ctx context.Context, input PairwiseInput) (PairwiseComparison, error)
}

type PairwiseDecisionJudge interface {
	DecidePairwise(ctx context.Context, input PairwiseDecisionInput) (PairwiseDecision, error)
}

type PairwisePromptVersionedJudge interface {
	PairwiseJudgePromptVersion() string
}

type PairwiseCandidate struct {
	Label      string
	OutputPath string
	Scores     []PairwiseScore
}

type PairwiseScore struct {
	Dimension contracts.Dimension
	Score     int
	Reason    string
}

type PairwiseInput struct {
	RunID      contracts.RunID
	Agent      contracts.AgentID
	Order      string
	TaskPrompt string
	RubricPath string
	A          PairwiseCandidate
	B          PairwiseCandidate
}

type PairwiseDimensionVote struct {
	Dimension contracts.Dimension
	Winner    contracts.PairwiseWinner
	Reason    string
}

type PairwiseComparison struct {
	Agent          contracts.AgentID
	Order          string
	Winner         contracts.PairwiseWinner
	Margin         contracts.PairwiseMargin
	Justification  string
	DimensionVotes []PairwiseDimensionVote
	FatalIssues    []string
}

type PairwisePair struct {
	Agent contracts.AgentID
	A     PairwiseCandidate
	B     PairwiseCandidate
}

type PairwiseDecisionInput struct {
	RunID       contracts.RunID
	Mode        PairwiseMode
	TaskPrompt  string
	RubricPath  string
	Pairs       []PairwisePair
	Comparisons []PairwiseComparison
}

type PairwiseDecisionAction string

const (
	PairwiseDecisionAdopt        PairwiseDecisionAction = "adopt"
	PairwiseDecisionReject       PairwiseDecisionAction = "reject"
	PairwiseDecisionInconclusive PairwiseDecisionAction = "inconclusive"
)

type PairwiseAgentDecision struct {
	Agent         contracts.AgentID
	Winner        contracts.PairwiseWinner
	Margin        contracts.PairwiseMargin
	Justification string
}

type PairwiseDecision struct {
	Action         PairwiseDecisionAction
	Justification  string
	AgentDecisions []PairwiseAgentDecision
}

func NewPairwiseJudgeFromConfig(cfg *config.Config) (PairwiseJudge, error) {
	profile, err := pairwiseJudgeProfile(cfg)
	if err != nil {
		return nil, err
	}
	return newPairwiseJudgeFromProfile(profile, "pairwise")
}

func NewPairwiseDecisionJudgeFromConfig(cfg *config.Config) (PairwiseDecisionJudge, error) {
	profile, err := pairwiseJudgeProfile(cfg)
	if err != nil {
		return nil, err
	}
	return newPairwiseDecisionJudgeFromProfile(profile, "pairwise-decision")
}

func NewScoreDerivedPairwiseJudge() PairwiseJudge {
	return scoreDerivedPairwiseJudge{}
}

func NewScoreDerivedPairwiseDecisionJudge() PairwiseDecisionJudge {
	return scoreDerivedPairwiseJudge{}
}

func PairwisePanelPromptVersion(base string, mode PairwiseMode, pairwise PairwiseJudge, decision PairwiseDecisionJudge) string {
	payload := struct {
		Base     string       `json:"base"`
		Mode     PairwiseMode `json:"mode"`
		Pairwise string       `json:"pairwise,omitempty"`
		Decision string       `json:"decision,omitempty"`
	}{
		Base: strings.TrimSpace(base),
		Mode: mode,
	}
	if versioned, ok := pairwise.(PairwisePromptVersionedJudge); ok {
		payload.Pairwise = versioned.PairwiseJudgePromptVersion()
	}
	if versioned, ok := decision.(PairwisePromptVersionedJudge); ok {
		payload.Decision = versioned.PairwiseJudgePromptVersion()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return pairwisePromptVersion
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s", pairwisePromptVersion, hex.EncodeToString(sum[:])[:12])
}

func pairwiseJudgeProfile(cfg *config.Config) (agents.Profile, error) {
	if cfg == nil {
		return agents.Profile{}, fmt.Errorf("judges: config is required")
	}
	profile, err := cfg.AgentProfile(agents.RoleJudgePrimary)
	if err != nil {
		return agents.Profile{}, err
	}
	if agents.IsGatedTestStubProvider(profile.Provider) && !agents.AllowTestStubProviders() {
		return agents.Profile{}, fmt.Errorf("judges: provider %q for pairwise judge requires %s=1", profile.Provider, agents.AllowTestStubProvidersEnv)
	}
	return profile, nil
}

func newPairwiseJudgeFromProfile(profile agents.Profile, purpose string) (PairwiseJudge, error) {
	switch profile.Provider {
	case agents.ProviderStub, agents.ProviderStubViolation, agents.ProviderStubAdopt:
		return NewScoreDerivedPairwiseJudge(), nil
	case agents.ProviderClaude, agents.ProviderCodex:
		return cliPairwiseJudge{profile: profile, purpose: purpose, timeout: defaultCLIJudgeTimeout, now: time.Now}, nil
	default:
		return nil, fmt.Errorf("judges: provider %q for pairwise judge is not implemented yet", profile.Provider)
	}
}

func newPairwiseDecisionJudgeFromProfile(profile agents.Profile, purpose string) (PairwiseDecisionJudge, error) {
	switch profile.Provider {
	case agents.ProviderStub, agents.ProviderStubViolation, agents.ProviderStubAdopt:
		return NewScoreDerivedPairwiseDecisionJudge(), nil
	case agents.ProviderClaude, agents.ProviderCodex:
		return cliPairwiseJudge{profile: profile, purpose: purpose, timeout: defaultCLIJudgeTimeout, now: time.Now}, nil
	default:
		return nil, fmt.Errorf("judges: provider %q for pairwise decision judge is not implemented yet", profile.Provider)
	}
}

type scoreDerivedPairwiseJudge struct{}

func (scoreDerivedPairwiseJudge) ComparePairwise(_ context.Context, input PairwiseInput) (PairwiseComparison, error) {
	comparison := PairwiseComparison{
		Agent:         input.Agent,
		Order:         input.Order,
		Winner:        compareScoreAverages(input.A.Scores, input.B.Scores),
		Margin:        scoreDeltaMargin(input.A.Scores, input.B.Scores),
		Justification: scoreDerivedJustification(input.A.Scores, input.B.Scores),
	}
	for _, dimension := range allDimensions {
		aScore, aOK := scoreByDimension(input.A.Scores, dimension)
		bScore, bOK := scoreByDimension(input.B.Scores, dimension)
		if !aOK || !bOK {
			continue
		}
		winner := contracts.PairwiseWinnerTie
		switch {
		case bScore > aScore:
			winner = contracts.PairwiseWinnerB
		case aScore > bScore:
			winner = contracts.PairwiseWinnerA
		}
		comparison.DimensionVotes = append(comparison.DimensionVotes, PairwiseDimensionVote{
			Dimension: dimension,
			Winner:    winner,
			Reason:    fmt.Sprintf("A=%d B=%d", aScore, bScore),
		})
	}
	return comparison, nil
}

func (scoreDerivedPairwiseJudge) DecidePairwise(_ context.Context, input PairwiseDecisionInput) (PairwiseDecision, error) {
	agentDecisions := make([]PairwiseAgentDecision, 0, len(input.Pairs))
	comparisonsByAgent := map[contracts.AgentID][]PairwiseComparison{}
	for _, comparison := range input.Comparisons {
		comparisonsByAgent[comparison.Agent] = append(comparisonsByAgent[comparison.Agent], comparison)
	}
	totalA, totalB := 0, 0
	for _, pair := range input.Pairs {
		decision := decisionForPair(pair, comparisonsByAgent[pair.Agent])
		switch decision.Winner {
		case contracts.PairwiseWinnerA:
			totalA++
		case contracts.PairwiseWinnerB:
			totalB++
		}
		agentDecisions = append(agentDecisions, decision)
	}
	action := PairwiseDecisionInconclusive
	switch {
	case totalB > totalA:
		action = PairwiseDecisionAdopt
	case totalA > totalB:
		action = PairwiseDecisionReject
	}
	return PairwiseDecision{
		Action:         action,
		Justification:  fmt.Sprintf("score-derived decision: B_agents=%d A_agents=%d mode=%s", totalB, totalA, input.Mode),
		AgentDecisions: agentDecisions,
	}, nil
}

func decisionForPair(pair PairwisePair, comparisons []PairwiseComparison) PairwiseAgentDecision {
	aVotes, bVotes := 0, 0
	var reasons []string
	for _, comparison := range comparisons {
		switch comparison.Winner {
		case contracts.PairwiseWinnerA:
			aVotes++
		case contracts.PairwiseWinnerB:
			bVotes++
		}
		if comparison.Justification != "" {
			reasons = append(reasons, comparison.Justification)
		}
		for _, vote := range comparison.DimensionVotes {
			switch vote.Winner {
			case contracts.PairwiseWinnerA:
				aVotes++
			case contracts.PairwiseWinnerB:
				bVotes++
			}
		}
	}
	if len(comparisons) == 0 {
		winner := compareScoreAverages(pair.A.Scores, pair.B.Scores)
		return PairwiseAgentDecision{
			Agent:         pair.Agent,
			Winner:        winner,
			Margin:        scoreDeltaMargin(pair.A.Scores, pair.B.Scores),
			Justification: scoreDerivedJustification(pair.A.Scores, pair.B.Scores),
		}
	}
	winner := contracts.PairwiseWinnerTie
	switch {
	case bVotes > aVotes:
		winner = contracts.PairwiseWinnerB
	case aVotes > bVotes:
		winner = contracts.PairwiseWinnerA
	}
	return PairwiseAgentDecision{
		Agent:         pair.Agent,
		Winner:        winner,
		Margin:        marginFromVoteDelta(aVotes, bVotes),
		Justification: strings.Join(reasons, " | "),
	}
}

func compareScoreAverages(aScores, bScores []PairwiseScore) contracts.PairwiseWinner {
	a := averagePairwiseScoresTenths(aScores)
	b := averagePairwiseScoresTenths(bScores)
	switch {
	case b > a:
		return contracts.PairwiseWinnerB
	case a > b:
		return contracts.PairwiseWinnerA
	default:
		return contracts.PairwiseWinnerTie
	}
}

func scoreDeltaMargin(aScores, bScores []PairwiseScore) contracts.PairwiseMargin {
	delta := averagePairwiseScoresTenths(bScores) - averagePairwiseScoresTenths(aScores)
	if delta < 0 {
		delta = -delta
	}
	switch {
	case delta > 100:
		return contracts.PairwiseMarginDecisive
	case delta > 30:
		return contracts.PairwiseMarginClear
	default:
		return contracts.PairwiseMarginSlight
	}
}

func marginFromVoteDelta(aVotes, bVotes int) contracts.PairwiseMargin {
	delta := bVotes - aVotes
	if delta < 0 {
		delta = -delta
	}
	switch {
	case delta >= 6:
		return contracts.PairwiseMarginDecisive
	case delta >= 3:
		return contracts.PairwiseMarginClear
	default:
		return contracts.PairwiseMarginSlight
	}
}

func scoreDerivedJustification(aScores, bScores []PairwiseScore) string {
	return fmt.Sprintf("score-derived comparison: A_avg_tenths=%d B_avg_tenths=%d", averagePairwiseScoresTenths(aScores), averagePairwiseScoresTenths(bScores))
}

func averagePairwiseScoresTenths(scores []PairwiseScore) int {
	if len(scores) == 0 {
		return 0
	}
	total := 0
	for _, score := range scores {
		total += score.Score
	}
	return total * 10 / len(scores)
}

func scoreByDimension(scores []PairwiseScore, dimension contracts.Dimension) (int, bool) {
	for _, score := range scores {
		if score.Dimension == dimension {
			return score.Score, true
		}
	}
	return 0, false
}

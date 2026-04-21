package scorecore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// Step30MarkerPaths describes the four jsonl files whose canonical-reduced
// contents are hashed into the step30 done.marker.
type Step30MarkerPaths struct {
	ScoreFinal      string
	ComplianceFinal string
	ScoreRaw        string
	ComplianceRaw   string
}

// Step30MarkerInputs bundles the per-agent + rubric metadata required for the
// marker. Agents / Dimensions are sorted+deduped internally; callers can pass
// whatever ordering their scorable-agent iteration yields.
type Step30MarkerInputs struct {
	Agents     []contracts.AgentID
	Dimensions []contracts.Dimension
	Paths      Step30MarkerPaths
	ResolvedAt time.Time
}

// BuildStep30DoneMarker reduces the 4 jsonl files with CollapseByKey and
// returns a ready-to-write Step30DoneMarker with cardinality and content
// hashes populated.
func BuildStep30DoneMarker(in Step30MarkerInputs) (contracts.Step30DoneMarker, error) {
	if len(in.Agents) == 0 {
		return contracts.Step30DoneMarker{}, errors.New("scorecore: step30 marker: at least one completed agent required")
	}
	if len(in.Dimensions) == 0 {
		in.Dimensions = allFiveDimensions()
	}

	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](in.Paths.ScoreFinal)
	if err != nil {
		return contracts.Step30DoneMarker{}, fmt.Errorf("scorecore: read %s: %w", in.Paths.ScoreFinal, err)
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](in.Paths.ComplianceFinal)
	if err != nil {
		return contracts.Step30DoneMarker{}, fmt.Errorf("scorecore: read %s: %w", in.Paths.ComplianceFinal, err)
	}
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](in.Paths.ScoreRaw)
	if err != nil {
		return contracts.Step30DoneMarker{}, fmt.Errorf("scorecore: read %s: %w", in.Paths.ScoreRaw, err)
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](in.Paths.ComplianceRaw)
	if err != nil {
		return contracts.Step30DoneMarker{}, fmt.Errorf("scorecore: read %s: %w", in.Paths.ComplianceRaw, err)
	}

	scoreFinalCollapsed := CollapseFinalScores(scoreFinal)
	complianceFinalCollapsed := CollapseFinalCompliance(complianceFinal)
	scoreRawCollapsed := CollapseRawScores(scoreRaw)
	complianceRawCollapsed := CollapseRawCompliance(complianceRaw)

	scoresFinalHash, err := hashFinalScores(scoreFinalCollapsed)
	if err != nil {
		return contracts.Step30DoneMarker{}, err
	}
	complianceFinalHash, err := hashFinalCompliance(complianceFinalCollapsed)
	if err != nil {
		return contracts.Step30DoneMarker{}, err
	}
	scoresRawHash, err := hashRawScores(scoreRawCollapsed)
	if err != nil {
		return contracts.Step30DoneMarker{}, err
	}
	complianceRawHash, err := hashRawCompliance(complianceRawCollapsed)
	if err != nil {
		return contracts.Step30DoneMarker{}, err
	}

	marker := contracts.Step30DoneMarker{
		CompletedAgents: sortedUniqueAgents(in.Agents),
		Dimensions:      sortedUniqueDimensions(in.Dimensions),
		ExpectedCounts: contracts.Step30ExpectedCounts{
			Scores:     int64(len(scoreFinalCollapsed)),
			Compliance: int64(len(complianceFinalCollapsed)),
		},
		ContentHashes: contracts.Step30DoneContentHashes{
			ScoresFinal:     scoresFinalHash,
			ComplianceFinal: complianceFinalHash,
		},
		RawHashes: contracts.StepDoneRawHashes{
			ScoresRaw:     scoresRawHash,
			ComplianceRaw: complianceRawHash,
		},
		ResolvedAt: in.ResolvedAt,
	}
	if err := marker.Validate(); err != nil {
		return contracts.Step30DoneMarker{}, err
	}
	return marker, nil
}

// WriteStep30DoneMarker persists the marker to <run>/30/done.marker atomically.
func WriteStep30DoneMarker(runCtx internalio.RunContext, marker contracts.Step30DoneMarker) error {
	path, err := runCtx.ResolveRunRelative("30/done.marker")
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, marker)
}

// VerifyStep30DoneMarker recomputes hashes from the four jsonl files and
// returns true only if the stored marker's hashes match.
func VerifyStep30DoneMarker(runCtx internalio.RunContext, paths Step30MarkerPaths) (bool, error) {
	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(markerPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	stored, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	if err != nil {
		// Malformed / legacy marker → treat as invalid so Run() can replace it,
		// matching "不一致なら marker ファイル削除 + 再採点" in io-contracts.md.
		return false, nil
	}
	rebuilt, err := BuildStep30DoneMarker(Step30MarkerInputs{
		Agents:     stored.CompletedAgents,
		Dimensions: stored.Dimensions,
		Paths:      paths,
		ResolvedAt: stored.ResolvedAt,
	})
	if err != nil {
		return false, err
	}
	if rebuilt.ExpectedCounts != stored.ExpectedCounts {
		return false, nil
	}
	if rebuilt.ContentHashes != stored.ContentHashes {
		return false, nil
	}
	if rebuilt.RawHashes != stored.RawHashes {
		return false, nil
	}
	return true, nil
}

func keepFreshArbiterScores(raws []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	latest := make(map[[3]string]string, len(raws))
	for _, row := range raws {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			continue
		}
		sum, err := rawScoreSha256(row)
		if err != nil {
			continue
		}
		latest[[3]string{string(row.Agent), string(row.JudgeRole), string(row.Dimension)}] = sum
	}
	out := make([]contracts.RawScoreEntry, 0, len(raws))
	for _, row := range raws {
		if row.JudgeRole != contracts.JudgeRoleArbiter {
			out = append(out, row)
			continue
		}
		if row.PrimaryRef == nil || row.SecondaryRef == nil {
			continue
		}
		primaryKey := [3]string{string(row.Agent), string(contracts.JudgeRolePrimary), string(row.Dimension)}
		secondaryKey := [3]string{string(row.Agent), string(contracts.JudgeRoleSecondary), string(row.Dimension)}
		if latest[primaryKey] != row.PrimaryRef.Sha256 {
			continue
		}
		if latest[secondaryKey] != row.SecondaryRef.Sha256 {
			continue
		}
		out = append(out, row)
	}
	return out
}

func keepFreshArbiterCompliance(raws []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	latest := make(map[[3]string]string, len(raws))
	for _, row := range raws {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			continue
		}
		sum, err := rawComplianceSha256(row)
		if err != nil {
			continue
		}
		latest[[3]string{string(row.Agent), string(row.JudgeRole), row.RuleID}] = sum
	}
	out := make([]contracts.RawComplianceEntry, 0, len(raws))
	for _, row := range raws {
		if row.JudgeRole != contracts.JudgeRoleArbiter {
			out = append(out, row)
			continue
		}
		if row.PrimaryRef == nil || row.SecondaryRef == nil {
			continue
		}
		primaryKey := [3]string{string(row.Agent), string(contracts.JudgeRolePrimary), row.RuleID}
		secondaryKey := [3]string{string(row.Agent), string(contracts.JudgeRoleSecondary), row.RuleID}
		if latest[primaryKey] != row.PrimaryRef.Sha256 {
			continue
		}
		if latest[secondaryKey] != row.SecondaryRef.Sha256 {
			continue
		}
		out = append(out, row)
	}
	return out
}

func hashFinalScores(rows []contracts.ScoreEntry) (string, error) {
	sorted := append([]contracts.ScoreEntry(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Agent != sorted[j].Agent {
			return sorted[i].Agent < sorted[j].Agent
		}
		return sorted[i].Dimension < sorted[j].Dimension
	})
	return hashCanonicalJoined(sorted)
}

func hashFinalCompliance(rows []contracts.ComplianceEntry) (string, error) {
	sorted := append([]contracts.ComplianceEntry(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Agent != sorted[j].Agent {
			return sorted[i].Agent < sorted[j].Agent
		}
		return sorted[i].RuleID < sorted[j].RuleID
	})
	return hashCanonicalJoined(sorted)
}

func hashRawScores(rows []contracts.RawScoreEntry) (string, error) {
	sorted := append([]contracts.RawScoreEntry(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Agent != sorted[j].Agent {
			return sorted[i].Agent < sorted[j].Agent
		}
		if sorted[i].JudgeRole != sorted[j].JudgeRole {
			return sorted[i].JudgeRole < sorted[j].JudgeRole
		}
		return sorted[i].Dimension < sorted[j].Dimension
	})
	return hashCanonicalJoined(sorted)
}

func hashRawCompliance(rows []contracts.RawComplianceEntry) (string, error) {
	sorted := append([]contracts.RawComplianceEntry(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Agent != sorted[j].Agent {
			return sorted[i].Agent < sorted[j].Agent
		}
		if sorted[i].JudgeRole != sorted[j].JudgeRole {
			return sorted[i].JudgeRole < sorted[j].JudgeRole
		}
		return sorted[i].RuleID < sorted[j].RuleID
	})
	return hashCanonicalJoined(sorted)
}

// hashCanonicalJoined canonical-marshals each element and joins them with a
// 0x00 byte, then returns the hex sha256 of the result. Zero-length input
// produces the sha256 of the empty string.
func hashCanonicalJoined[T any](rows []T) (string, error) {
	h := sha256.New()
	for i, row := range rows {
		data, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return "", err
		}
		if i > 0 {
			h.Write([]byte{0x00})
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sortedUniqueAgents(in []contracts.AgentID) []contracts.AgentID {
	seen := make(map[contracts.AgentID]struct{}, len(in))
	out := make([]contracts.AgentID, 0, len(in))
	for _, a := range in {
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUniqueDimensions(in []contracts.Dimension) []contracts.Dimension {
	seen := make(map[contracts.Dimension]struct{}, len(in))
	out := make([]contracts.Dimension, 0, len(in))
	for _, d := range in {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func allFiveDimensions() []contracts.Dimension {
	return []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
}

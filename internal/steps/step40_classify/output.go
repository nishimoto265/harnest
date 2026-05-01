package step40_classify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
)

func writeCandidateBodies(runIO internalio.RunContext, candidates []builtCandidate) error {
	for _, item := range candidates {
		path, err := runIO.ResolveRunRelative(item.Candidate.ProposedBodyPath)
		if err != nil {
			return err
		}
		if sha256Hex([]byte(item.Body)) != item.Candidate.ProposedBodySha256 {
			return fmt.Errorf("step40_classify: candidate body sha mismatch: candidate_id=%s", item.Candidate.CandidateID)
		}
		if err := internalio.WriteAtomic(path, []byte(item.Body)); err != nil {
			return err
		}
	}
	return nil
}

func writeExperimentChecklist(runIO internalio.RunContext, candidates []builtCandidate) error {
	items := make([]lessons.Lesson, 0, len(candidates))
	for _, item := range candidates {
		if item.Candidate.Kind == contracts.CandidateKindDuplicate {
			continue
		}
		items = append(items, item.Lesson)
	}
	path, err := runIO.ResolveRunRelative(experimentChecklistPath)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte(lessons.RenderChecklist(items)))
}

func writeClassificationJSONL(runIO internalio.RunContext, classifications []contracts.ClassificationEntry) error {
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		return err
	}

	var buffer bytes.Buffer
	for _, entry := range classifications {
		if _, err := contracts.MarshalStrict(entry); err != nil {
			return err
		}
		payload, err := contracts.CanonicalMarshal(entry)
		if err != nil {
			return err
		}
		if len(payload)+1 > internalio.JSONLMaxLineBytes {
			return internalio.ErrEntryTooLarge
		}
		if _, err := buffer.Write(payload); err != nil {
			return err
		}
		if err := buffer.WriteByte('\n'); err != nil {
			return err
		}
	}
	return internalio.WriteAtomic(path, buffer.Bytes())
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

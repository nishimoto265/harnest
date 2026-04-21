package agentrunner

import (
	"fmt"
	"path/filepath"
	"time"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const (
	RescueBundleModeNone     = "none"
	RescueBundleModeRange    = "range"
	RescueBundleModeFullHead = "full_head"
)

type RescueArtifactDigest struct {
	Path   string `json:"path" validate:"required"`
	SHA256 string `json:"sha256" validate:"required,len=64,hexadecimal"`
}

type RescueStateFile struct {
	ExpectedBaseSHA string                 `json:"expected_base_sha" validate:"required,sha1_hex"`
	RescuedHeadSHA  string                 `json:"rescued_head_sha" validate:"required,sha1_hex"`
	RetryCount      int                    `json:"retry_count" validate:"gte=1"`
	CommitCount     int                    `json:"commit_count" validate:"gte=0"`
	BundleMode      string                 `json:"bundle_mode" validate:"required,oneof=none range full_head"`
	CreatedAt       time.Time              `json:"created_at" validate:"required"`
	Artifacts       []RescueArtifactDigest `json:"artifacts" validate:"required,dive"`
}

func WriteRescueState(path string, state RescueStateFile) error {
	return internalio.WriteJSONAtomic(path, state)
}

func ReadRescueState(path string) (RescueStateFile, error) {
	return internalio.ReadJSON[RescueStateFile](path)
}

func VerifyRescueState(rescueDir string, fileDigest func(string) (string, error), errPrefix string) error {
	state, err := ReadRescueState(filepath.Join(rescueDir, "state.json"))
	if err != nil {
		return err
	}
	for _, artifact := range state.Artifacts {
		path := filepath.Join(rescueDir, filepath.FromSlash(artifact.Path))
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		if digest != artifact.SHA256 {
			return fmt.Errorf("%s: rescue artifact digest mismatch: path=%s", errPrefix, artifact.Path)
		}
	}
	return nil
}

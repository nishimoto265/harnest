package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ManifestKind is the discriminator for the Manifest tagged union.
type ManifestKind string

const (
	ManifestKindSuccess ManifestKind = "success"
	ManifestKindError   ManifestKind = "error"
	ManifestKindTimeout ManifestKind = "timeout"
)

// Manifest is the step 20/50 per-agent completion marker written atomically to
// `<run>/{20-pass1|50-pass2}/<agent>/manifest.json`.
//
// Tagged union over `kind` (= exit_status in io-contracts.md §step20/50):
//   - success → ManifestSuccess  (scorable)
//   - error   → ManifestError
//   - timeout → ManifestTimeout
//
// 採点時は success のみを読み込むため、`internal/io.LoadScorableManifest` が
// success 以外を `ErrNotScorable` で reject する契約 (io-contracts.md §2).
type Manifest struct {
	Kind  ManifestKind    `json:"kind"`
	Value ManifestVariant `json:"-"`
}

// ManifestVariant is implemented by the three Manifest variant structs.
type ManifestVariant interface {
	manifestVariant()
}

var (
	ErrManifestVariantTypeMismatch        = errors.New("contracts: manifest: kind does not match variant type")
	ErrManifestVariantKindMismatch        = errors.New("contracts: manifest: kind does not match inner kind field")
	ErrManifestArtifactPathPrefixMismatch = errors.New("contracts: manifest: artifact path prefix must match pass and agent")
	ErrManifestErrorMissingExitCode       = errors.New("contracts: manifest: error.exit_code field is required")
)

// ManifestSuccess: agent が実装 + commit + checklist 記入まで完走した状態.
type ManifestSuccess struct {
	Kind          ManifestKind `json:"kind" validate:"required,eq=success"`
	SchemaVersion string       `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int          `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID      `json:"agent" validate:"required,agent_id_fmt"`

	// BranchName: agent が commit した branch 名 (worktree の HEAD 先).
	BranchName string `json:"branch_name" validate:"required"`
	// HeadSHA: commit 後の HEAD SHA.
	HeadSHA string `json:"head_sha" validate:"required,sha1_hex"`
	// BaseSHA: agent が実装を始めた時点の base (= worktree.base_sha).
	BaseSHA string `json:"base_sha" validate:"required,sha1_hex"`

	// DiffPath / SessionPath / ChecklistPath: 相対 path (run 基準) で記録.
	// 下流 scorer は RunContext 経由で絶対 path に解決する.
	DiffPath      string `json:"diff_path" validate:"required"`
	SessionPath   string `json:"session_path" validate:"required"`
	ChecklistPath string `json:"checklist_path" validate:"required"`

	// PromptVersion / RubricVersion: 下流 score 生成時に記録済みの prompt/rubric
	// ではなく、**agent 実装時に適用された best 設定の識別子**.
	PromptVersion string `json:"prompt_version" validate:"required"`

	// StartedAt / FinishedAt: agent wrapper の壁時計時刻.
	StartedAt  time.Time `json:"started_at" validate:"required"`
	FinishedAt time.Time `json:"finished_at" validate:"required"`
}

func (ManifestSuccess) manifestVariant() {}

func (m ManifestSuccess) Validate() error {
	if err := validateStruct(m); err != nil {
		return err
	}
	prefix := manifestArtifactPrefix(m.Pass, m.Agent)
	for field, path := range map[string]string{
		"diff_path":      m.DiffPath,
		"session_path":   m.SessionPath,
		"checklist_path": m.ChecklistPath,
	} {
		if err := EnsureRelativePathUnderPrefix(path, prefix); err != nil {
			if errors.Is(err, ErrPathRelativeBadPrefix) {
				return fmt.Errorf("%w: field=%s path=%q required_prefix=%q", ErrManifestArtifactPathPrefixMismatch, field, path, prefix)
			}
			return fmt.Errorf("contracts: manifest: %s: %w", field, err)
		}
	}
	return nil
}

// ManifestError: agent wrapper が非 timeout で error exit した記録.
// Non-success manifests are emitted before any commit or artifact hand-off, so
// commit metadata / diff/session/checklist artifact paths are intentionally
// absent by contract.
type ManifestError struct {
	Kind          ManifestKind `json:"kind" validate:"required,eq=error"`
	SchemaVersion string       `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int          `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID      `json:"agent" validate:"required,agent_id_fmt"`

	// ExitCode: claude CLI が返した exit code.
	ExitCode int `json:"exit_code" validate:"gte=0"`
	// Reason: classifier が判定した kind を短い string で記録
	// (rate_limit / budget / context / signal / unknown).
	Reason string `json:"reason" validate:"required,oneof=rate_limit budget context signal unknown"`
	// Detail: 300 字 cap. 超過は sidecar へ.
	Detail string `json:"detail,omitempty" validate:"omitempty,max=300"`

	StartedAt  time.Time `json:"started_at" validate:"required"`
	FinishedAt time.Time `json:"finished_at" validate:"required"`
}

func (ManifestError) manifestVariant() {}

func (m *ManifestError) UnmarshalJSON(data []byte) error {
	type alias ManifestError
	var a alias
	if err := decodeStrictWithRequiredFields(data, &a, map[string]error{
		"exit_code": ErrManifestErrorMissingExitCode,
	}); err != nil {
		return err
	}
	*m = ManifestError(a)
	return nil
}

func (m ManifestError) Validate() error {
	return validateStruct(m)
}

// ManifestTimeout: agent が wall-clock timeout に到達した記録.
// Non-success manifests are emitted before any commit or artifact hand-off, so
// commit metadata / diff/session/checklist artifact paths are intentionally
// absent by contract.
type ManifestTimeout struct {
	Kind          ManifestKind `json:"kind" validate:"required,eq=timeout"`
	SchemaVersion string       `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int          `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID      `json:"agent" validate:"required,agent_id_fmt"`

	// TimeoutSeconds: configured timeout value in effect.
	TimeoutSeconds int `json:"timeout_seconds" validate:"required,gt=0"`

	StartedAt  time.Time `json:"started_at" validate:"required"`
	FinishedAt time.Time `json:"finished_at" validate:"required"`
}

func (ManifestTimeout) manifestVariant() {}

func (m ManifestTimeout) Validate() error {
	return validateStruct(m)
}

// UnmarshalJSON implements strict tagged-union decoding for Manifest.
// io-contracts.md §4 / Go 実装計画.md §0-bootstrap-1 に従う:
//   - envelope で kind を peek
//   - variant ごとに DisallowUnknownFields decoder で decode
//   - trailing token 禁止
//   - validator.Struct(variant)
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var env struct {
		Kind ManifestKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Kind {
	case ManifestKindSuccess:
		var v ManifestSuccess
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		m.Kind = env.Kind
		m.Value = v
	case ManifestKindError:
		var v ManifestError
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		m.Kind = env.Kind
		m.Value = v
	case ManifestKindTimeout:
		var v ManifestTimeout
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		m.Kind = env.Kind
		m.Value = v
	default:
		return ErrUnknownManifestKind
	}
	return nil
}

// MarshalJSON is a pass-through to the inner variant so that round-tripping
// through JSON is symmetric with UnmarshalJSON.
func (m Manifest) MarshalJSON() ([]byte, error) {
	if m.Value == nil {
		return nil, ErrUnknownManifestKind
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m.Value)
}

// Validate runs tag-based validation on the embedded variant so that
// EncodeStrict / MarshalStrict (Phase 0-bootstrap-1 gate 3rd-round finding
// #1) enforce the same invariants on the write path that decodeStrict
// enforces on the read path. Manifest itself has no tag rules — the variant
// carries them.
func (m Manifest) Validate() error {
	if m.Value == nil {
		return ErrUnknownManifestKind
	}
	expected, inner, err := manifestVariantMetadata(m.Value)
	if err != nil {
		return err
	}
	if err := validateTaggedUnionDiscriminator(m.Kind, expected, inner, ErrManifestVariantTypeMismatch, ErrManifestVariantKindMismatch); err != nil {
		return err
	}
	return runValidation(m.Value)
}

func manifestVariantMetadata(v ManifestVariant) (expected ManifestKind, inner ManifestKind, err error) {
	switch vv := v.(type) {
	case ManifestSuccess:
		return ManifestKindSuccess, vv.Kind, nil
	case *ManifestSuccess:
		if vv == nil {
			return "", "", ErrUnknownManifestKind
		}
		return ManifestKindSuccess, vv.Kind, nil
	case ManifestError:
		return ManifestKindError, vv.Kind, nil
	case *ManifestError:
		if vv == nil {
			return "", "", ErrUnknownManifestKind
		}
		return ManifestKindError, vv.Kind, nil
	case ManifestTimeout:
		return ManifestKindTimeout, vv.Kind, nil
	case *ManifestTimeout:
		if vv == nil {
			return "", "", ErrUnknownManifestKind
		}
		return ManifestKindTimeout, vv.Kind, nil
	default:
		return "", "", ErrUnknownManifestKind
	}
}

func manifestArtifactPrefix(pass int, agent AgentID) string {
	switch pass {
	case 1:
		return fmt.Sprintf("20-pass1/%s", agent)
	case 2:
		return fmt.Sprintf("50-pass2/%s", agent)
	default:
		return fmt.Sprintf("unknown-pass/%s", agent)
	}
}

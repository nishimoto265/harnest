package policyrepo

import "github.com/nishimoto265/harnest/internal/policyartifact"

const (
	RepoDirName          = "auto-improve"
	RegistryRepoRelPath  = "auto-improve/rules-registry.jsonl"
	RulesRepoDirRelPath  = "auto-improve/rules"
	GuidanceRepoDirPath  = "auto-improve/guidance"
	OverlayRepoDirPath   = ".auto-improve"
	registryLocalName    = "rules-registry.jsonl"
	metadataLocalName    = "snapshot.json"
	idempotencyLocalName = "rules-idempotency-index.jsonl"
	rulesLocalDirName    = "rules"
)

// IsPolicyArtifactPath reports whether a repository-relative path is owned by
// auto-improve policy/harness state rather than task implementation code.
func IsPolicyArtifactPath(path string) bool {
	return policyartifact.Is(path)
}

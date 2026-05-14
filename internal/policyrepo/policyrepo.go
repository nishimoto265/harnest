package policyrepo

import "github.com/nishimoto265/harnest/internal/policyartifact"

const (
	RepoDirName          = "harnest"
	RegistryRepoRelPath  = "harnest/rules-registry.jsonl"
	RulesRepoDirRelPath  = "harnest/rules"
	GuidanceRepoDirPath  = "harnest/guidance"
	OverlayRepoDirPath   = ".harnest"
	registryLocalName    = "rules-registry.jsonl"
	metadataLocalName    = "snapshot.json"
	idempotencyLocalName = "rules-idempotency-index.jsonl"
	rulesLocalDirName    = "rules"
)

// IsPolicyArtifactPath reports whether a repository-relative path is owned by
// harnest policy/harness state rather than task implementation code.
func IsPolicyArtifactPath(path string) bool {
	return policyartifact.Is(path)
}

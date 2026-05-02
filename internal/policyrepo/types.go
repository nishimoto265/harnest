package policyrepo

type snapshot struct {
	registry        []byte
	registryPresent bool
	rules           map[string][]byte
	files           map[string][]byte
}

type SnapshotMetadata struct {
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`
	PolicyBranch  string `json:"policy_branch,omitempty"`
	PolicyHead    string `json:"policy_head,omitempty" validate:"omitempty,sha1_hex"`
	RegistryHead  string `json:"registry_head" validate:"omitempty,sha256_hex"`
}

type ActiveRule struct {
	RuleID   string
	RulePath string
	Body     string
}

type PreparedPublish struct {
	RepoRoot     string
	Branch       string
	ExpectedHead string
	Head         string
	worktreeDir  string
	needsPush    bool
	cleaned      bool
}

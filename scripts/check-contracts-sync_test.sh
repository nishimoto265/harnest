#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/scripts" "$tmpdir/docs/design" "$tmpdir/internal/contracts"
cp "$repo_root/scripts/check-contracts-sync.sh" "$tmpdir/scripts/check-contracts-sync.sh"

cat >"$tmpdir/internal/contracts/registry.go" <<'GO'
package contracts

type RegistryKind string

const (
	RegistryKindAdded RegistryKind = "added"
)

type RuleRegistryAdded struct{}

func (RuleRegistryAdded) ruleRegistryVariant() {}
GO

cat >"$tmpdir/docs/design/io-contracts.md" <<'MD'
# I/O contracts

`RuleRegistryAdded`

**Recovery state machine**

| stage | entry condition | allowed next transitions | required fields | startup / recovery 動作 |
|---|---|---|---|---|
| `planning` | test | test | test | test |
| `finalized` | test | test | test | test |

**planning recovery decision tree**
MD

cat >"$tmpdir/internal/contracts/intention.go" <<'GO'
package contracts

type IntentionStage string

const (
	IntentionStagePlanning         IntentionStage = "planning"
	IntentionStagePolicyPublishing IntentionStage = "policy_publishing"
)
GO

if bash "$tmpdir/scripts/check-contracts-sync.sh" >"$tmpdir/out" 2>"$tmpdir/err"; then
  echo "expected missing docs stage to fail" >&2
  exit 1
fi
grep -Fq "intention stage missing in recovery state machine table: policy_publishing" "$tmpdir/err"

cat >"$tmpdir/internal/contracts/intention.go" <<'GO'
package contracts

type IntentionStage string
GO

if bash "$tmpdir/scripts/check-contracts-sync.sh" >"$tmpdir/out" 2>"$tmpdir/err"; then
  echo "expected empty Go stage extraction to fail" >&2
  exit 1
fi
grep -Fq "no intention stages extracted from code" "$tmpdir/err"

cat >"$tmpdir/internal/contracts/registry.go" <<'GO'
package contracts

type RegistryKind string

const (
	RegistryKindAdded   RegistryKind = "added"
	RegistryKindRemoved RegistryKind = "removed"
)

type RuleRegistryAdded struct{}
type RuleRegistryRemoved struct{}

func (RuleRegistryAdded) ruleRegistryVariant() {}
func (RuleRegistryRemoved) ruleRegistryVariant() {}
GO

cat >"$tmpdir/internal/contracts/intention.go" <<'GO'
package contracts

type IntentionStage string

const (
	IntentionStagePlanning IntentionStage = "planning"
)
GO

if bash "$tmpdir/scripts/check-contracts-sync.sh" >"$tmpdir/out" 2>"$tmpdir/err"; then
  echo "expected missing registry docs entries to fail" >&2
  exit 1
fi
grep -Fq "registry variant missing in docs: RuleRegistryRemoved" "$tmpdir/err"
grep -Fq "registry kind missing in docs: removed" "$tmpdir/err"

printf 'check-contracts-sync failure-path tests OK\n'

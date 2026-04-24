#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/auto-improve-install-test.XXXXXX")"
trap 'rm -rf "$TMP_ROOT"' EXIT

repo_root="$TMP_ROOT/repo"
install_dir="$TMP_ROOT/bin"
home_dir="$TMP_ROOT/home"
plist_dir="$home_dir/Library/LaunchAgents"
fake_bin="$TMP_ROOT/fake-bin"
payload="$TMP_ROOT/auto-improve-payload"
launchctl_log="$TMP_ROOT/launchctl.log"
install_output_path="$TMP_ROOT/install.out"

mkdir -p "$repo_root" "$install_dir" "$plist_dir" "$fake_bin"
printf 'paths:\n  runs: "%s"\nworktree:\n  base: "%s"\n' "$TMP_ROOT/runs" "$TMP_ROOT/worktrees" >"$repo_root/config.yaml"
printf 'legacy plist\n' >"$plist_dir/com.nishimoto265.auto-improve.plist"

cat >"$payload" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "preflight" ]]; then
  exit 0
fi
exit 1
EOF
chmod +x "$payload"

expected_sha="$(shasum -a 256 "$payload" | awk '{print $1}')"

cat >"$fake_bin/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) printf 'Darwin\n' ;;
esac
EOF
chmod +x "$fake_bin/uname"

cat >"$fake_bin/id" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -un) printf 'testuser\n' ;;
  -u) printf '501\n' ;;
  *) /usr/bin/id "$@" ;;
esac
EOF
chmod +x "$fake_bin/id"

cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
out=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [[ -z "$out" ]]; then
  echo "fake curl expected -o" >&2
  exit 2
fi
cp "$AUTO_IMPROVE_TEST_PAYLOAD" "$out"
EOF
chmod +x "$fake_bin/curl"

cat >"$fake_bin/launchctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$AUTO_IMPROVE_TEST_LAUNCHCTL_LOG"
if [[ "${1:-}" == "bootstrap" ]]; then
  exit 42
fi
exit 0
EOF
chmod +x "$fake_bin/launchctl"

set +e
PATH="$fake_bin:$PATH" \
INSTALL_DIR="$install_dir" \
REPO_ROOT="$repo_root" \
AUTO_IMPROVE_RELEASE_URL="https://example.invalid/auto-improve" \
AUTO_IMPROVE_EXPECTED_SHA256="$expected_sha" \
AUTO_IMPROVE_LAUNCHD_USER="testuser" \
AUTO_IMPROVE_LAUNCHD_HOME="$home_dir" \
AUTO_IMPROVE_PLIST_DIR="$plist_dir" \
AUTO_IMPROVE_INSTANCE="new" \
AUTO_IMPROVE_TEST_PAYLOAD="$payload" \
AUTO_IMPROVE_TEST_LAUNCHCTL_LOG="$launchctl_log" \
bash "$ROOT/scripts/install.sh" >"$install_output_path" 2>&1
status=$?
set -e

if [[ "$status" -ne 4 ]]; then
  cat "$install_output_path" >&2
  echo "expected install.sh to fail with exit 4, got $status" >&2
  exit 1
fi

legacy_plist="$plist_dir/com.nishimoto265.auto-improve.plist"
if [[ ! -f "$legacy_plist" ]]; then
  echo "legacy plist was removed after new bootstrap failed" >&2
  exit 1
fi
if [[ "$(cat "$legacy_plist")" != "legacy plist" ]]; then
  echo "legacy plist content changed after new bootstrap failed" >&2
  exit 1
fi

if grep -F "com.nishimoto265.auto-improve.plist" "$launchctl_log" >/dev/null 2>&1; then
  echo "legacy launchd job was migrated before new bootstrap succeeded" >&2
  exit 1
fi

printf 'install migration bootstrap failure test passed\n'

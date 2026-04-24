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
case "${1:-}" in
  bootstrap)
    if [[ "${AUTO_IMPROVE_TEST_BOOTSTRAP_FAIL:-0}" == "1" ]]; then
      exit 42
    fi
    exit 0
    ;;
  bootout)
    if [[ "${AUTO_IMPROVE_TEST_LEGACY_BOOTOUT_FAIL:-0}" == "1" && "$*" == *"com.nishimoto265.auto-improve.plist"* ]]; then
      exit 42
    fi
    exit 0
    ;;
  print)
    if [[ "${2:-}" == */com.nishimoto265.auto-improve && "${AUTO_IMPROVE_TEST_LEGACY_LOADED:-0}" == "1" ]]; then
      exit 0
    fi
    exit 42
    ;;
esac
exit 0
EOF
chmod +x "$fake_bin/launchctl"

cat >"$fake_bin/sudo" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-u" ]]; then
  shift 2
fi
exec "$@"
EOF
chmod +x "$fake_bin/sudo"

cat >"$fake_bin/chown" <<'EOF'
#!/usr/bin/env bash
if [[ "${AUTO_IMPROVE_TEST_CHOWN_FAIL:-0}" == "1" ]]; then
  exit 42
fi
/usr/sbin/chown "$@"
EOF
chmod +x "$fake_bin/chown"

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
AUTO_IMPROVE_TEST_BOOTSTRAP_FAIL=1 \
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

standalone_log="$TMP_ROOT/standalone-launchctl.log"
printf 'legacy plist\n' >"$legacy_plist"
PATH="$fake_bin:$PATH" \
INSTALL_DIR="$install_dir" \
TARGET="$install_dir/auto-improve" \
REPO_ROOT="$repo_root" \
AUTO_IMPROVE_LAUNCHD_USER="testuser" \
AUTO_IMPROVE_LAUNCHD_HOME="$home_dir" \
AUTO_IMPROVE_PLIST_DIR="$plist_dir" \
AUTO_IMPROVE_INSTANCE="standalone" \
AUTO_IMPROVE_TEST_LAUNCHCTL_LOG="$standalone_log" \
bash "$ROOT/scripts/install-launchd.sh" >"$install_output_path" 2>&1

if [[ ! -f "$legacy_plist" ]]; then
  cat "$install_output_path" >&2
  echo "standalone install-launchd migrated legacy plist before the new label was loaded" >&2
  exit 1
fi
if grep -F "com.nishimoto265.auto-improve.plist" "$standalone_log" >/dev/null 2>&1; then
  cat "$standalone_log" >&2
  echo "standalone install-launchd touched legacy launchd job before the new label was loaded" >&2
  exit 1
fi

printf 'legacy plist\n' >"$legacy_plist"
set +e
PATH="$fake_bin:$PATH" \
INSTALL_DIR="$install_dir" \
REPO_ROOT="$repo_root" \
AUTO_IMPROVE_RELEASE_URL="https://example.invalid/auto-improve" \
AUTO_IMPROVE_EXPECTED_SHA256="$expected_sha" \
AUTO_IMPROVE_LAUNCHD_USER="testuser" \
AUTO_IMPROVE_LAUNCHD_HOME="$home_dir" \
AUTO_IMPROVE_PLIST_DIR="$plist_dir" \
AUTO_IMPROVE_INSTANCE="bootoutfail" \
AUTO_IMPROVE_TEST_PAYLOAD="$payload" \
AUTO_IMPROVE_TEST_LAUNCHCTL_LOG="$launchctl_log" \
AUTO_IMPROVE_TEST_LEGACY_BOOTOUT_FAIL=1 \
AUTO_IMPROVE_TEST_LEGACY_LOADED=1 \
bash "$ROOT/scripts/install.sh" >"$install_output_path" 2>&1
status=$?
set -e

if [[ "$status" -ne 4 ]]; then
  cat "$install_output_path" >&2
  echo "expected install.sh legacy bootout failure to exit 4, got $status" >&2
  exit 1
fi
if [[ "$(cat "$legacy_plist")" != "legacy plist" ]]; then
  cat "$install_output_path" >&2
  echo "legacy plist was removed even though bootout failed and the job was still loaded" >&2
  exit 1
fi

printf 'legacy plist\n' >"$legacy_plist"
set +e
PATH="$fake_bin:$PATH" \
INSTALL_DIR="$install_dir" \
REPO_ROOT="$repo_root" \
AUTO_IMPROVE_RELEASE_URL="https://example.invalid/auto-improve" \
AUTO_IMPROVE_EXPECTED_SHA256="$expected_sha" \
AUTO_IMPROVE_LAUNCHD_USER="testuser" \
AUTO_IMPROVE_LAUNCHD_HOME="$home_dir" \
AUTO_IMPROVE_PLIST_DIR="$plist_dir" \
AUTO_IMPROVE_INSTANCE="unloaded" \
AUTO_IMPROVE_TEST_PAYLOAD="$payload" \
AUTO_IMPROVE_TEST_LAUNCHCTL_LOG="$launchctl_log" \
AUTO_IMPROVE_TEST_LEGACY_BOOTOUT_FAIL=1 \
bash "$ROOT/scripts/install.sh" >"$install_output_path" 2>&1
status=$?
set -e

if [[ "$status" -ne 0 ]]; then
  cat "$install_output_path" >&2
  echo "expected install.sh to succeed when legacy bootout failed but label was unloaded, got $status" >&2
  exit 1
fi
if [[ -f "$legacy_plist" ]]; then
  cat "$install_output_path" >&2
  echo "legacy plist was not removed after bootout failed with the label verified unloaded" >&2
  exit 1
fi

chown_plist="$plist_dir/com.nishimoto265.auto-improve.chownfail.plist"
printf 'old chown plist\n' >"$chown_plist"

set +e
PATH="$fake_bin:$PATH" \
INSTALL_DIR="$install_dir" \
REPO_ROOT="$repo_root" \
AUTO_IMPROVE_RELEASE_URL="https://example.invalid/auto-improve" \
AUTO_IMPROVE_EXPECTED_SHA256="$expected_sha" \
AUTO_IMPROVE_LAUNCHD_USER="otheruser" \
AUTO_IMPROVE_LAUNCHD_HOME="$home_dir" \
AUTO_IMPROVE_PLIST_DIR="$plist_dir" \
AUTO_IMPROVE_INSTANCE="chownfail" \
AUTO_IMPROVE_TEST_PAYLOAD="$payload" \
AUTO_IMPROVE_TEST_LAUNCHCTL_LOG="$launchctl_log" \
AUTO_IMPROVE_TEST_CHOWN_FAIL=1 \
bash "$ROOT/scripts/install.sh" >"$install_output_path" 2>&1
status=$?
set -e

if [[ "$status" -ne 4 ]]; then
  cat "$install_output_path" >&2
  echo "expected install.sh chown failure to exit 4, got $status" >&2
  exit 1
fi

if [[ "$(cat "$chown_plist")" != "old chown plist" ]]; then
  echo "plist backup was not restored after install-launchd chown failure" >&2
  exit 1
fi

printf 'install migration tests passed\n'

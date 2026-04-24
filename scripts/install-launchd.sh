#!/usr/bin/env bash

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "install-launchd.sh is only supported on macOS" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/launchd-common.sh
. "$SCRIPT_DIR/launchd-common.sh"

INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
TARGET="${TARGET:-$INSTALL_DIR/auto-improve}"
REPO_ROOT_INPUT="${REPO_ROOT:-$(pwd -P)}"
PLIST_OVERRIDE_SET=0
if [[ -n "${PLIST:-}" ]]; then
  PLIST_OVERRIDE_SET=1
fi

if ! REPO_ROOT="$(cd "$REPO_ROOT_INPUT" 2>/dev/null && pwd -P)"; then
  echo "REPO_ROOT=$REPO_ROOT_INPUT does not exist" >&2
  exit 1
fi

PLIST_DIR="$(auto_improve_launchd_plist_dir)"
PLIST="$(auto_improve_launchd_plist_path)"
LAUNCHD_LABEL="$(auto_improve_launchd_label)"
LAUNCHD_PATH="$(auto_improve_launchd_path)"
LAUNCHD_USER="$(auto_improve_launchd_user)"

if [[ ! -f "$REPO_ROOT/config.yaml" ]]; then
  echo "config.yaml not found in REPO_ROOT=$REPO_ROOT" >&2
  exit 1
fi

mkdir -p "$PLIST_DIR"
tmp_plist="$(mktemp "$PLIST_DIR/.${LAUNCHD_LABEL}.plist.tmp.XXXXXX")"

cleanup() {
  rm -f "$tmp_plist"
}
trap cleanup EXIT

escaped_target="$(auto_improve_xml_escape "$TARGET")"
escaped_repo_root="$(auto_improve_xml_escape "$REPO_ROOT")"
escaped_path="$(auto_improve_xml_escape "$LAUNCHD_PATH")"
escaped_label="$(auto_improve_xml_escape "$LAUNCHD_LABEL")"

cat >"$tmp_plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$escaped_label</string>
  <key>ProgramArguments</key>
  <array>
    <string>$escaped_target</string>
    <string>run</string>
    <string>--detect-loop</string>
    <string>--with-preflight</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>$escaped_path</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>$escaped_repo_root</string>
  <key>StartInterval</key>
  <integer>3600</integer>
  <key>RunAtLoad</key>
  <false/>
</dict>
</plist>
EOF

mv "$tmp_plist" "$PLIST"
if [[ "$(id -un)" != "$LAUNCHD_USER" ]]; then
  chown "$LAUNCHD_USER" "$PLIST"
fi

if [[ "$PLIST_OVERRIDE_SET" -eq 0 ]]; then
  auto_improve_migrate_legacy_launchd_plist "$PLIST"
fi

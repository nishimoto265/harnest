#!/usr/bin/env bash

set -euo pipefail

if [[ "${AUTO_IMPROVE_TEST_MODE:-0}" == "1" && -n "${AUTO_IMPROVE_TEST_SAFE_PATH:-}" ]]; then
  if [[ "$(/usr/bin/id -u)" == "0" || -n "${SUDO_USER:-}" ]]; then
    echo "AUTO_IMPROVE_TEST_MODE is not allowed for privileged launchd uninstalls" >&2
    exit 1
  fi
  PATH="$AUTO_IMPROVE_TEST_SAFE_PATH"
else
  PATH="/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"
fi
export PATH

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "uninstall-launchd.sh is only supported on macOS" >&2
  exit 1
fi

# shellcheck source=scripts/launchd-common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/launchd-common.sh"

PLIST="$(auto_improve_launchd_plist_path)"
LABEL="$(auto_improve_launchd_label)"
if ! auto_improve_launchctl_bootout_checked "$PLIST"; then
  if auto_improve_launchctl_label_loaded "$LABEL"; then
    echo "failed to unload launchd job $LABEL; keeping $PLIST" >&2
    exit 1
  fi
fi
rm -f "$PLIST"

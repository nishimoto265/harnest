#!/usr/bin/env bash

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "uninstall-launchd.sh is only supported on macOS" >&2
  exit 1
fi

# shellcheck source=scripts/launchd-common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/launchd-common.sh"

PLIST="$(auto_improve_launchd_plist_path)"
auto_improve_launchctl_bootout "$PLIST"
rm -f "$PLIST"

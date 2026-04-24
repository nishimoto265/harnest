#!/usr/bin/env bash

set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
TARGET="$INSTALL_DIR/auto-improve"
STAGE="$INSTALL_DIR/.auto-improve.new.$$"
BACKUP="$INSTALL_DIR/.auto-improve.bak.$$"
REPO_ROOT_INPUT="${REPO_ROOT:-$(pwd -P)}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/launchd-common.sh
. "$SCRIPT_DIR/launchd-common.sh"
INSTALL_LAUNCHD_SCRIPT="$SCRIPT_DIR/install-launchd.sh"
REPO_SLUG="${AUTO_IMPROVE_REPO_SLUG:-nishimoto265/auto-improve}"

ARCHIVE_URL_DEFAULT_BASE="https://github.com/${REPO_SLUG}/releases/latest/download"
RELEASE_URL="${AUTO_IMPROVE_RELEASE_URL:-}"
DOWNLOAD_BASE_URL="${AUTO_IMPROVE_RELEASE_BASE_URL:-$ARCHIVE_URL_DEFAULT_BASE}"
CHECKSUMS_URL="${AUTO_IMPROVE_CHECKSUMS_URL:-}"
EXPECTED_SHA256="${AUTO_IMPROVE_EXPECTED_SHA256:-}"
PLIST_OVERRIDE_SET=0
if [[ -n "${PLIST:-}" ]]; then
  PLIST_OVERRIDE_SET=1
fi

archive_path=""
checksums_path=""

cleanup() {
  rm -f "$STAGE"
  if [[ -n "$archive_path" ]]; then
    rm -f "$archive_path"
  fi
  if [[ -n "$checksums_path" ]]; then
    rm -f "$checksums_path"
  fi
}
trap cleanup EXIT

sha256_file() {
  local path="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
    return 0
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
    return 0
  fi
  echo "neither shasum nor sha256sum is available for checksum verification" >&2
  return 1
}

resolve_effective_url() {
  local url="$1"
  curl -fsSIL -o /dev/null -w '%{url_effective}' "$url"
}

resolve_latest_release_tag() {
  local repo_slug="$1"
  local latest_url effective_url tag
  latest_url="https://github.com/${repo_slug}/releases/latest"
  effective_url="$(resolve_effective_url "$latest_url")"
  tag="${effective_url##*/}"
  if [[ -z "$tag" || "$tag" == "latest" ]]; then
    echo "failed to resolve latest release tag for ${repo_slug}" >&2
    return 1
  fi
  printf '%s\n' "$tag"
}

if ! REPO_ROOT="$(cd "$REPO_ROOT_INPUT" 2>/dev/null && pwd -P)"; then
  echo "REPO_ROOT=$REPO_ROOT_INPUT does not exist" >&2
  exit 1
fi

if [[ -d "$INSTALL_DIR" ]]; then
  if [[ ! -w "$INSTALL_DIR" ]]; then
    echo "INSTALL_DIR=$INSTALL_DIR not writable. Re-run with sudo, or set INSTALL_DIR=\$HOME/.local/bin" >&2
    exit 2
  fi
elif ! mkdir -p "$INSTALL_DIR"; then
  echo "INSTALL_DIR=$INSTALL_DIR not writable. Re-run with sudo, or set INSTALL_DIR=\$HOME/.local/bin" >&2
  exit 2
fi

if [[ ! -f "$REPO_ROOT/config.yaml" ]]; then
  echo "config.yaml not found in REPO_ROOT=$REPO_ROOT" >&2
  exit 1
fi

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) asset_os="darwin" ;;
  Linux) asset_os="linux" ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  arm64|aarch64) asset_arch="arm64" ;;
  x86_64|amd64) asset_arch="amd64" ;;
  *)
    echo "unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

if [[ "$asset_os" == "linux" && "$asset_arch" != "amd64" ]]; then
  echo "unsupported platform: ${asset_os}/${asset_arch}" >&2
  exit 1
fi

PLIST=""
PLIST_BAK=""
if [[ "$asset_os" == "darwin" ]]; then
  PLIST="$(auto_improve_launchd_plist_path)"
  PLIST_BAK="${PLIST}.bak.$$"
fi

asset_name="auto-improve_${asset_os}_${asset_arch}"
if [[ -z "$RELEASE_URL" ]]; then
  release_tag="$(resolve_latest_release_tag "$REPO_SLUG")" || exit 1
  release_base="https://github.com/${REPO_SLUG}/releases/download/${release_tag}"
  RELEASE_URL="${release_base}/${asset_name}"
  if [[ -z "$CHECKSUMS_URL" && -z "$EXPECTED_SHA256" ]]; then
    CHECKSUMS_URL="${release_base}/checksums.txt"
  fi
fi
if [[ -z "$CHECKSUMS_URL" && -z "$EXPECTED_SHA256" ]]; then
  asset_name="$(basename "${RELEASE_URL%%\?*}")"
  CHECKSUMS_URL="${RELEASE_URL%/*}/checksums.txt"
fi

archive_path="$(mktemp "$INSTALL_DIR/.auto-improve.download.XXXXXX")"
if ! curl -fsSL "$RELEASE_URL" -o "$archive_path"; then
  cat >&2 <<EOF
failed to download release asset: ${RELEASE_URL}
If no GitHub release has been published yet, either cut a release first or set:
  AUTO_IMPROVE_RELEASE_URL=<direct binary url>
  AUTO_IMPROVE_EXPECTED_SHA256=<sha256>
EOF
  exit 1
fi
if [[ -z "$EXPECTED_SHA256" ]]; then
  asset_name="${asset_name:-$(basename "${RELEASE_URL%%\?*}")}"
  checksums_path="$(mktemp "$INSTALL_DIR/.auto-improve.checksums.XXXXXX")"
  curl -fsSL "$CHECKSUMS_URL" -o "$checksums_path"
  EXPECTED_SHA256="$(awk -v name="$asset_name" '$2 == name { print $1; exit }' "$checksums_path")"
  if [[ -z "$EXPECTED_SHA256" ]]; then
    echo "release checksum for ${asset_name} not found in ${CHECKSUMS_URL}" >&2
    exit 1
  fi
fi
actual_sha256="$(sha256_file "$archive_path")"
if [[ "$actual_sha256" != "$EXPECTED_SHA256" ]]; then
  echo "checksum verification failed for ${RELEASE_URL}: want=${EXPECTED_SHA256} have=${actual_sha256}" >&2
  exit 1
fi
mv "$archive_path" "$STAGE"
archive_path=""
chmod +x "$STAGE"

run_preflight_check() {
  local target_user target_home target_path
  target_user="$(auto_improve_launchd_user)"
  target_home="$(auto_improve_launchd_home)"
  target_path="$(auto_improve_launchd_path)"
  (
    cd "$REPO_ROOT"
    if [[ "$(id -un)" != "$target_user" ]]; then
      sudo -u "$target_user" env HOME="$target_home" PATH="$target_path" "$STAGE" preflight
    else
      env HOME="$target_home" PATH="$target_path" "$STAGE" preflight
    fi
  )
}

if ! run_preflight_check; then
  rm -f "$STAGE"
  cat >&2 <<EOF
preflight failed while validating the staged binary.
Install or authenticate the required runtime dependencies, then retry:
  gh: https://cli.github.com/
  claude: https://docs.anthropic.com/en/docs/claude-code/getting-started
  codex: https://openai.com/codex
  jq: https://jqlang.org/download/
  yq: https://github.com/mikefarah/yq
EOF
  exit 1
fi

if [[ -f "$TARGET" ]]; then
  cp -p "$TARGET" "$BACKUP"
fi

if [[ -f "$PLIST" ]]; then
  cp -p "$PLIST" "$PLIST_BAK"
fi

restore_backup_launchd() {
  if [[ ! -f "$PLIST_BAK" ]]; then
    rm -f "$PLIST"
    return 0
  fi
  mv "$PLIST_BAK" "$PLIST"
  if ! auto_improve_launchctl_bootstrap "$PLIST"; then
    cat >&2 <<EOF
failed to restore the previous launchd job for $(auto_improve_launchd_user).
manual recovery:
  launchctl bootstrap $(auto_improve_launchd_domain) "$PLIST"
EOF
    return 1
  fi
  return 0
}

if ! mv "$STAGE" "$TARGET"; then
  [[ -f "$BACKUP" ]] && mv "$BACKUP" "$TARGET"
  rm -f "$STAGE"
  exit 3
fi

if [[ "$asset_os" == "darwin" ]]; then
  if ! INSTALL_DIR="$INSTALL_DIR" TARGET="$TARGET" REPO_ROOT="$REPO_ROOT" PLIST="$PLIST" "$INSTALL_LAUNCHD_SCRIPT"; then
    rm -f "$TARGET"
    [[ -f "$BACKUP" ]] && mv "$BACKUP" "$TARGET"
    restore_backup_launchd || true
    exit 4
  fi

  auto_improve_launchctl_bootout "$PLIST"
  if ! auto_improve_launchctl_bootstrap "$PLIST"; then
    rm -f "$TARGET"
    [[ -f "$BACKUP" ]] && mv "$BACKUP" "$TARGET"
    restore_backup_launchd || true
    exit 4
  fi
  if [[ "$PLIST_OVERRIDE_SET" -eq 0 ]]; then
    auto_improve_migrate_legacy_launchd_plist "$PLIST"
  fi
fi

rm -f "$BACKUP" "$PLIST_BAK"

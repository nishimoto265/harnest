#!/usr/bin/env bash

auto_improve_launchd_user() {
  if [[ -n "${AUTO_IMPROVE_LAUNCHD_USER:-}" ]]; then
    printf '%s\n' "$AUTO_IMPROVE_LAUNCHD_USER"
    return 0
  fi
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "$SUDO_USER"
    return 0
  fi
  id -un
}

auto_improve_launchd_home() {
  if [[ -n "${AUTO_IMPROVE_LAUNCHD_HOME:-}" ]]; then
    printf '%s\n' "$AUTO_IMPROVE_LAUNCHD_HOME"
    return 0
  fi

  local user
  user="$(auto_improve_launchd_user)"
  if [[ "$user" == "$(id -un)" && -n "${HOME:-}" ]]; then
    printf '%s\n' "$HOME"
    return 0
  fi

  local home
  if command -v getent >/dev/null 2>&1; then
    home="$(getent passwd "$user" | awk -F: 'NR==1 {print $6}')"
  elif command -v dscl >/dev/null 2>&1; then
    home="$(dscl . -read "/Users/$user" NFSHomeDirectory 2>/dev/null | awk 'NR==1 {print $2}')"
  fi
  if [[ -z "$home" ]]; then
    echo "failed to resolve home directory for launchd user: $user" >&2
    return 1
  fi
  printf '%s\n' "$home"
}

auto_improve_default_cli_path() {
  local home="$1"
  printf '%s/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin\n' "$home"
}

auto_improve_sanitize_launchd_instance() {
  local raw="$1"
  local sanitized
  sanitized="$(
    printf '%s' "$raw" \
      | tr '[:upper:]' '[:lower:]' \
      | sed -E 's/[^a-z0-9._-]+/-/g; s/^[._-]+//; s/[._-]+$//; s/[._-][._-]+/-/g'
  )"
  if [[ -z "$sanitized" ]]; then
    sanitized="default"
  fi
  printf '%s\n' "$sanitized"
}

auto_improve_launchd_instance() {
  if [[ -n "${AUTO_IMPROVE_INSTANCE:-}" ]]; then
    auto_improve_sanitize_launchd_instance "$AUTO_IMPROVE_INSTANCE"
    return 0
  fi
  if [[ -n "${REPO_ROOT:-}" ]]; then
    auto_improve_sanitize_launchd_instance "$REPO_ROOT"
    return 0
  fi
  auto_improve_sanitize_launchd_instance "$(pwd -P)"
}

auto_improve_launchd_label() {
  printf 'com.nishimoto265.auto-improve.%s\n' "$(auto_improve_launchd_instance)"
}

auto_improve_launchd_domain() {
  local user
  user="$(auto_improve_launchd_user)"
  printf 'gui/%s\n' "$(id -u "$user")"
}

auto_improve_launchd_plist_dir() {
  if [[ -n "${AUTO_IMPROVE_PLIST_DIR:-}" ]]; then
    printf '%s\n' "$AUTO_IMPROVE_PLIST_DIR"
    return 0
  fi
  printf '%s/Library/LaunchAgents\n' "$(auto_improve_launchd_home)"
}

auto_improve_launchd_plist_path() {
  if [[ -n "${PLIST:-}" ]]; then
    printf '%s\n' "$PLIST"
    return 0
  fi
  printf '%s/%s.plist\n' "$(auto_improve_launchd_plist_dir)" "$(auto_improve_launchd_label)"
}

auto_improve_launchd_path() {
  if [[ -n "${AUTO_IMPROVE_LAUNCHD_PATH:-}" ]]; then
    printf '%s\n' "$AUTO_IMPROVE_LAUNCHD_PATH"
    return 0
  fi
  printf '%s\n' "$(auto_improve_default_cli_path "$(auto_improve_launchd_home)")"
}

auto_improve_launchctl_bootout() {
  local plist="$1"
  launchctl bootout "$(auto_improve_launchd_domain)" "$plist" >/dev/null 2>&1 || true
}

auto_improve_launchctl_bootstrap() {
  local plist="$1"
  launchctl bootstrap "$(auto_improve_launchd_domain)" "$plist"
}

auto_improve_xml_escape() {
  local value="$1"
  value=${value//&/&amp;}
  value=${value//</&lt;}
  value=${value//>/&gt;}
  printf '%s' "$value"
}

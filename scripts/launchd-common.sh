#!/usr/bin/env bash

harnest_launchd_user() {
  if [[ -n "${HARNEST_LAUNCHD_USER:-}" ]]; then
    printf '%s\n' "$HARNEST_LAUNCHD_USER"
    return 0
  fi
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "$SUDO_USER"
    return 0
  fi
  id -un
}

harnest_launchd_home() {
  if [[ -n "${HARNEST_LAUNCHD_HOME:-}" ]]; then
    printf '%s\n' "$HARNEST_LAUNCHD_HOME"
    return 0
  fi

  local user
  user="$(harnest_launchd_user)"
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

harnest_default_cli_path() {
  local home="$1"
  printf '%s/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin\n' "$home"
}

harnest_sanitize_launchd_instance() {
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

harnest_launchd_instance() {
  if [[ -n "${HARNEST_INSTANCE:-}" ]]; then
    harnest_sanitize_launchd_instance "$HARNEST_INSTANCE"
    return 0
  fi
  if [[ -n "${REPO_ROOT:-}" ]]; then
    harnest_sanitize_launchd_instance "$REPO_ROOT"
    return 0
  fi
  harnest_sanitize_launchd_instance "$(pwd -P)"
}

harnest_launchd_label() {
  printf 'com.nishimoto265.harnest.%s\n' "$(harnest_launchd_instance)"
}

harnest_legacy_launchd_label() {
  printf 'com.nishimoto265.harnest\n'
}

harnest_launchd_domain() {
  local user
  user="$(harnest_launchd_user)"
  printf 'gui/%s\n' "$(id -u "$user")"
}

harnest_launchd_plist_dir() {
  if [[ -n "${HARNEST_PLIST_DIR:-}" ]]; then
    printf '%s\n' "$HARNEST_PLIST_DIR"
    return 0
  fi
  printf '%s/Library/LaunchAgents\n' "$(harnest_launchd_home)"
}

harnest_launchd_plist_path() {
  if [[ -n "${PLIST:-}" ]]; then
    printf '%s\n' "$PLIST"
    return 0
  fi
  printf '%s/%s.plist\n' "$(harnest_launchd_plist_dir)" "$(harnest_launchd_label)"
}

harnest_legacy_launchd_plist_path() {
  printf '%s/%s.plist\n' "$(harnest_launchd_plist_dir)" "$(harnest_legacy_launchd_label)"
}

harnest_legacy_launchd_plist_matches_repo_root() {
  local plist="$1"
  local repo_root_input="${REPO_ROOT:-}"
  if [[ -z "$repo_root_input" ]]; then
    return 1
  fi

  local repo_root
  if ! repo_root="$(cd "$repo_root_input" 2>/dev/null && pwd -P)"; then
    return 1
  fi

  local escaped_repo_root
  escaped_repo_root="$(harnest_xml_escape "$repo_root")"
  awk -v want="<string>${escaped_repo_root}</string>" '
    /<key>WorkingDirectory<\/key>/ {
      seen = 1
      next
    }
    seen && /<string>/ {
      if (index($0, want) > 0) {
        found = 1
      }
      seen = 0
    }
    END {
      exit(found ? 0 : 1)
    }
  ' "$plist"
}

harnest_launchd_path() {
  if [[ -n "${HARNEST_LAUNCHD_PATH:-}" ]]; then
    printf '%s\n' "$HARNEST_LAUNCHD_PATH"
    return 0
  fi
  printf '%s\n' "$(harnest_default_cli_path "$(harnest_launchd_home)")"
}

harnest_launchctl_bootout() {
  local plist="$1"
  harnest_launchctl_bootout_checked "$plist" || true
}

harnest_launchctl_bootout_checked() {
  local plist="$1"
  launchctl bootout "$(harnest_launchd_domain)" "$plist" >/dev/null 2>&1
}

harnest_launchctl_bootstrap() {
  local plist="$1"
  launchctl bootstrap "$(harnest_launchd_domain)" "$plist"
}

harnest_launchctl_label_loaded() {
  local label="$1"
  launchctl print "$(harnest_launchd_domain)/$label" >/dev/null 2>&1
}

harnest_migrate_legacy_launchd_plist() {
  local current_plist="$1"
  local legacy_plist
  legacy_plist="$(harnest_legacy_launchd_plist_path)"
  if [[ "$legacy_plist" == "$current_plist" || ! -e "$legacy_plist" ]]; then
    return 0
  fi
  if ! harnest_legacy_launchd_plist_matches_repo_root "$legacy_plist"; then
    echo "legacy launchd plist does not target REPO_ROOT; keeping $legacy_plist" >&2
    return 0
  fi
  if ! harnest_launchctl_bootout_checked "$legacy_plist"; then
    if harnest_launchctl_label_loaded "$(harnest_legacy_launchd_label)"; then
      echo "failed to unload legacy launchd job; keeping $legacy_plist" >&2
      return 1
    fi
  fi
  rm -f "$legacy_plist"
}

harnest_xml_escape() {
  local value="$1"
  value=${value//&/&amp;}
  value=${value//</&lt;}
  value=${value//>/&gt;}
  printf '%s' "$value"
}

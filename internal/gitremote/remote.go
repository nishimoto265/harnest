package gitremote

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const DefaultGitHubHost = "github.com"

type Info struct {
	Scheme string
	Host   string
	Slug   string
}

func AllowedGitHubHostsFromEnv(env []string) []string {
	hosts := []string{DefaultGitHubHost}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key != "GH_HOST" || strings.TrimSpace(value) == "" {
			continue
		}
		if host := normalizeConfiguredHost(value); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func ParseGitHubRemote(remoteURL string, allowedHosts []string) (Info, error) {
	info, err := parse(remoteURL)
	if err != nil {
		return Info{}, err
	}
	if !hostAllowed(info.Host, allowedHosts) {
		return Info{}, fmt.Errorf("origin remote host %q is not an allowed GitHub host", info.Host)
	}
	return info, nil
}

func parse(remoteURL string) (Info, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return Info{}, fmt.Errorf("origin remote url is empty")
	}
	if strings.HasPrefix(remoteURL, "/") || strings.HasPrefix(remoteURL, "./") || strings.HasPrefix(remoteURL, "../") {
		return Info{}, fmt.Errorf("expected GitHub owner/name remote, got local path %q", remoteURL)
	}

	if strings.Contains(remoteURL, "://") {
		parsed, err := url.Parse(remoteURL)
		if err != nil {
			return Info{}, fmt.Errorf("could not parse git remote url %q: %w", remoteURL, err)
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "https" && scheme != "ssh" {
			return Info{}, fmt.Errorf("unsupported GitHub remote URL scheme %q; supported schemes are https and ssh", parsed.Scheme)
		}
		return infoFromParts(parsed.Scheme, parsed.Host, parsed.Path)
	}

	if host, path, ok := parseSCPStyle(remoteURL); ok {
		return infoFromParts("ssh", host, path)
	}

	return Info{}, fmt.Errorf("could not parse GitHub remote url: %q", remoteURL)
}

func parseSCPStyle(remoteURL string) (string, string, bool) {
	colon := strings.Index(remoteURL, ":")
	if colon <= 0 || strings.Contains(remoteURL[:colon], "/") {
		return "", "", false
	}
	hostPart := remoteURL[:colon]
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if hostPart == "" || remoteURL[colon+1:] == "" {
		return "", "", false
	}
	return hostPart, remoteURL[colon+1:], true
}

func infoFromParts(scheme, host, path string) (Info, error) {
	host = normalizeHostForScheme(scheme, host)
	if host == "" {
		return Info{}, fmt.Errorf("git remote url is missing a host")
	}
	slug, err := slugFromPath(path)
	if err != nil {
		return Info{}, err
	}
	return Info{Scheme: strings.ToLower(scheme), Host: host, Slug: slug}, nil
}

func slugFromPath(path string) (string, error) {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("expected GitHub owner/name repo slug, got %q", path)
	}
	if parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", fmt.Errorf("expected GitHub owner/name repo slug, got %q", path)
	}
	return parts[0] + "/" + parts[1], nil
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = normalizeConfiguredHost(host)
	for _, allowed := range normalizedAllowedHosts(allowedHosts) {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

func normalizedAllowedHosts(allowedHosts []string) []string {
	hosts := make([]string, 0, len(allowedHosts)+1)
	seen := map[string]struct{}{}
	add := func(host string) {
		host = normalizeConfiguredHost(host)
		if host == "" {
			return
		}
		key := strings.ToLower(host)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		hosts = append(hosts, host)
	}
	add(DefaultGitHubHost)
	for _, host := range allowedHosts {
		add(host)
	}
	return hosts
}

func normalizeConfiguredHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err == nil && parsed.Host != "" {
			return normalizeHostForScheme(parsed.Scheme, parsed.Host)
		}
	}
	return normalizeConfiguredHostWithoutScheme(strings.Trim(value, "/"))
}

func normalizeHostForScheme(scheme, host string) string {
	host = normalizeHostBase(host)
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https":
		return stripDefaultPort(host, "443")
	case "ssh":
		return stripDefaultPort(host, "22")
	default:
		return host
	}
}

func normalizeConfiguredHostWithoutScheme(host string) string {
	host = normalizeHostBase(host)
	host = stripDefaultPort(host, "443")
	host = stripDefaultPort(host, "22")
	return host
}

func normalizeHostBase(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "git@")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	return host
}

func stripDefaultPort(host, defaultPort string) string {
	if !strings.HasSuffix(host, ":"+defaultPort) {
		return host
	}
	if h, port, err := net.SplitHostPort(host); err == nil && port == defaultPort {
		return strings.ToLower(h)
	}
	return strings.TrimSuffix(host, ":"+defaultPort)
}

func PreferredRemoteURLForAuth(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	first := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.HasPrefix(strings.ToLower(line), "https://") {
			return line
		}
	}
	return first
}

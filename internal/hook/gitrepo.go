package hook

import (
	"strings"
)

// ParseGitRemote extracts "org/name" from a git remote URL.
// Supports SSH (git@github.com:org/name.git) and HTTPS
// (https://github.com/org/name.git) formats.
// Returns empty string if the URL cannot be parsed.
func ParseGitRemote(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}

	// SSH format: git@github.com:org/name.git
	if i := strings.Index(url, ":"); i >= 0 && !strings.Contains(url, "://") {
		url = url[i+1:]
		url = strings.TrimSuffix(url, ".git")
		return url
	}

	// HTTPS format: https://github.com/org/name.git
	if strings.Contains(url, "://") {
		// Strip scheme + host: everything after the third slash.
		url = strings.TrimPrefix(url, "http://")
		url = strings.TrimPrefix(url, "https://")
		if i := strings.Index(url, "/"); i >= 0 {
			url = url[i+1:]
		} else {
			return ""
		}
		url = strings.TrimSuffix(url, ".git")
		return url
	}

	return ""
}

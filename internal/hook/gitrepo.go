package hook

import (
	"strings"
)

// validateOrgName returns s if it has exactly two non-empty segments
// separated by a single slash, otherwise "".
func validateOrgName(s string) string {
	if strings.Count(s, "/") != 1 {
		return ""
	}
	org, name, _ := strings.Cut(s, "/")
	if org == "" || name == "" {
		return ""
	}
	return s
}

// ParseGitRemote extracts "org/name" from a git remote URL.
// Supports SSH (git@github.com:org/name.git) and HTTPS
// (https://github.com/org/name.git) formats.
// Returns empty string if the URL cannot be parsed.
func ParseGitRemote(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}

	// SSH scp-style: git@github.com:org/name.git
	if i := strings.Index(url, ":"); i >= 0 && !strings.Contains(url, "://") {
		url = url[i+1:]
		return validateOrgName(strings.TrimSuffix(url, ".git"))
	}

	// URL with scheme: ssh://, https://, http://
	if strings.Contains(url, "://") {
		// Strip scheme + host: everything after the first / past ://.
		after := url[strings.Index(url, "://")+3:]
		if i := strings.Index(after, "/"); i >= 0 {
			after = after[i+1:]
		} else {
			return ""
		}
		return validateOrgName(strings.TrimSuffix(after, ".git"))
	}

	return ""
}

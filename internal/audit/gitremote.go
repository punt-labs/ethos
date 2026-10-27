package audit

import (
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
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

// RepoIdentity returns the "org/name" identity of the git checkout at repoRoot,
// derived from its origin remote. It is the same value session-start records in
// a roster's Repo field, computed by the same parser, so a roster's Repo and the
// identity of the checkout that owns it always compare equal.
//
// It returns ("", nil) when repoRoot is empty, is not a repository, has no
// origin remote, or the remote URL cannot be parsed. A checkout with no parseable origin also
// records "" in its rosters, so "" == "" still matches those sessions; callers
// treat a "" identity as "this checkout's own sessions" and leave
// differently-identified sessions to the checkout that owns them. Any
// other git failure (a timeout, git not installed, dubious ownership) is an
// error, never "": an empty identity would match the wrong sessions.
func RepoIdentity(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	out, absent, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: repoRoot, Args: []string{"remote", "get-url", "origin"}, Env: gitenv.Parsed(),
	}, gitenv.AbsentNoRemote|gitenv.AbsentNotRepo)
	if err != nil {
		return "", fmt.Errorf("reading the origin of %s: %w", repoRoot, err)
	}
	if absent {
		return "", nil // no origin, or not a repository
	}
	return ParseGitRemote(string(out)), nil
}

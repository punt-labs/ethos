package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionDateFormat is the YYYY-MM-DD prefix on a per-session
// directory under <repoRoot>/.ethos/sessions/. UTC by convention so
// two operators in different timezones see the same directory name
// for the same session.
const sessionDateFormat = "2006-01-02"

// resolveAuditWritePath returns the absolute path to a session's
// audit JSONL file in the layer that should receive new appends.
//
// When repoRoot is non-empty, the path lives under
// <repoRoot>/.ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl.
// The per-session directory is created if needed. If a directory
// with a different date prefix already exists for the same
// session-id (the wall clock has rolled over since the session
// started), that existing directory is reused — the date in the
// directory name is the session-start date, not the date of the
// most recent write.
//
// When repoRoot is empty, the legacy single-tree fallback is used:
// <globalSessionsDir>/<session-id>.audit.jsonl. The
// globalSessionsDir is created if needed so first-run installations
// outside a repo still produce a log.
//
// Errors only on mkdir failure; missing directories are created
// silently. Returns the resolved path on success.
func resolveAuditWritePath(repoRoot, globalSessionsDir, sessionID string, now time.Time) (string, error) {
	if repoRoot == "" {
		if err := os.MkdirAll(globalSessionsDir, 0o700); err != nil {
			return "", fmt.Errorf("creating %s: %w", globalSessionsDir, err)
		}
		return filepath.Join(globalSessionsDir, filepath.Base(sessionID)+".audit.jsonl"), nil
	}
	dir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return filepath.Join(dir, "audit.jsonl"), nil
}

// resolveRepoSessionDir returns the per-session directory under the
// repo's .ethos/sessions/ tree. Reuses an existing
// <date>-<session-id> directory when one exists (any date prefix);
// otherwise builds a fresh path with today's UTC date. Splitting the
// directory resolution from the path build lets the reader walk the
// same logic without an mkdir side effect.
func resolveRepoSessionDir(repoRoot, sessionID string, now time.Time) (string, error) {
	base := filepath.Join(repoRoot, ".ethos", "sessions")
	existing, err := findSessionDir(base, sessionID)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	date := now.UTC().Format(sessionDateFormat)
	return filepath.Join(base, date+"-"+filepath.Base(sessionID)), nil
}

// findSessionDir walks <repoRoot>/.ethos/sessions and returns the
// path of the first directory whose name ends in "-<sessionID>"
// (any date prefix). Returns the empty string and nil error when
// no such directory exists. A non-existent base directory is
// likewise the empty string, not an error.
//
// Used by both the writer (reuse-existing-or-create-new) and the
// reader (find-where-this-session-lives) so a session whose start
// date differs from the current day still resolves to one place.
func findSessionDir(base, sessionID string) (string, error) {
	id := filepath.Base(sessionID)
	if id == "" {
		return "", fmt.Errorf("empty session id")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", base, err)
	}
	suffix := "-" + id
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, suffix) {
			return filepath.Join(base, name), nil
		}
	}
	return "", nil
}

package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindSealedSessionDirSuffixCollision is B2: a session id must match only
// its own <date>-<id> directory. A bare suffix match let id "abc" resolve to
// "2026-07-21-x-abc", landing one session's chunks in another's tree.
func TestFindSealedSessionDirSuffixCollision(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	mkdir := func(name string) {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("2026-07-21-abc")
	mkdir("2026-07-21-x-abc")

	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "2026-07-21-abc"); got != want {
		t.Errorf("session abc resolved to %q, want %q", got, want)
	}

	got, err = FindSealedSessionDir(repoRoot, "x-abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "2026-07-21-x-abc"); got != want {
		t.Errorf("session x-abc resolved to %q, want %q", got, want)
	}
}

// TestFindSealedSessionDirNoFalseMatch is B2's fall-through case: with only a
// longer-suffixed directory present, a distinct session id must NOT match — it
// falls through to the create-new path.
func TestFindSealedSessionDirNoFalseMatch(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	if err := os.MkdirAll(filepath.Join(base, "2026-07-21-x-abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("session abc matched %q, want no match", got)
	}
}

// TestFindSealedSessionDirRejectsBadDate confirms the 10-char prefix must be a
// valid date, so a non-dated directory that happens to end in the id is not a
// session dir.
func TestFindSealedSessionDirRejectsBadDate(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	if err := os.MkdirAll(filepath.Join(base, "not-a-date-abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("non-dated dir matched %q, want no match", got)
	}
}

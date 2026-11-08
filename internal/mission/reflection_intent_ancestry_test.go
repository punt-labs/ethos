//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All redirects are installed before the operation. These tests exercise stable
// physical ancestry, not concurrent replacement of checked path components.
func reflectionIntentAncestryPath(t *testing.T, path string, data []byte, kind string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if err := os.Remove(path); !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	external := filepath.Join(t.TempDir(), filepath.Base(path))
	require.NoError(t, os.WriteFile(external, data, 0o600))
	switch kind {
	case "regular":
		require.NoError(t, os.WriteFile(path, data, 0o600))
	case "missing-leaf":
	case "missing-ancestor", "directory-link", "dangling-ancestor", "nondirectory":
		require.NoError(t, os.Remove(filepath.Dir(path)))
		switch kind {
		case "directory-link":
			require.NoError(t, os.Symlink(filepath.Dir(external), filepath.Dir(path)))
		case "dangling-ancestor":
			require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Dir(path)))
		case "nondirectory":
			require.NoError(t, os.WriteFile(filepath.Dir(path), []byte("not a directory"), 0o600))
		}
	case "leaf-link":
		require.NoError(t, os.Symlink(external, path))
	case "dangling-leaf":
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), path))
	default:
		t.Fatalf("unknown ancestry fixture %q", kind)
	}
	return external
}

// Pin mtimes away from now so rewriting identical accepted evidence is visible.
func preserveReflectionIntentEvidence(t *testing.T, paths ...string) func() {
	t.Helper()
	checks := make([]func(), 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		stamp := time.Unix(1700000000, 0)
		require.NoError(t, os.Chtimes(path, stamp, stamp))
		info, err := os.Stat(path)
		require.NoError(t, err)
		checks = append(checks, func() {
			t.Helper()
			after, err := os.ReadFile(path)
			if !assert.NoError(t, err, "preserve external/accepted evidence %s", path) {
				return
			}
			assert.Equal(t, data, after, "preserve evidence bytes: %s", path)
			afterInfo, err := os.Stat(path)
			if assert.NoError(t, err) {
				assert.Equal(t, info.ModTime(), afterInfo.ModTime(), "preserve evidence mtime: %s", path)
			}
		})
	}
	return func() {
		t.Helper()
		for _, check := range checks {
			check()
		}
	}
}

func TestReflectionIntentAncestryAcceptedRetry(t *testing.T) {
	for _, action := range []string{"retry", "advance"} {
		t.Run(action, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			p, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, layout.RecordFull)
			require.NoError(t, err)
			s = NewRepositoryStore(p, s.root).WithSessionID("original")
			r := reflectionFor(c.MissionID, 1, RecommendationContinue)
			out, err := s.ApplyReflection(c.MissionID, r)
			require.NoError(t, err)
			require.Equal(t, "applied", out.Application)
			require.Equal(t, "complete", out.Trace)
			archive := s.reflectionArchivePath(c.MissionID, 1)
			data, err := os.ReadFile(archive)
			require.NoError(t, err)
			// Cleanup left an empty real reflection-intents directory. Redirect it
			// to an external matching envelope, not arbitrary malformed input.
			intent := s.intentPath(c.MissionID, 1)
			external := reflectionIntentAncestryPath(t, intent, data, "directory-link")
			check := preserveReflectionIntentEvidence(t, external, archive,
				filepath.Join(p.CheckoutRecordRoot(), "missions", c.MissionID, "reflections.yaml"),
				mustContractPath(t, s, c.MissionID),
				(audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(p))
			if action == "retry" {
				_, err = s.ApplyReflection(c.MissionID, r)
			} else {
				_, err = s.AdvanceRound(c.MissionID, c.Leader)
			}
			check() // RED must show external deletion, not just an unexpected error.
			assert.ErrorContains(t, err, "refusing to follow symlink")
			info, statErr := os.Lstat(filepath.Dir(intent))
			require.NoError(t, statErr)
			assert.NotZero(t, info.Mode()&os.ModeSymlink, "retain the redirect for operator repair")
		})
	}
}

func TestReadReflectionIntentAncestry(t *testing.T) {
	for _, source := range []string{"receipt", "archive"} {
		for _, kind := range []string{"regular", "missing-leaf", "missing-ancestor", "directory-link", "dangling-ancestor", "nondirectory", "leaf-link", "dangling-leaf"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				s, c := authorityStore(t, 3)
				in := retainAcceptedReflection(t, s, c)
				data, err := os.ReadFile(s.intentPath(c.MissionID, 1))
				require.NoError(t, err)
				path := s.intentPath(c.MissionID, 1)
				if source == "archive" {
					path = s.reflectionArchivePath(c.MissionID, 1)
				}
				external := reflectionIntentAncestryPath(t, path, data, kind)
				check := preserveReflectionIntentEvidence(t, external)
				before := legacyReflectionTree(t, s.repoRoot)
				got, err := readIntent(path, c.MissionID)
				switch kind {
				case "regular":
					assert.NoError(t, err)
					assert.Equal(t, in, got)
				case "missing-leaf", "missing-ancestor":
					assert.NoError(t, err)
					assert.Nil(t, got)
				case "nondirectory":
					assert.ErrorContains(t, err, "ancestor is not a directory")
					assert.Nil(t, got)
				default:
					assert.ErrorContains(t, err, "refusing to follow symlink")
					assert.Nil(t, got)
				}
				check()
				assert.Equal(t, before, legacyReflectionTree(t, s.repoRoot), "read validation must not create directories or alter evidence")
			})
		}
	}
}

func TestFinishReflectionIntentAncestry(t *testing.T) {
	for _, kind := range []string{"regular", "missing-leaf", "directory-link", "dangling-ancestor", "nondirectory", "leaf-link", "dangling-leaf"} {
		t.Run(kind, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			p, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, layout.RecordFull)
			require.NoError(t, err)
			s = NewRepositoryStore(p, s.root).WithSessionID("original")
			in := retainAcceptedReflection(t, s, c)
			path := s.intentPath(c.MissionID, 1)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			external := reflectionIntentAncestryPath(t, path, data, kind)
			check := preserveReflectionIntentEvidence(t, external, mustContractPath(t, s, c.MissionID),
				(audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(p))
			before := legacyReflectionTree(t, s.repoRoot)
			checkoutBefore := legacyReflectionTree(t, s.checkoutRoot)
			var checkpoints []string
			// Bypass receipt reading deliberately: cleanup must validate its own
			// precondition before checkpoint, archive or publication side effects.
			err = s.withMissionLock(c.MissionID, true, func() error {
				s.persistenceCheck = func(phase string) error {
					checkpoints = append(checkpoints, phase)
					return nil
				}
				return s.finishReflection(*in)
			})
			check()
			switch kind {
			case "regular", "missing-leaf":
				require.NoError(t, err)
				got, err := readIntent(s.reflectionArchivePath(c.MissionID, 1), c.MissionID)
				require.NoError(t, err)
				assert.Equal(t, in, got)
				assert.NoFileExists(t, path)
				assert.FileExists(t, filepath.Join(p.CheckoutRecordRoot(), "missions", c.MissionID, "reflections.yaml"))
			default:
				if kind == "nondirectory" {
					assert.ErrorContains(t, err, "ancestor is not a directory")
				} else {
					assert.ErrorContains(t, err, "refusing to follow symlink")
				}
				assert.Empty(t, checkpoints, "reject cleanup path before optional work begins")
				assert.Equal(t, before, legacyReflectionTree(t, s.repoRoot), "no archive, receipt or evidence mutation")
				assert.Equal(t, checkoutBefore, legacyReflectionTree(t, s.checkoutRoot), "no publication mutation")
			}
		})
	}
}

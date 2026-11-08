//go:build !windows

package mission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep the original fixture under a sibling name rather than deleting its live
// evidence. Redirects are stable before use; concurrent path swaps are not tested.
func correctionIntentAncestryPath(t *testing.T, f requiredReplayFixture, kind string) (string, string) {
	t.Helper()
	path := f.receipt
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	live := (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)
	liveData, err := os.ReadFile(live)
	require.NoError(t, err)
	external := filepath.Join(t.TempDir(), filepath.Base(path))
	require.NoError(t, os.WriteFile(external, data, 0o600))
	// Supply the original required stream too: a refusal must not be explained
	// by an external receipt with no matching delivered event.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(external), filepath.Base(live)), liveData, 0o600))
	preservedLive := live
	switch kind {
	case "regular":
	case "missing-leaf", "leaf-link", "dangling-leaf":
		require.NoError(t, os.Rename(path, path+".preserved"))
		if kind == "leaf-link" {
			require.NoError(t, os.Symlink(external, path))
		} else if kind == "dangling-leaf" {
			require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(external), "absent"), path))
		}
	case "missing-ancestor", "directory-link", "dangling-ancestor", "nondirectory":
		dir := filepath.Dir(path)
		require.NoError(t, os.Rename(dir, dir+".preserved"))
		preservedLive = filepath.Join(dir+".preserved", filepath.Base(live))
		switch kind {
		case "directory-link":
			require.NoError(t, os.Symlink(filepath.Dir(external), dir))
		case "dangling-ancestor":
			require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(external), "absent"), dir))
		case "nondirectory":
			require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))
		}
	default:
		t.Fatalf("unknown ancestry fixture %q", kind)
	}
	return external, preservedLive
}

func TestCorrectionIntentAncestryAcceptedRetry(t *testing.T) {
	for _, route := range []string{"exact", "ordinary"} {
		t.Run(route, func(t *testing.T) {
			f := acceptedCorrectionRecovery(t)
			external, originalLive := correctionIntentAncestryPath(t, f, "directory-link")
			check := preserveReflectionIntentEvidence(t, external, originalLive,
				filepath.Join(filepath.Dir(external), filepath.Base(originalLive)),
				f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id))
			before := legacyReflectionTree(t, f.store.repoRoot)
			err := acceptedCorrectionAttempt(t, f, route)
			t.Logf("public %s retry returned: %v", route, err)
			check()
			assert.ErrorContains(t, err, "refusing to follow symlink")
			assert.Equal(t, before, legacyReflectionTree(t, f.store.repoRoot), "refusal must preserve the receipt and accepted evidence")
			assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
		})
	}
}

func TestReadCorrectionIntentAncestry(t *testing.T) {
	for _, kind := range []string{"regular", "missing-leaf", "missing-ancestor", "directory-link", "dangling-ancestor", "nondirectory", "leaf-link", "dangling-leaf"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptedCorrectionRecovery(t)
			want, err := f.store.readCorrectionIntent(f.id)
			require.NoError(t, err)
			external, originalLive := correctionIntentAncestryPath(t, f, kind)
			check := preserveReflectionIntentEvidence(t, external, originalLive,
				filepath.Join(filepath.Dir(external), filepath.Base(originalLive)),
				f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id))
			before := legacyReflectionTree(t, f.store.repoRoot)
			got, err := f.store.readCorrectionIntent(f.id)
			switch kind {
			case "regular":
				assert.NoError(t, err)
				assert.Equal(t, want, got)
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
			assert.Equal(t, before, legacyReflectionTree(t, f.store.repoRoot), "read must not create directories or alter evidence")
		})
	}
}

func TestReadCorrectionIntentMalformed(t *testing.T) {
	for _, kind := range []string{"syntax", "unknown-field", "duplicate-key", "multiple-documents", "version", "event-session"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptedCorrectionRecovery(t)
			data, err := os.ReadFile(f.receipt)
			require.NoError(t, err)
			body := string(data)
			switch kind {
			case "syntax":
				body = "[broken YAML"
			case "unknown-field":
				body += "unexpected: value\n"
			case "duplicate-key":
				body += "version: 1\n"
			case "multiple-documents":
				body += "---\nversion: 1\n"
			case "version":
				body = strings.Replace(body, "version: 1", "version: 2", 1)
			case "event-session":
				body = strings.Replace(body, "event_session: original", "event_session: ../outside", 1)
			}
			require.NotEqual(t, string(data), body)
			require.NoError(t, os.WriteFile(f.receipt, []byte(body), 0o600))
			check := preserveReflectionIntentEvidence(t, f.receipt, f.store.correctionsPath(f.id),
				mustContractPath(t, f.store, f.id),
				(audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy))
			before := legacyReflectionTree(t, f.store.repoRoot)
			got, err := f.store.readCorrectionIntent(f.id)
			assert.Error(t, err)
			assert.Nil(t, got)
			check()
			assert.Equal(t, before, legacyReflectionTree(t, f.store.repoRoot))
		})
	}
}

func TestRemoveCorrectionIntentAncestry(t *testing.T) {
	for _, kind := range []string{"regular", "missing-leaf", "missing-ancestor", "directory-link", "dangling-ancestor", "nondirectory", "leaf-link", "dangling-leaf"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptedCorrectionRecovery(t)
			external, originalLive := correctionIntentAncestryPath(t, f, kind)
			check := preserveReflectionIntentEvidence(t, external, originalLive,
				filepath.Join(filepath.Dir(external), filepath.Base(originalLive)),
				f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id))
			before := legacyReflectionTree(t, f.store.repoRoot)
			// Bypass the reader: deletion needs its own precheck, even when its
			// caller already has a validated, accepted receipt in memory.
			err := f.store.withMissionLock(f.id, true, func() error {
				return f.store.removeCorrectionIntent(f.id)
			})
			t.Logf("direct cleanup %s returned: %v", kind, err)
			check() // RED must expose actual external deletion, not just an error.
			switch kind {
			case "regular", "missing-leaf":
				assert.NoError(t, err)
				assert.NoFileExists(t, f.receipt)
			case "missing-ancestor":
				// Removal ignores a missing leaf, but directory sync still reports
				// a missing parent. Keep this established durability diagnostic.
				assert.ErrorIs(t, err, os.ErrNotExist)
			default:
				if kind == "nondirectory" {
					assert.ErrorContains(t, err, "ancestor is not a directory")
				} else {
					assert.ErrorContains(t, err, "refusing to follow symlink")
				}
			}
			if kind != "regular" {
				assert.Equal(t, before, legacyReflectionTree(t, f.store.repoRoot), "failed/missing cleanup must not mutate evidence or the redirect")
			}
		})
	}
}

func TestRemoveCorrectionIntentSyncFailure(t *testing.T) {
	f := acceptedCorrectionRecovery(t)
	check := preserveReflectionIntentEvidence(t, f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id),
		(audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy))
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	interrupted := errors.New("receipt cleanup directory sync interrupted")
	syncDir = func(dir string) error {
		if dir == filepath.Dir(f.receipt) {
			return interrupted
		}
		return real(dir)
	}
	err := f.store.withMissionLock(f.id, true, func() error {
		return f.store.removeCorrectionIntent(f.id)
	})
	assert.ErrorIs(t, err, interrupted)
	assert.NoFileExists(t, f.receipt, "post-removal sync failure must not pretend removal was rolled back")
	check()
}

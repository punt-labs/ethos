package enable

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestEnableRefusesRetirementWithLegacyProtectedFiles(t *testing.T) {
	for _, tt := range []struct {
		name, pattern, rel string
	}{
		{"private overlay", ".punt-labs/**/*.local.*", ".punt-labs/ethos/identities/alice.ext/service.local.yaml"},
		{"narrow overlay", ".punt-labs/ethos/**/*.local.yaml", ".punt-labs/ethos/identities/alice.ext/service.local.yaml"},
		{"other product credentials", ".punt-labs/**/*.local.*", ".punt-labs/vox/creds.local.json"},
		{"other product local directory", ".punt-labs/**/local/**", ".punt-labs/vox/local/creds.json"},
		{"old lock", missionLockPat, ".punt-labs/ethos/missions/m-1/mission.lock"},
		{"old staging", stagingPat, ".punt-labs/ethos/identities/alice.ethos-tmp1"},
		{"prompt", ".punt-labs/ethos/missions/*/delegations/*/prompt.md", ".punt-labs/ethos/missions/m-1/delegations/d-1/prompt.md"},
		{"reflection", ".punt-labs/ethos/missions/*/reflections.yaml", ".punt-labs/ethos/missions/m-1/reflections.yaml"},
		{"live log", ".punt-labs/ethos/missions/*/log.jsonl", ".punt-labs/ethos/missions/m-1/log.jsonl"},
		{"archived log", ".punt-labs/ethos/missions/*/log-*.jsonl", ".punt-labs/ethos/missions/m-1/log-1.jsonl"},
		{"quarantine", ".punt-labs/ethos/missions/*/log-*.quarantine", ".punt-labs/ethos/missions/m-1/log-1.quarantine"},
		{"corrupt log", ".punt-labs/ethos/missions/*/log-*.jsonl.corrupt*", ".punt-labs/ethos/missions/m-1/log-1.jsonl.corrupt2"},
		{"artifacts directory", ".punt-labs/ethos/missions/*/artifacts/", ".punt-labs/ethos/missions/m-1/artifacts/private.txt"},
		{"artifact glob", ".punt-labs/ethos/missions/*/artifacts/**", ".punt-labs/ethos/missions/m-1/artifacts/nested/private.txt"},
		{"live audit", ".punt-labs/ethos/sessions/*/audit.jsonl", ".punt-labs/ethos/sessions/s-1/audit.jsonl"},
		{"archived audit", ".punt-labs/ethos/sessions/*/audit-*.jsonl", ".punt-labs/ethos/sessions/s-1/audit-1.jsonl"},
		{"audit quarantine", ".punt-labs/ethos/sessions/*/audit-*.quarantine", ".punt-labs/ethos/sessions/s-1/audit-1.quarantine"},
		{"corrupt audit", ".punt-labs/ethos/sessions/*/audit-*.jsonl.corrupt*", ".punt-labs/ethos/sessions/s-1/audit-1.jsonl.corrupt2"},
		{"quoted path", ".punt-labs/**/*.local.*", ".punt-labs/vox/a\n\"b.local.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "quoted path" && runtime.GOOS == "windows" {
				t.Skip("Windows forbids quotes and newlines in filenames")
			}
			root := gitRepo(t)
			initial := legacyGitignoreMarker + "\n" + tt.pattern + "\n"
			ignorePath := filepath.Join(root, ".gitignore")
			require.NoError(t, os.WriteFile(ignorePath, []byte(initial), 0o600))
			private := filepath.Join(root, filepath.FromSlash(tt.rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(private), 0o700))
			require.NoError(t, os.WriteFile(private, []byte("private fixture\n"), 0o600))
			ignored, err := gitExcludes(root, []string{tt.rel})
			require.NoError(t, err)
			require.True(t, ignored)

			_, err = Enable(root)
			require.ErrorContains(t, err, "refusing to retire legacy ignore rules")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", private))
			assert.Equal(t, initial, readFile(t, ignorePath))
			assert.Equal(t, "private fixture\n", readFile(t, private))
			assert.NoFileExists(t, filepath.Join(root, guideRel), "preflight precedes deposit")
			assert.NoFileExists(t, filepath.Join(root, markerRel))
			gitRun(t, root, "add", "-A")
			assert.NotContains(t, gitOutput(t, root, "ls-files", "-z"), tt.rel+"\x00", "ordinary add must still exclude private data")
		})
	}
}

func TestEnableRetirementReportsAllPathsAndKeepsUserProtection(t *testing.T) {
	root := gitRepo(t)
	pattern := ".punt-labs/**/*.local.*\n"
	initial := pattern + legacyGitignoreMarker + "\n" + pattern
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
	for _, rel := range []string{".punt-labs/ethos/identities/alice.ext/service.local.yaml", ".punt-labs/vox/creds.local.json"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("private fixture"), 0o600))
	}
	_, err := Enable(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("%q", filepath.Join(root, ".punt-labs", "ethos", "identities", "alice.ext", "service.local.yaml")))
	assert.Contains(t, err.Error(), fmt.Sprintf("%q", filepath.Join(root, ".punt-labs", "vox", "creds.local.json")))
	assert.Equal(t, initial, readFile(t, filepath.Join(root, ".gitignore")), "do not remove a still-used protection even if duplicated")
}

func TestEnableToPreflightsBothIgnoreHostsBeforeMutation(t *testing.T) {
	for _, owner := range []string{"store", "checkout"} {
		t.Run(owner, func(t *testing.T) {
			store := gitRepo(t)
			gitRun(t, store, "commit", "-qm", "initial", "--allow-empty")
			checkout := filepath.Join(physicalTempDir(t), "linked")
			gitRun(t, store, "worktree", "add", "--detach", checkout)
			initial := legacyGitignoreMarker + "\n.punt-labs/**/*.local.*\n"
			for _, root := range []string{store, checkout} {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
			}
			root := store
			if owner == "checkout" {
				root = checkout
			}
			private := filepath.Join(root, ".punt-labs", "vox", "creds.local.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(private), 0o700))
			require.NoError(t, os.WriteFile(private, []byte("private fixture"), 0o600))
			_, err := EnableTo(checkout, store)
			require.ErrorContains(t, err, fmt.Sprintf("%q", private))
			for _, host := range []string{store, checkout} {
				assert.Equal(t, initial, readFile(t, filepath.Join(host, ".gitignore")))
				assert.NoFileExists(t, filepath.Join(host, guideRel))
			}
		})
	}
}

func TestEnableRetirementAllowsCanonicalLocalFilesAndTrackedDeletion(t *testing.T) {
	root := gitRepo(t)
	rel := ".punt-labs/ethos/identities/alice.ext/service.local.yaml"
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("fixture"), 0o600))
	gitRun(t, root, "add", "--", rel)
	require.NoError(t, os.Remove(path))
	localRel := ".punt-labs/local/ethos/identities/alice.ext/service.local.yaml"
	local := filepath.Join(root, filepath.FromSlash(localRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o700))
	require.NoError(t, os.WriteFile(local, []byte("fixture"), 0o600))
	initial := legacyGitignoreMarker + "\n.punt-labs/**/*.local.*\n.punt-labs/**/local/**\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
	_, err := Enable(root)
	require.NoError(t, err)
	assert.Equal(t, gitignoreMarker+"\n"+LocalIgnoreRule+"\n", readFile(t, filepath.Join(root, ".gitignore")))
	gitRun(t, root, "add", "-A")
	assert.NotContains(t, gitOutput(t, root, "ls-files", "-z"), localRel+"\x00")
}

func TestEnableRetirementRefusesLocalFilesWithNegatedProtection(t *testing.T) {
	root := gitRepo(t)
	initial := LocalIgnoreRule + "\n!.punt-labs/local/\n" + legacyGitignoreMarker + "\n.punt-labs/**/local/**\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
	rel := ".punt-labs/local/ethos/private.local.yaml"
	private := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(private), 0o700))
	require.NoError(t, os.WriteFile(private, []byte("private fixture"), 0o600))
	ignored, err := gitExcludes(root, []string{rel})
	require.NoError(t, err)
	require.True(t, ignored, "the old broad rule currently protects these bytes")
	_, err = Enable(root)
	require.ErrorContains(t, err, fmt.Sprintf("%q", private))
	assert.Equal(t, initial, readFile(t, filepath.Join(root, ".gitignore")))
	gitRun(t, root, "add", "-A")
	assert.NotContains(t, gitOutput(t, root, "ls-files", "-z"), rel+"\x00")
}

func TestEnableRetirementRefusesWhenGitCannotInspect(t *testing.T) {
	root := gitRepo(t)
	initial := legacyGitignoreMarker + "\n.punt-labs/**/*.local.*\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	testhelpers.ShimGit(t, "ls-files")
	_, _, err = ensureGitignore(root, p)
	require.ErrorContains(t, err, "checking legacy ignore protections")
	assert.Equal(t, initial, readFile(t, filepath.Join(root, ".gitignore")))
}

func TestEnableRetirementRefusesUnreadableProtectedDirectory(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix permission enforcement as a non-root user")
	}
	root := gitRepo(t)
	initial := legacyGitignoreMarker + "\n.punt-labs/**/*.local.*\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
	dir := filepath.Join(root, ".punt-labs", "private")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "service.local.yaml"), []byte("private fixture"), 0o600))
	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	_, err := Enable(root)
	require.ErrorContains(t, err, "checking legacy ignore protections")
	assert.Equal(t, initial, readFile(t, filepath.Join(root, ".gitignore")))
}

func TestEnableKeepsLocalDescendantSymlinkRefusal(t *testing.T) {
	root := gitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o700))
	if err := os.Symlink(physicalTempDir(t), filepath.Join(root, ".punt-labs", "local")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := Enable(root)
	require.ErrorContains(t, err, "symlink")
	assert.NoFileExists(t, filepath.Join(root, ".gitignore"))
}

func TestPhysicalTemporaryRootAliasForDirectStaging(t *testing.T) {
	root := gitRepo(t)
	alias := filepath.Join(physicalTempDir(t), "root-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p, err := layout.NewRepository(alias, alias, layout.RecordBlame)
	require.NoError(t, err)
	_, err = createVendoredTemp(filepath.Join(alias, guideRel), p)
	require.ErrorContains(t, err, "symlink", "unresolved explicit roots must still refuse")
	physical, err := filepath.EvalSymlinks(alias)
	require.NoError(t, err)
	_, err = Enable(physical)
	require.NoError(t, err)
	_, err = Disable(physical, false)
	require.NoError(t, err)
	assert.False(t, strings.Contains(readFile(t, filepath.Join(root, ".gitignore")), ".local.*"))
}

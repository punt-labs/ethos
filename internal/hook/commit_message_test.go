package hook

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const messageMission = "m-2026-11-04-043"
const messageDelegation = "d-2026-11-04-043"

func messagePolicy(t *testing.T, mode layout.RecordMode) layout.Policy {
	t.Helper()
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), mode)
	require.NoError(t, err)
	return p
}

func TestCommitMessageTrailers(t *testing.T) {
	const mixedParagraph = "subject\n\nThis paragraph documents an example.\nMission: m-2026-11-04-001\nThe value above is quoted documentation.\nThis line continues the explanation.\nAnother ordinary sentence follows.\nThe paragraph ends here.\n"
	const commentFooter = "\n# Please enter the commit message for your changes.\n# On branch main\n"
	for _, tc := range []struct {
		name, original, mission, delegation, want string
	}{
		{"both", "subject\n", messageMission, messageDelegation, "subject\n\nMission: " + messageMission + "\nDelegation: " + messageDelegation + "\n"},
		{"no final newline still creates a trailer block", "subject", messageMission, "", "subject\n\nMission: " + messageMission + "\n"},
		{"delegation alone", "subject\n", "", messageDelegation, "subject\n\nDelegation: " + messageDelegation + "\n"},
		{"no IDs no normalization", "subject\r\n\n", "", "", "subject\r\n\n"},
		{"quoted body is not a trailer", "subject\n\nMission: quoted\n\nlast body paragraph\n", messageMission, "", "subject\n\nMission: quoted\n\nlast body paragraph\n\nMission: " + messageMission + "\n"},
		{"mixed final paragraph is not a trailer block", mixedParagraph, messageMission, "", mixedParagraph + "\nMission: " + messageMission + "\n"},
		{"new trailer before comment footer", "subject\n" + commentFooter, messageMission, "", "subject\n\nMission: " + messageMission + "\n" + commentFooter},
		{"existing trailer before comment footer retained exactly", "subject\n\nMission: m-2026-11-04-001\n" + commentFooter, messageMission, "", "subject\n\nMission: m-2026-11-04-001\n" + commentFooter},
		{"existing differently cased trailer retained exactly", "subject\n\nmission:m-2026-11-04-001\n", messageMission, "", "subject\n\nmission:m-2026-11-04-001\n"},
		{"existing different mission retained", "subject\n\nMission: m-2026-01-01-001\n", messageMission, messageDelegation, "subject\n\nMission: m-2026-01-01-001\nDelegation: " + messageDelegation + "\n"},
		{"existing CRLF retained exactly", "subject\r\n\r\nMission: " + messageMission + "\r\n\r\n", messageMission, "", "subject\r\n\r\nMission: " + messageMission + "\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := messagePolicy(t, layout.RecordBlame)
			path := filepath.Join(filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), "MSG")
			require.NoError(t, os.WriteFile(path, []byte(tc.original), 0o640))
			for range 2 {
				require.NoError(t, ApplyCommitMessage(p, path, tc.mission, tc.delegation))
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, tc.want, string(data), "must be byte-idempotent")
			}
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
		})
	}
}

func TestCommitMessageConfiguredTrailerSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, config, original, delegation, want string
	}{
		{"separator", "[trailer]\n\tseparators = %\n", "subject\n\nMission% old\n", messageDelegation, "subject\n\nMission% old\nDelegation% " + messageDelegation + "\n"},
		{"key alias", "[trailer \"Mission\"]\n\tkey = Mission-Ref\n", "subject\n\nMission-Ref: old\n", messageDelegation, "subject\n\nMission-Ref: old\nDelegation: " + messageDelegation + "\n"},
		{"configured replacement cannot replace existing ID", "[trailer]\n\tifExists = replace\n", "subject\n\nMission: old\n", "", "subject\n\nMission: old\n"},
		{"configured refusal cannot suppress missing ID", "[trailer]\n\tifMissing = doNothing\n", "subject\n", "", "subject\n\nMission: " + messageMission + "\n"},
		{"comment character", "[core]\n\tcommentChar = \";\"\n", "subject\n\n; Git comment footer\n", "", "subject\n\nMission: " + messageMission + "\n\n; Git comment footer\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "gitconfig")
			require.NoError(t, os.WriteFile(config, []byte(tc.config), 0o600))
			t.Setenv("GIT_CONFIG_GLOBAL", config)
			p := messagePolicy(t, layout.RecordBlame)
			path := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(path, []byte(tc.original), 0o600))
			for range 2 {
				require.NoError(t, ApplyCommitMessage(p, path, messageMission, tc.delegation))
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, tc.want, string(data), "parser and renderer must agree and preserve existing IDs")
			}
		})
	}
}

func TestCommitMessageInstalledButDirectorySyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory sync is a documented no-op")
	}
	p := messagePolicy(t, layout.RecordBlame)
	dir := filepath.Join(t.TempDir(), "message-dir")
	moved := dir + "-moved"
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "MSG")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
	installed := false
	rename := func(stage, dest string) error {
		if err := os.Rename(stage, dest); err != nil {
			return err
		}
		installed = true
		// Force the existing directory-sync function's open to fail after the
		// real commit point, without a mutable global seam or privilege checks.
		return os.Rename(dir, moved)
	}
	err := applyCommitMessage(p, path, messageMission, "", rename)
	require.True(t, installed, "the message rename reached its commit point")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.ErrorContains(t, err, "commit message installed, but syncing its directory failed")
	data, err := os.ReadFile(filepath.Join(moved, "MSG"))
	require.NoError(t, err)
	assert.Equal(t, "original\n\nMission: "+messageMission+"\n", string(data), "installed bytes are not rolled back or reported unchanged")
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries, "no stage remains after the committed rename")
}

func TestCommitMessageStagingAndRenameFailure(t *testing.T) {
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		for _, failure := range []error{nil, syscall.EXDEV, os.ErrPermission} {
			name := "success"
			if failure != nil {
				name = failure.Error()
			}
			t.Run(string(rune('0'+mode))+"/"+name, func(t *testing.T) {
				p := messagePolicy(t, mode)
				checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
				path := filepath.Join(checkout, "MSG")
				require.NoError(t, os.MkdirAll(p.RecordRoot(), 0o700))
				t.Setenv("TMPDIR", p.RecordRoot())
				require.NoError(t, os.WriteFile(path, []byte("old bytes\n"), 0o600))
				called := false
				rename := func(stage, dest string) error {
					called = true
					assert.Equal(t, p.TempDir(), filepath.Dir(stage), "observe transient stage before rename")
					assert.Contains(t, filepath.Base(stage), layout.TmpMarker)
					assert.Equal(t, path, dest)
					data, err := os.ReadFile(stage)
					require.NoError(t, err)
					assert.Equal(t, "old bytes\n\nMission: "+messageMission+"\nDelegation: "+messageDelegation+"\n", string(data))
					old, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, "old bytes\n", string(old), "not touched before the commit point")
					entries, err := os.ReadDir(p.RecordRoot())
					require.NoError(t, err)
					assert.Empty(t, entries, "no transient file in caller TMPDIR")
					entries, err = os.ReadDir(checkout)
					require.NoError(t, err)
					require.Len(t, entries, 1, "no adjacent stage, even transiently")
					if failure != nil {
						return failure
					}
					return os.Rename(stage, dest)
				}
				err := applyCommitMessage(p, path, messageMission, messageDelegation, rename)
				assert.True(t, called)
				if failure != nil {
					require.ErrorIs(t, err, failure)
					old, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, "old bytes\n", string(old))
				} else {
					require.NoError(t, err)
				}
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "staging is removed on either outcome")
				assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			})
		}
	}
}

func TestCommitMessageRefusesInvalidInputAndUnusableStage(t *testing.T) {
	for _, tc := range []struct{ name, mission, delegation string }{
		{"newline injection", messageMission + "\nInjected: yes", ""},
		{"path injection", "../../mission", ""},
		{"invalid delegation", "", "--trailer=Injected: yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := messagePolicy(t, layout.RecordBlame)
			path := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
			err := ApplyCommitMessage(p, path, tc.mission, tc.delegation)
			var invalid *layout.ValidationError
			require.ErrorAs(t, err, &invalid)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "original\n", string(data))
			assert.NoDirExists(t, p.LocalRoot())
		})
	}
	p := messagePolicy(t, layout.RecordBlame)
	path := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
	assert.Error(t, ApplyCommitMessage(layout.Policy{}, path, messageMission, ""))
	assert.Error(t, ApplyCommitMessage(p, "", messageMission, ""))
	assert.Error(t, ApplyCommitMessage(p, filepath.Dir(path), messageMission, ""))
	require.NoError(t, layout.EnsureLocalDir(p, p.LocalRoot()))
	require.NoError(t, os.Symlink(t.TempDir(), p.TempDir()))
	err := ApplyCommitMessage(p, path, messageMission, "")
	require.ErrorContains(t, err, "symlink")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(data), "failed staging must not trigger append")
}

func TestCommitMessageGitFailurePreservesOriginal(t *testing.T) {
	p := messagePolicy(t, layout.RecordBlame)
	path := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf 'fixture render failure' >&2\nexit 17\n"), 0o700))
	t.Setenv("PATH", bin)
	err := ApplyCommitMessage(p, path, messageMission, messageDelegation)
	require.ErrorContains(t, err, "fixture render failure")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(data))
	assert.NoDirExists(t, p.TempDir(), "render every trailer before staging")
}

func TestCommitMessageGitParserFailurePreservesOriginal(t *testing.T) {
	p := messagePolicy(t, layout.RecordBlame)
	path := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	wrapper := `#!/bin/sh
if [ "$1 $2" = 'interpret-trailers --parse' ]; then
  echo 'fixture parser failure' >&2
  exit 17
fi
exec "$REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PATH", bin)
	err = ApplyCommitMessage(p, path, messageMission, "")
	require.ErrorContains(t, err, "fixture parser failure")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(data))
	assert.NoDirExists(t, p.TempDir(), "a successful render cannot bypass a failed parser")
}

func TestCommitMessageConcurrentCallsAndSymlinkAlias(t *testing.T) {
	p := messagePolicy(t, layout.RecordBlame)
	path := filepath.Join(t.TempDir(), "MSG")
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0o600))
	require.NoError(t, os.Symlink(path, alias))
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			if i%2 == 0 {
				errs <- ApplyCommitMessage(p, alias, messageMission, "")
			} else {
				errs <- ApplyCommitMessage(p, path, "", messageDelegation)
			}
		})
	}
	wg.Wait()
	close(errs)
	var all error
	for err := range errs {
		all = errors.Join(all, err)
	}
	require.NoError(t, all)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "Mission: "))
	assert.Equal(t, 1, strings.Count(string(data), "Delegation: "))
	info, err := os.Lstat(alias)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
}

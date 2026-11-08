package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestLocatedLayoutUsesOnlyItsSnapshot(t *testing.T) {
	store, checkout := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(store, ".punt-labs"), 0o700))
	path := filepath.Join(store, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.WriteFile(path, []byte("record: full\n"), 0o600))
	cfg, err := ConfigOf(store)
	require.NoError(t, err)
	loc := Located{Store: store, Checkout: checkout, Config: cfg}

	// Invalid on-disk config and an unusable process override cannot affect an
	// operation whose roots and configuration have already been loaded.
	require.NoError(t, os.WriteFile(path, []byte("record: invalid\n"), 0o600))
	t.Setenv("ETHOS_REPO_ROOT", filepath.Join(store, "nonexistent"))
	p, err := loc.Layout()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "trace"), p.TraceArchiveRoot())
	assert.Equal(t, filepath.Join(store, ".punt-labs", "ethos"), p.RecordRoot())
	assert.Equal(t, filepath.Join(checkout, ".punt-labs", "ethos"), p.CheckoutRecordRoot())

	// The value owns its mode, not a pointer into the caller's mutable config.
	cfg.Record = RecordBlame
	assert.Equal(t, layout.RecordFull, p.RecordMode())
	q, err := loc.Layout()
	require.NoError(t, err)
	assert.Equal(t, layout.RecordBlame, q.RecordMode())
	assert.NotEqual(t, p, q, "MCP callers can compare policy snapshots")
	assert.Equal(t, p.TraceArchiveRoot(), q.TraceArchiveRoot(), "mode changes do not relocate the retained archive")
	assert.Equal(t, p.RecordRoot(), q.RecordRoot())
	assert.Equal(t, p.CheckoutRecordRoot(), q.CheckoutRecordRoot())

	loc.Config = nil
	q, err = loc.Layout()
	require.NoError(t, err)
	assert.Equal(t, layout.RecordBlame, q.RecordMode(), "absent config defaults without rereading disk")
	loc.Config = &RepoConfig{Record: "invalid"}
	_, err = loc.Layout()
	assert.ErrorContains(t, err, "record")
}

func TestLocatedLayoutPreservesErrorsAndAbsence(t *testing.T) {
	root := t.TempDir()
	broken := errors.New("location or configuration failure")
	for _, loc := range []Located{
		{StoreErr: broken}, {EthosErr: broken}, {CheckoutErr: broken}, {ConfigErr: broken},
	} {
		p, err := loc.Layout()
		assert.Same(t, broken, err)
		assert.Equal(t, layout.Policy{}, p)
	}
	ce := &ConfigError{Err: broken, What: "configuration failed"}
	_, err := (Located{Store: root, Checkout: root, ConfigErr: ce}).Layout()
	assert.Same(t, ce, err)
	assert.ErrorIs(t, err, broken)
	_, err = (Located{}).Layout()
	assert.ErrorIs(t, err, ErrNotInRepo)
	_, err = (Located{Store: root}).Layout()
	assert.Error(t, err, "absent checkout cannot produce a relative publication path")

	// Ethos being absent means a fresh repo, not global scope.
	p, err := (Located{Store: root, Checkout: root}).Layout()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".punt-labs", "ethos"), p.RecordRoot())
}

func TestRootsLayoutForLinkedCheckout(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	main, checkout := filepath.Join(base, "main"), filepath.Join(base, "linked")
	initRepo(t, main)
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".punt-labs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(main, ".punt-labs", "ethos.yaml"), []byte("record: full\n"), 0o600))
	gitRun(t, "-C", main, "worktree", "add", "-q", checkout)
	// A conflicting checkout config is deliberately not authoritative.
	require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".punt-labs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".punt-labs", "ethos.yaml"), []byte("record: blame\n"), 0o600))
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Chdir(checkout)
	loc := Roots()
	require.NoError(t, loc.Err())
	p, err := loc.Layout()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(realpath(t, main), ".punt-labs", "ethos"), p.RecordRoot())
	assert.Equal(t, filepath.Join(realpath(t, checkout), ".punt-labs", "ethos"), p.CheckoutRecordRoot())
	assert.Equal(t, filepath.Join(realpath(t, main), ".punt-labs", "local", "ethos"), p.LocalRoot())
	assert.Equal(t, filepath.Join(realpath(t, main), ".punt-labs", "local", "ethos", "trace"), p.TraceArchiveRoot())
	assert.Equal(t, layout.RecordFull, p.RecordMode())
}

func TestRootsLayoutRejectsInvalidMode(t *testing.T) {
	root := t.TempDir()
	testhelpersGitDir(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: typo\n"), 0o600))
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Chdir(root)
	loc := Roots()
	require.ErrorContains(t, loc.ConfigErr, "record")
	_, err := loc.Layout()
	assert.Same(t, loc.ConfigErr, err)
}

func TestRootsLayoutCreatesLocalTempThroughRootAliases(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		t.Run(map[bool]string{true: "git", false: "no git"}[withGit], func(t *testing.T) {
			if withGit {
				requireGit(t)
			} else {
				hideGit(t)
			}
			for _, tt := range []struct {
				name             string
				cwd              string // relative to the fixture base
				override         string
				absoluteOverride bool   // prefix override with the fixture base
				aliasTarget      string // relative target for base/alias
				unixOnly         bool
			}{
				{name: "temp root", cwd: "repo"},
				{name: "alias override", cwd: "repo", override: "alias", absoluteOverride: true, aliasTarget: "repo"},
				{name: "relative override", cwd: "repo", override: "."},
				{name: "relative parent override", cwd: "repo/sub", override: ".."},
				{name: "symlink cwd", cwd: "alias/sub", aliasTarget: "repo"},
				{name: "symlink cwd relative parent override", cwd: "alias", override: "..", aliasTarget: "repo/sub", unixOnly: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					if tt.unixOnly && runtime.GOOS == "windows" {
						t.Skip("requires Unix symlink/.. path semantics")
					}
					base := t.TempDir()
					root := filepath.Join(base, "repo")
					testhelpersGitDir(t, root)
					require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o700))
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: full\n"), 0o600))
					if tt.aliasTarget != "" {
						if err := os.Symlink(filepath.FromSlash(tt.aliasTarget), filepath.Join(base, "alias")); err != nil {
							t.Skipf("symlinks unavailable: %v", err)
						}
					}
					cwd := filepath.Join(base, filepath.FromSlash(tt.cwd))
					override := filepath.FromSlash(tt.override)
					if tt.absoluteOverride {
						// Preserve alias/.. instead of cleaning it with filepath.Join.
						override = base + string(filepath.Separator) + override
					}
					t.Chdir(cwd)
					if runtime.GOOS != "windows" {
						// os.Getwd honors an equivalent PWD spelling on Unix. Keep
						// the logical alias, including macOS /var, as locator input.
						t.Setenv("PWD", cwd)
						got, err := os.Getwd()
						require.NoError(t, err)
						require.Equal(t, cwd, got)
					}
					t.Setenv("ETHOS_REPO_ROOT", override)
					loc := Roots()
					require.NoError(t, loc.Err())
					physical := realpath(t, root)
					assert.Equal(t, physical, loc.Store)
					assert.Equal(t, physical, loc.Checkout)
					assert.Equal(t, filepath.Join(physical, ".punt-labs", "ethos"), loc.Ethos)
					require.NotNil(t, loc.Config)
					assert.Equal(t, RecordFull, loc.Config.Record)
					p, err := loc.Layout()
					require.NoError(t, err)
					assert.Equal(t, loc.Ethos, p.RecordRoot(), "store callers and policy must share the same spelling")
					f, err := layout.CreateLocalTemp(p, "alias", ".yaml")
					require.NoError(t, err)
					assert.Equal(t, filepath.Join(physical, ".punt-labs", "local", "ethos", "tmp"), filepath.Dir(f.Name()))
					require.NoError(t, f.Close())
					require.NoError(t, os.Remove(f.Name()))
				})
			}
		})
	}
}

func TestRootsLayoutRefusesLocalSymlinkDescendants(t *testing.T) {
	for _, descendant := range []string{".punt-labs", ".punt-labs/local", ".punt-labs/local/ethos", ".punt-labs/local/ethos/tmp"} {
		t.Run(descendant, func(t *testing.T) {
			base := t.TempDir()
			root, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
			testhelpersGitDir(t, root)
			require.NoError(t, os.Mkdir(outside, 0o700))
			link := filepath.Join(root, filepath.FromSlash(descendant))
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Setenv("ETHOS_REPO_ROOT", "")
			t.Chdir(root)
			loc := Roots()
			require.NoError(t, loc.Err())
			p, err := loc.Layout()
			require.NoError(t, err)
			f, err := layout.CreateLocalTemp(p, "refused", ".yaml")
			assert.Nil(t, f)
			assert.ErrorContains(t, err, "symlink")
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries, "refusal must not create anything through the link")
		})
	}
}

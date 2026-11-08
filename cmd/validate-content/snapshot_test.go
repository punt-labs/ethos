package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSnapshot_ConfigReplacement(t *testing.T) {
	for _, replacement := range []string{"resolution: layered\nactive_bundle: missing\nrecord: full\n", "resolution: [invalid\n"} {
		t.Run(replacement, func(t *testing.T) {
			repo, ethos := minimalRepo(t, "repo-only")
			writeRepoConfig(t, repo, "repo-only", "ops")
			bundleRoot := filepath.Join(repo, ".punt-labs", "ethos-bundles", "ops")
			require.NoError(t, attribute.NewStore(bundleRoot, attribute.Talents).Save(&attribute.Attribute{
				Slug: "piloting", Content: "# Piloting\n\nFollow the captured bundle.\n",
			}))
			writeIdentityYAML(t, ethos, "alice", "Alice", []string{"piloting"})
			loc, err := contentTarget(ethos)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte(replacement), 0o600))
			rep, err := runFor(loc, globalWithInvalidIdentity(t))
			require.NoError(t, err)
			for _, r := range rep.results {
				assert.True(t, r.pass, "%s: %s", r.label, r.detail)
				assert.NotContains(t, r.label, "not applicable", "completeness must still check repo-only")
			}
			assert.Equal(t, 1, rep.nIdentities, "the global layer must not enter the captured repo-only policy")
		})
	}
}

func TestRunSnapshot_CheckoutEmbedSync(t *testing.T) {
	for _, tc := range []struct {
		name          string
		driftCheckout bool
	}{
		{name: "checkout matches and store drifts"},
		{name: "checkout drifts and store matches", driftCheckout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ethos := minimalRepo(t, "repo-only")
			checkout, checkoutEthos := minimalRepo(t, "layered")
			writeIdentityYAML(t, checkoutEthos, "checkout-only", "Checkout", []string{"missing-talent"})
			loc, err := contentTarget(ethos)
			require.NoError(t, err)
			loc.Checkout = checkout
			assert.NotEqual(t, loc.Store, loc.Checkout)

			driftRoot := store
			if tc.driftCheckout {
				driftRoot = checkout
			}
			for _, rel := range []string{"docs/ETHOS-SETUP.md", ".punt-labs/ethos/CLAUDE.md"} {
				require.NoError(t, os.WriteFile(filepath.Join(driftRoot, rel), []byte("different factory content\n"), 0o600))
			}

			// Neither a new cwd/override nor replacement config may retarget the
			// captured checkout or change the store's repo-only content selection.
			for _, root := range []string{store, checkout} {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("resolution: [invalid\n"), 0o600))
			}
			other := t.TempDir()
			t.Setenv("ETHOS_REPO_ROOT", other)
			t.Chdir(other)
			rep, err := runFor(loc, globalWithInvalidIdentity(t))
			require.NoError(t, err)
			assert.Equal(t, 1, rep.nIdentities, "identity content must still come from the captured store, not checkout or global")
			for _, label := range []string{"enable: ETHOS-SETUP.md embed sync", "enable: CLAUDE.md guide embed sync"} {
				r := findResult(rep.results, label)
				require.NotNil(t, r)
				assert.Equal(t, !tc.driftCheckout, r.pass, "%s: %s", r.label, r.detail)
				if tc.driftCheckout {
					assert.Contains(t, r.detail, "have drifted")
				}
			}
			for _, r := range rep.results {
				if !strings.HasPrefix(r.label, "enable:") {
					assert.True(t, r.pass, "%s: %s", r.label, r.detail)
				}
			}
		})
	}
}

func TestValidateContent_ExplicitTargetEmbedSync(t *testing.T) {
	bin := buildValidateContent(t)
	for _, driftTarget := range []bool{false, true} {
		name := "target matches and invoking checkout drifts"
		if driftTarget {
			name = "target drifts and invoking checkout matches"
		}
		t.Run(name, func(t *testing.T) {
			cwd, _ := minimalRepo(t, "layered")
			target, ethos := minimalRepo(t, "repo-only")
			driftRoot := cwd
			if driftTarget {
				driftRoot = target
			}
			for _, rel := range []string{"docs/ETHOS-SETUP.md", ".punt-labs/ethos/CLAUDE.md"} {
				require.NoError(t, os.WriteFile(filepath.Join(driftRoot, rel), []byte("different factory content\n"), 0o600))
			}
			t.Setenv("ETHOS_REPO_ROOT", cwd)
			out, err := runValidateContent(t, bin, cwd, ethos, globalWithInvalidIdentity(t))
			if driftTarget {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, out)
				assert.Equal(t, 1, exitErr.ExitCode(), out)
				assert.Contains(t, out, "FAIL  enable: ETHOS-SETUP.md embed sync")
				assert.Contains(t, out, "FAIL  enable: CLAUDE.md guide embed sync")
				assert.Contains(t, out, "2 failure(s)")
			} else {
				require.NoError(t, err, out)
				assert.Contains(t, out, "all checks passed")
			}
			assert.NotContains(t, out, "ghost")
		})
	}
}

func TestValidateContent_ExplicitTargetIgnoresInvokingConfig(t *testing.T) {
	bin := buildValidateContent(t)
	cwd, _ := minimalRepo(t, "layered")
	require.NoError(t, os.WriteFile(filepath.Join(cwd, ".punt-labs", "ethos.yaml"), []byte("record: [invalid\n"), 0o600))
	_, ethos := minimalRepo(t, "repo-only")
	t.Setenv("ETHOS_REPO_ROOT", cwd)
	out, err := runValidateContent(t, bin, cwd, ethos, globalWithInvalidIdentity(t))
	require.NoError(t, err, out)
	assert.Contains(t, out, "all checks passed")
	assert.NotContains(t, out, "ghost")
}

func TestRunSnapshot_CompletenessCannotBecomeLayered(t *testing.T) {
	repo, ethos := minimalRepo(t, "repo-only")
	writeIdentityYAML(t, ethos, "alice", "Alice", []string{"missing-talent"})
	loc, err := contentTarget(ethos)
	require.NoError(t, err)
	writeRepoConfig(t, repo, "layered", "")
	rep, err := runFor(loc, t.TempDir())
	require.NoError(t, err)
	complete := findResult(rep.results, "Repo-only completeness")
	require.NotNil(t, complete)
	assert.False(t, complete.pass, "replacement layered config must not bypass the captured completeness gate")
	assert.Contains(t, complete.detail, "missing-talent")
}

func TestValidateContent_ExplicitTargetRejectsTargetConfig(t *testing.T) {
	bin := buildValidateContent(t)
	cwd, _ := minimalRepo(t, "repo-only")
	target, ethos := minimalRepo(t, "layered")
	require.NoError(t, os.WriteFile(filepath.Join(target, ".punt-labs", "ethos.yaml"), []byte("record: [invalid\n"), 0o600))
	t.Setenv("ETHOS_REPO_ROOT", cwd)
	out, err := runValidateContent(t, bin, cwd, ethos, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, out, "selected content configuration")
}

func TestRunSnapshot_TargetAliasDoesNotRetarget(t *testing.T) {
	repo, _ := minimalRepo(t, "repo-only")
	otherRepo, _ := minimalRepo(t, "layered")
	alias := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(repo, alias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	loc, err := contentTarget(filepath.Join(alias, ".punt-labs", "ethos"))
	require.NoError(t, err)
	physicalRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, physicalRepo, loc.Store)
	require.NoError(t, os.Remove(alias))
	require.NoError(t, os.Symlink(otherRepo, alias))
	require.NoError(t, os.WriteFile(filepath.Join(otherRepo, ".punt-labs", "ethos.yaml"), []byte("record: [invalid\n"), 0o600))
	rep, err := runFor(loc, globalWithInvalidIdentity(t))
	require.NoError(t, err)
	for _, r := range rep.results {
		assert.True(t, r.pass, "%s: %s", r.label, r.detail)
	}
}

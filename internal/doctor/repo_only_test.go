package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/vendor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoOnlySet builds a repo whose .punt-labs/ethos/ holds a complete,
// self-standing identity set and whose config selects repo-only.
func repoOnlySet(t *testing.T, resolution string) (repoRoot, ethosRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	ethosRoot = filepath.Join(repoRoot, ".punt-labs", "ethos")

	write := func(rel, body string) {
		p := filepath.Join(ethosRoot, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("identities/bwk.yaml",
		"name: Brian K\nhandle: bwk\nkind: agent\npersonality: kernighan\n")
	write("personalities/kernighan.md", "# Kernighan\n\nSimplicity.\n")
	write("roles/go-specialist.yaml", "name: go-specialist\nresponsibilities:\n  - Go\n")
	write("teams/engineering.yaml",
		"name: engineering\nmembers:\n  - identity: bwk\n    role: go-specialist\n")
	write("identities/bwk.ext/quarry.yaml", "memory_collection: bwk-mem\n")
	write(filepath.Join("..", "ethos.yaml"), "resolution: "+resolution+"\n")

	require.NoError(t, os.WriteFile(vendor.ManifestPath(ethosRoot), []byte(
		"version: 1\nseeds: [bwk]\nidentities:\n  - handle: bwk\n    ext:\n      - quarry.yaml\n"),
		0o644))
	return repoRoot, ethosRoot
}

func TestCheckRepoSetComplete(t *testing.T) {
	t.Run("complete set passes", func(t *testing.T) {
		repoRoot, _ := repoOnlySet(t, "repo-only")
		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "PASS", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "1 identities")
	})

	// Layered mode is not held to this bar: the global fallback is
	// expected to catch the tail, so an incomplete repo layer is normal.
	t.Run("layered is not applicable", func(t *testing.T) {
		repoRoot, ethosRoot := repoOnlySet(t, "layered")
		require.NoError(t, os.Remove(filepath.Join(ethosRoot, "personalities", "kernighan.md")))
		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "PASS", r.Status)
		assert.Contains(t, r.Detail, "not applicable")
	})

	t.Run("missing attribute fails and names it", func(t *testing.T) {
		repoRoot, ethosRoot := repoOnlySet(t, "repo-only")
		require.NoError(t, os.Remove(filepath.Join(ethosRoot, "personalities", "kernighan.md")))

		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "personalities/kernighan")
		assert.Contains(t, r.Detail, "ethos vendor --apply")
	})

	// The manifest is what makes an omitted extension detectable at all.
	t.Run("missing manifest-recorded extension fails", func(t *testing.T) {
		repoRoot, ethosRoot := repoOnlySet(t, "repo-only")
		require.NoError(t, os.Remove(filepath.Join(ethosRoot, "identities", "bwk.ext", "quarry.yaml")))

		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "ext/bwk/quarry")
	})

	// A hand-authored set is legal, but the limit must be visible: a PASS
	// would imply a guarantee about extensions that doctor cannot make.
	t.Run("no manifest warns that ext is unverifiable", func(t *testing.T) {
		repoRoot, ethosRoot := repoOnlySet(t, "repo-only")
		require.NoError(t, os.Remove(vendor.ManifestPath(ethosRoot)))

		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "unverifiable")
	})

	t.Run("repo-only without a store fails", func(t *testing.T) {
		repoRoot, ethosRoot := repoOnlySet(t, "repo-only")
		require.NoError(t, os.RemoveAll(ethosRoot))

		r := CheckRepoSetComplete(repoRoot)
		assert.Equal(t, "FAIL", r.Status)
	})

	t.Run("no repo passes", func(t *testing.T) {
		assert.Equal(t, "PASS", CheckRepoSetComplete("").Status)
	})
}

// gitRepo initializes a real repo: the check asks git, so a fixture that
// fakes .gitignore parsing would not exercise what ships.
func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", root},
		{"-C", root, "config", "user.email", "t@example.com"},
		{"-C", root, "config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return root
}

func writeLocalExt(t *testing.T, root string) string {
	t.Helper()
	rel := filepath.Join(".punt-labs", "ethos", "identities", "bwk.ext", "quarry.local.yaml")
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("api_token: s3cret\n"), 0o644))
	return rel
}

func TestCheckLocalExtNotTracked(t *testing.T) {
	t.Run("ignored and untracked passes", func(t *testing.T) {
		root := gitRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
			[]byte(GitignoreRule+"\n"), 0o644))
		writeLocalExt(t, root)

		r := CheckLocalExtNotTracked(root)
		assert.Equal(t, "PASS", r.Status, r.Detail)
	})

	// Nothing is exposed yet, but the next `ext set --local` would be.
	t.Run("missing rule warns", func(t *testing.T) {
		root := gitRepo(t)
		writeLocalExt(t, root)

		r := CheckLocalExtNotTracked(root)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, GitignoreRule)
	})

	// The case the rule cannot fix: .gitignore does NOT untrack a file
	// already in the index, so adding the rule would leave the secret
	// committed and the repo looking clean.
	t.Run("already tracked fails with the untrack command", func(t *testing.T) {
		root := gitRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
			[]byte(GitignoreRule+"\n"), 0o644))
		rel := writeLocalExt(t, root)

		add := exec.Command("git", "-C", root, "add", "-f", rel)
		out, err := add.CombinedOutput()
		require.NoError(t, err, string(out))

		r := CheckLocalExtNotTracked(root)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "quarry.local.yaml")
		assert.Contains(t, r.Detail, "git rm --cached")
	})

	t.Run("no repo passes", func(t *testing.T) {
		assert.Equal(t, "PASS", CheckLocalExtNotTracked("").Status)
	})

	// A git failure means the check did not run. PASS would report that
	// tracking was verified when nothing was — a false all-clear on
	// secret-bearing files (Copilot, PR #410).
	// A directory git cannot even enter. (A temp dir would not do: `git -C`
	// walks up, and TMPDIR here sits inside a real repo, so git would
	// happily answer about the enclosing checkout.)
	t.Run("unverifiable warns rather than passing", func(t *testing.T) {
		r := CheckLocalExtNotTracked(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "could not verify")
	})
}

func TestCheckExtCredentialNames(t *testing.T) {
	newStore := func(t *testing.T) *identity.Store {
		t.Helper()
		s := identity.NewStore(t.TempDir())
		require.NoError(t, s.Save(&identity.Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
		return s
	}

	t.Run("clean keys pass", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.ExtSet("mal", "quarry", "memory_collection", "mem"))
		require.NoError(t, s.ExtSet("mal", "beadle", "gpg_key_id", "ABC"))

		r := CheckExtCredentialNames(s)
		assert.Equal(t, "PASS", r.Status, r.Detail)
	})

	// Advisory, not fatal: doctor is a health check, and a repo whose
	// extensions predate the lint should be told, not stopped. Vendor is
	// where it blocks, because vendor is what writes into git.
	t.Run("credential-named keys warn", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.ExtSet("mal", "quarry", "api_token", "s3cret"))

		r := CheckExtCredentialNames(s)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "mal quarry/api_token")
		assert.Contains(t, r.Detail, "--local")
	})

	// Following the check's own advice must clear it. Reading the merged
	// view would keep flagging the key after the user moved it to
	// .local — advice that can never be satisfied is noise.
	t.Run("moving the key to .local clears the warning", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.ExtSet("mal", "quarry", "api_token", "s3cret", identity.Local(true)))
		require.NoError(t, s.ExtSet("mal", "quarry", "memory_collection", "mem"))

		// The merged view still holds the credential-named key...
		merged, err := s.ExtGet("mal", "quarry", "")
		require.NoError(t, err)
		require.Contains(t, merged, "api_token")

		// ...but only the base file is git-bound, so the check is quiet.
		r := CheckExtCredentialNames(s)
		assert.Equal(t, "PASS", r.Status, r.Detail)
	})

	t.Run("nil store passes", func(t *testing.T) {
		assert.Equal(t, "PASS", CheckExtCredentialNames(nil).Status)
	})
}

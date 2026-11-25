package behavioral

import (
	"crypto/sha256"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type ignoredFile struct {
	mode os.FileMode
	hash [sha256.Size]byte
}

var ethosRuntimeFile = regexp.MustCompile(`^\.punt-labs/local/ethos/(layout\.yaml|locks/[a-z][a-z0-9-]*/[a-f0-9]{64}\.lock|sessions/[^/]+\.audit\.jsonl|missions/[^/]+/[^/]+\.log\.jsonl)$`)

func (f *Fixture) snapshotIgnoredFiles(t *testing.T) map[string]ignoredFile {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	cmd.Dir, cmd.Env = f.Repo, f.Env
	out, err := cmd.Output()
	require.NoError(t, err, "listing ignored files")

	snapshot := make(map[string]ignoredFile)
	for name := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(f.Repo, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		require.NoError(t, err, "checking ignored path %q", name)
		mode := info.Mode()
		if mode.IsRegular() && ethosRuntimeFile.MatchString(name) {
			continue
		}
		var content []byte
		switch {
		case mode.IsRegular():
			content, err = os.ReadFile(path)
		case mode&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(path)
			content = []byte(target)
		}
		require.NoError(t, err, "reading ignored path %q", name)
		snapshot[name] = ignoredFile{mode: mode, hash: sha256.Sum256(content)}
	}
	return snapshot
}

func (f *Fixture) writeSetChangedFiles(t *testing.T, base string, before map[string]ignoredFile) []string {
	t.Helper()
	changed := make(map[string]bool)
	for _, name := range f.GitDiffFiles(t, base) {
		changed[name] = true
	}
	after := f.snapshotIgnoredFiles(t)
	for name, file := range before {
		if current, ok := after[name]; !ok || current != file {
			changed[name] = true
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changed[name] = true
		}
	}
	return slices.Sorted(maps.Keys(changed))
}

func TestWriteSetIncludesIgnoredChanges(t *testing.T) {
	f := SetupFixture(t)
	base := f.GitHead(t)
	const local = ".punt-labs/local/"
	writeFile(t, filepath.Join(f.Repo, local+"unchanged.txt"), "unchanged")
	writeFile(t, filepath.Join(f.Repo, local+"edited.txt"), "before")
	writeFile(t, filepath.Join(f.Repo, local+"removed.txt"), "before")
	before := f.snapshotIgnoredFiles(t)

	writeFile(t, filepath.Join(f.Repo, local+"unexpected.txt"), "new")
	writeFile(t, filepath.Join(f.Repo, local+"edited.txt"), "after!")
	require.NoError(t, os.Remove(filepath.Join(f.Repo, local+"removed.txt")))
	writeFile(t, filepath.Join(f.Repo, local+"ethos/unexpected.txt"), "not an Ethos runtime path")
	writeFile(t, filepath.Join(f.Repo, local+"ethos/layout.yaml"), "version: 5")
	writeFile(t, filepath.Join(f.Repo, local+"ethos/sessions/fixture.audit.jsonl"), "{}\n")
	writeFile(t, filepath.Join(f.Repo, local+"ethos/locks/mission-state/"+
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.lock"), "")

	require.Empty(t, f.GitDiffFiles(t, base))
	require.ElementsMatch(t, []string{
		local + "unexpected.txt",
		local + "edited.txt",
		local + "removed.txt",
		local + "ethos/unexpected.txt",
	}, f.writeSetChangedFiles(t, base, before))
}

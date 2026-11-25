//go:build linux || darwin

package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyCorpusComparesRenamedTrace(t *testing.T) {
	root := t.TempDir()
	write := func(path, contents string) {
		t.Helper()
		path = filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o700))
	}
	run := func(name string, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "LEGACY_CORPUS_SKIP_REGEN=")
		return cmd.CombinedOutput()
	}
	for _, tool := range []string{"git", "perl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required by test-legacy-corpus", tool)
		}
	}
	const data = "internal/autorepair/testdata/"
	const snapshot = data + "corpus/v4.22.1/"
	const trace = "trace-1711111111111111.jsonl"
	write(snapshot+"repo/"+trace, `{"event":"original"}`+"\n")
	write(snapshot+"git/tracked-files.txt", trace+"\n")
	write(snapshot+"git/repo-files.txt", trace+"\n")
	for i := range 11 {
		write(fmt.Sprintf("%sdamage/fixture-%02d/README", data, i), "fixture\n")
	}
	check, err := os.ReadFile("../scripts/legacy_corpus_test.sh")
	require.NoError(t, err)
	write("scripts/legacy_corpus_test.sh", string(check))
	write("scripts/legacy-corpus.sh", `#!/usr/bin/env bash
set -euo pipefail
dest="$4/corpus/$2"
mkdir -p "$dest/repo" "$dest/git"
printf '%s\n' '{"event":"changed"}' >"$dest/repo/trace-1722222222222222.jsonl"
printf '%s\n' 'trace-1722222222222222.jsonl' | tee "$dest/git/tracked-files.txt" >"$dest/git/repo-files.txt"
`)
	out, err := run("git", "init", "--quiet")
	require.NoError(t, err, "%s", out)
	out, err = run("git", "add", "--", "internal")
	require.NoError(t, err, "%s", out)
	out, err = run("bash", "scripts/legacy_corpus_test.sh")
	require.Contains(t, string(out), "regenerated file list matches")
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "contents differ beyond timestamps and ids: ./repo/trace-"+"NANOS.jsonl")
}

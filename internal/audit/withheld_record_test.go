package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsWithheldRecord: only the exact bytes writeRecord produces are a
// record. A record that decodes to the same fields but is spelled differently
// (a duplicate key, a case-variant key, another key order, white space, a ts
// that is not a timestamp) is not, because those spellings carry text.
func TestIsWithheldRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.corrupt")
	require.NoError(t, writeRecord(path, []byte(`{"ts":"2026-01-01T00:00:00Z","x":1}`+"\n"), WithheldRule))
	good, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, IsWithheldRecord(good), "a record writeRecord wrote: %s", good)
	require.NoError(t, writeRecord(path, []byte("garbage\n"), WithheldRule))
	noTS, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, IsWithheldRecord(noTS), "a record with no ts: %s", noTS)

	sum := strings.Repeat("ab", 32)
	base := `"withheld":"` + withheldText + `","sha256":"` + sum + `","size":3,"rule":"` + WithheldRule + `"`
	for name, in := range map[string]string{
		"ts holding text":     `{` + base + `,"ts":"GITHUB_TOKEN=ghp_abc alice@example.com /home/alice/x"}` + "\n",
		"duplicate rule key":  `{"rule":"password=hunter2",` + base + `}` + "\n",
		"case-variant key":    `{` + base + `,"RULE":"password=hunter2"}` + "\n",
		"other key order":     `{"sha256":"` + sum + `","withheld":"` + withheldText + `","size":3,"rule":"` + WithheldRule + `"}` + "\n",
		"white space":         `{ ` + base + ` }` + "\n",
		"two lines":           string(good) + string(good),
		"no trailing newline": strings.TrimSuffix(string(good), "\n"),
	} {
		assert.False(t, IsWithheldRecord([]byte(in)), name)
	}
}

// TestNoticeWithheld_SaysWhereTheOriginalIs: the notice promises the local copy
// only when there is one.
func TestNoticeWithheld_SaysWhereTheOriginalIs(t *testing.T) {
	say := func(local bool) string {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		old := os.Stderr
		os.Stderr = w
		noticeWithheld("c.corrupt", WithheldRule, local)
		os.Stderr = old
		require.NoError(t, w.Close())
		out, err := io.ReadAll(r)
		require.NoError(t, err)
		return string(out)
	}
	assert.Contains(t, say(true), "the original is in")
	assert.Contains(t, say(false), "no local copy of the original was found")
	assert.NotContains(t, say(false), "the original is in")

}

// TestHasLocalCopy_IsTheCopyOfThisRecord: a local file with the same stem is
// another retirement's copy unless its name carries the record's hash and its
// bytes hash to it. Two retirements of one stem leave two records over time; only
// the copy of the version the record names counts.
func TestHasLocalCopy_IsTheCopyOfThisRecord(t *testing.T) {
	repo := t.TempDir()
	hashOf := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	a, b := []byte("version a\n"), []byte("version b\n")
	assert.False(t, hasLocalCopy(repo, "stem", hashOf(a)), "nothing saved")

	require.NoError(t, saveOriginal(repo, "stem", b))
	assert.False(t, hasLocalCopy(repo, "stem", hashOf(a)), "only the other version is local")
	assert.True(t, hasLocalCopy(repo, "stem", hashOf(b)))

	require.NoError(t, saveOriginal(repo, "stem", a))
	assert.True(t, hasLocalCopy(repo, "stem", hashOf(a)))

	// A file under the right name whose bytes are not the version does not count.
	name := filepath.Join(QuarantineDir(repo), "stem2."+hashOf(a)[:12]+".corrupt")
	require.NoError(t, os.WriteFile(name, []byte("something else"), 0o600))
	assert.False(t, hasLocalCopy(repo, "stem2", hashOf(a)))
}

// TestUnstagedRecordNotice_NamesOnlyItsOwnCopy: a record found unstaged is
// reported with "the original is in the local zone" only when its own version is
// there; another retirement's copy of the same stem does not make it so.
func TestUnstagedRecordNotice_NamesOnlyItsOwnCopy(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	chunk := SessionChunkFile(100, 300)
	cn, kind := Classify(chunk, SessionNS)
	require.Equal(t, KindValid, kind)
	a, b := []byte("version a\n"), []byte("version b\n")
	require.NoError(t, writeRecord(filepath.Join(sealedDir, chunk+".corrupt"), a, WithheldRule))
	require.NoError(t, saveOriginal(repo, cn.Stem(), b)) // only the other version is local

	np := &notePad{}
	require.NoError(t, np.addUnstagedRecords(repo, sealedDir, cn))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	np.print()
	os.Stderr = old
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "no local copy of the original was found")

	require.NoError(t, saveOriginal(repo, cn.Stem(), a))
	np = &notePad{}
	require.NoError(t, np.addUnstagedRecords(repo, sealedDir, cn))
	r, w, err = os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	np.print()
	os.Stderr = old
	require.NoError(t, w.Close())
	out, err = io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "the original is in")
}

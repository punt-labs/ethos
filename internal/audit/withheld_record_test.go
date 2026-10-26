package audit

import (
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

	repo := t.TempDir()
	assert.False(t, hasLocalCopy(repo, "stem"))
	require.NoError(t, saveOriginal(repo, "stem", []byte("x")))
	assert.True(t, hasLocalCopy(repo, "stem"))
}

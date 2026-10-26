package audit

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoticeWithheld_Grammar: the rule is a verb phrase in the singular
// ("holds a credential"); the notice's subject is plural, "its bytes".
func TestNoticeWithheld_Grammar(t *testing.T) {
	for rule, want := range map[string]string{
		"holds a credential":                  "its bytes hold a credential",
		"names this machine's home directory": "its bytes name this machine's home directory",
		"contains a home-directory slug":      "its bytes contain a home-directory slug",
		"is not a JSON object":                "its bytes are not a JSON object",
		"has a thing":                         "its bytes has a thing",
		"was odd":                             "its bytes was odd",
	} {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		old := os.Stderr
		os.Stderr = w
		noticeWithheld("c.jsonl", rule)
		os.Stderr = old
		require.NoError(t, w.Close())
		out, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Contains(t, string(out), want)
	}
}

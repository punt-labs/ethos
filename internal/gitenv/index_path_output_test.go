package gitenv_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

func TestIndexPathOutput(t *testing.T) {
	for _, path := range []string{
		".git/index", "/repo/.git/index", "C:/repo/.git/index",
		"index ", "index\t", " index", "\tindex", "index\r",
		"in\ndex", "index\n", "index\n\n", "\n", " ", "\t",
		`"index"`, `index\literal`, "index\u00a0", "index\xff",
	} {
		t.Run(strconv.Quote(path), func(t *testing.T) {
			got, err := gitenv.IndexPathOutput([]byte(path + "\n"))
			assert.NoError(t, err)
			assert.Equal(t, path, got, "only the final LF is framing, never decode or normalize the pathname")
		})
	}
	for _, tc := range []struct {
		name string
		out  string
		err  string
	}{
		{"no output", "", "LF terminator"},
		{"no terminator", "index", "LF terminator"},
		{"CR is not terminator", "index\r", "LF terminator"},
		{"empty path", "\n", "empty index path"},
		{"NUL", "index\x00\n", "contains NUL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitenv.IndexPathOutput([]byte(tc.out))
			assert.ErrorContains(t, err, tc.err)
			assert.Empty(t, got)
		})
	}
}

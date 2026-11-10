package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every message ethos prints states what this binary did or found. It names no
// later actor and no command this branch removes (DES-084). The list is the
// closed set of phrases the round-1 review found; each must stay absent from
// every non-test Go source under cmd/, internal/ and plugin/.
func TestMessagesStateOnlyWhatThisCodeDid(t *testing.T) {
	banned := []string{
		"ethos audit quarantine",
		"retires at session start",
		"retires dead sessions at session start",
		"the next commit includes it",
		"retry in full mode",
		"the storage upgrade finishes it",
		"repair or clear it",
	}
	root := filepath.Join("..", "..")
	var hits []string
	for _, dir := range []string{"cmd", "internal", "plugin"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			// The command table names every cobra path the refusal policy
			// covers; removing the `audit quarantine` entry belongs to the
			// commands unit that deletes the command.
			if filepath.Base(path) == "store_refusal.go" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, phrase := range banned {
				if strings.Contains(string(b), phrase) {
					hits = append(hits, path+": "+phrase)
				}
			}
			return nil
		}))
	}
	assert.Empty(t, hits)
}

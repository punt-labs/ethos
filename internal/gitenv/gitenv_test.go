package gitenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsed(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GIT_COMMON_DIR", "/elsewhere/.git")
	t.Setenv("GIT_CEILING_DIRECTORIES", "/ceiling")

	env := Parsed()
	assert.Contains(t, env, "LC_ALL=C")
	assert.Contains(t, env, "LANGUAGE=")
	assert.Contains(t, env, "GIT_CEILING_DIRECTORIES=/ceiling", "a ceiling is the caller's to keep")
	for _, kv := range env {
		for _, gone := range []string{"LC_ALL=de", "LANGUAGE=de", "GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR="} {
			assert.False(t, strings.HasPrefix(kv, gone), "kept %s", kv)
		}
	}
}

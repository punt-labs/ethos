// Package gitenv builds the environment for a git command whose output or
// messages ethos parses.
package gitenv

import (
	"os"
	"strings"
)

// dropped are the variables Parsed removes: the locale, which Parsed sets
// itself, and the repository locations, which would make git answer for the
// repository they name instead of discovering one from the directory it is
// given. A pre-commit hook runs with GIT_DIR exported; from a subdirectory,
// git would then call that subdirectory the top level.
var dropped = map[string]bool{
	"LC_ALL":         true,
	"LANGUAGE":       true,
	"GIT_DIR":        true,
	"GIT_WORK_TREE":  true,
	"GIT_COMMON_DIR": true,
}

// Parsed returns the current environment with git's messages in the C locale
// (LC_ALL=C, LANGUAGE empty) and no GIT_DIR, GIT_WORK_TREE or GIT_COMMON_DIR.
// ethos matches git's English messages ("not a git repository", "dubious
// ownership"); a translated message would match nothing.
func Parsed() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !dropped[name] {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "LANGUAGE=")
}

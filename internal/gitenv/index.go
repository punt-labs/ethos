package gitenv

import (
	"fmt"
	"os"
	"strings"
)

// IndexPathOutput parses a nonempty raw pathname printed by rev-parse's
// --git-path index or --shared-index-path. Git appends one LF, without quoting
// the path. Remove only that record terminator: every other byte, including CR
// and embedded or trailing whitespace/newlines, belongs to the pathname.
// A caller accepting no shared index must handle zero-length output separately.
func IndexPathOutput(out []byte) (string, error) {
	if len(out) == 0 || out[len(out)-1] != '\n' {
		return "", fmt.Errorf("git index path output is missing its LF terminator")
	}
	name := string(out[:len(out)-1])
	if name == "" {
		return "", fmt.Errorf("git printed an empty index path")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("git index path contains NUL")
	}
	return name, nil
}

// ActualIndexEnv isolates repository discovery but retains the caller's actual
// committing index. Preserve GIT_INDEX_FILE verbatim: Git interprets relative
// paths in the checkout used as its command directory, even when the caller
// invoked the CLI from a subdirectory. Other inherited repository selectors
// are deliberately not accepted.
func ActualIndexEnv() ([]string, error) {
	env := Parsed()
	if index, ok := os.LookupEnv("GIT_INDEX_FILE"); ok {
		if index == "" {
			return nil, fmt.Errorf("GIT_INDEX_FILE is empty")
		}
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	return env, nil
}

// WithIndex returns an independent environment targeting a private candidate.
func WithIndex(env []string, index string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			out = append(out, entry)
		}
	}
	return append(out, "GIT_INDEX_FILE="+index)
}

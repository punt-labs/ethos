package resolve

import (
	"os/exec"
	"strings"
	"testing"
)

// TestGitLocateErrorStatesTheCauseOnly pins that git's own advice never
// reaches a message: the safe.directory remedy spans several lines of stderr.
func TestGitLocateErrorStatesTheCauseOnly(t *testing.T) {
	dubious := "fatal: detected dubious ownership in repository at '/r'\n" +
		"To add an exception for this directory, call:\n\n" +
		"\tgit config --global --add safe.directory /r\n"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"dubious ownership", &exec.ExitError{Stderr: []byte(dubious)}, "git refuses this directory (safe.directory)"},
		{"git missing", exec.ErrNotFound, "git not found on PATH"},
		{"other", &exec.ExitError{Stderr: []byte("fatal: something odd\nhint: do a thing\n")}, "fatal: something odd"},
	}
	for _, c := range cases {
		got := gitLocateError("/r/sub", c.err).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, got, c.want)
		}
		if strings.Contains(got, "git config") || strings.Contains(got, "hint:") || strings.Contains(got, "\n") {
			t.Errorf("%s: %q carries git's advice or a newline", c.name, got)
		}
	}
}

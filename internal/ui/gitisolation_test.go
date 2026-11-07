package ui

import (
	"fmt"
	"os"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// TestMain isolates git (testhelpers.IsolateGit): no inherited GIT_DIR or
// kin may point a git command at a real repository, and no temporary
// directory may resolve to the checkout that holds TMPDIR.
func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit()
	// UI readers union repository records with global session history. Tests
	// must never inspect the operator's real history through that fallback.
	home, err := os.MkdirTemp("", "ethos-ui-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			fmt.Fprintln(os.Stderr, err)
			_ = os.RemoveAll(home)
			os.Exit(2)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 2
	}
	os.Exit(code)
}

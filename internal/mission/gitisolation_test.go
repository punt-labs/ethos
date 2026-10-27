package mission

import (
	"os"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// TestMain isolates git (testhelpers.IsolateGit): no inherited GIT_DIR or
// kin may point a git command at a real repository, and no temporary
// directory may resolve to the checkout that holds TMPDIR.
func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit()
	os.Exit(m.Run())
}

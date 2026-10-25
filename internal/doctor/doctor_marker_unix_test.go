//go:build unix

package doctor

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A marker that exists but is not a regular file is neither enabled nor not
// enabled: doctor FAILs it, as the hooks block on it.
func TestHookPresenceMarkerThatIsNotAFile(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T, path string){
		"directory": func(t *testing.T, path string) { require.NoError(t, os.Mkdir(path, 0o755)) },
		"fifo":      func(t *testing.T, path string) { require.NoError(t, syscall.Mkfifo(path, 0o644)) },
	} {
		t.Run(name, func(t *testing.T) {
			repo := repoTemp(t)
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
			mk(t, filepath.Join(repo, ".punt-labs", "ethos", "enabled"))
			r := CheckSealHook(repo)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Contains(t, r.Detail, "not a regular file")
		})
	}
}

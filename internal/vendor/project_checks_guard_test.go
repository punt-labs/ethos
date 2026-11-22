package vendor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectCheckDestinationGuard(t *testing.T) {
	for _, scenario := range []string{"disabled", "no reference", "none", "absent", "identical", "different", "destination directory", "missing source", "source directory"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			f.src.Checks = "portable"
			f.src.ProjectChecksRoots = []string{f.global}
			const body = "name: portable\nchecks: []\n"
			if scenario == "source directory" {
				require.NoError(t, os.MkdirAll(filepath.Join(f.global, "checks/portable.yaml"), 0o700))
			} else if scenario != "missing source" {
				f.writeGlobal(t, "checks/portable.yaml", body)
			}
			dest := filepath.Join(f.repo, "checks/portable.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
			if scenario == "destination directory" {
				require.NoError(t, os.Mkdir(dest, 0o700))
			} else if scenario != "absent" {
				content := body
				if scenario == "different" {
					content += "description: destination policy\n"
				}
				require.NoError(t, os.WriteFile(dest, []byte(content), 0o600))
			}
			if scenario == "no reference" {
				f.src.Checks = ""
			} else if scenario == "none" {
				f.src.Checks = "none"
			}
			v, err := New(f.src, Options{Dest: f.repo, GuardChecks: scenario != "disabled"})
			require.NoError(t, err)
			err = v.guardProjectChecks()
			switch scenario {
			case "different", "destination directory", "missing source", "source directory":
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
		})
	}
}

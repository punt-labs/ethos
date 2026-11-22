package checkset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDecode(t *testing.T) {
	valid := "name: test\nchecks:\n  - name: test\n    stage: edit\n    files: ['**/*.go']\n    run: [go, vet, '{package_dir}']\n"
	for _, tc := range []struct{ name, old, replacement string }{
		{"unknown field", "    stage: edit", "    shell: true\n    stage: edit"},
		{"set field", "name: test\nchecks:", "name: test\nunknown: true\nchecks:"},
		{"set slug", "name: test\nchecks:", "name: ../test\nchecks:"},
		{"check slug", "  - name: test", "  - name: Bad"},
		{"stage", "stage: edit", "stage: later"},
		{"all edit", "files: ['**/*.go']", "files: all"},
		{"empty files", "files: ['**/*.go']", "files: []"},
		{"invalid glob", "files: ['**/*.go']", "files: ['[']"},
		{"exclude glob", "stage: edit", "stage: edit\n    exclude: ['[']"},
		{"missing argv", "run: [go, vet, '{package_dir}']", "run: []"},
		{"argv type", "run: [go, vet, '{package_dir}']", "run: [go, 99]"},
		{"exclude type", "stage: edit", "stage: edit\n    exclude: [99]"},
		{"empty command", "run: [go, vet, '{package_dir}']", "run: ['', vet]"},
		{"adapter", "stage: edit", "adapter: make\n    stage: edit"},
		{"on fail", "stage: edit", "on_fail: ignore\n    stage: edit"},
		{"cwd", "stage: edit", "cwd: somewhere\n    stage: edit"},
		{"timeout", "stage: edit", "timeout: nope\n    stage: edit"},
		{"zero timeout", "stage: edit", "timeout: 0s\n    stage: edit"},
		{"over budget", "stage: edit", "timeout: 61s\n    stage: edit"},
		{"unknown placeholder", "'{package_dir}'", "'{unknown}'"},
		{"spliced placeholder", "'{package_dir}'", "'prefix{file}'"},
		{"non-edit file", valid, strings.Replace(strings.Replace(valid, "stage: edit", "stage: commit", 1), "{package_dir}", "{file}", 1)},
		{"missing checks", valid, "name: test\n"},
		{"duplicate names", valid, valid + strings.SplitN(valid, "checks:\n", 2)[1]},
		{"extra document", valid, valid + "---\nname: other\nchecks: []\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(strings.Replace(valid, tc.old, tc.replacement, 1)))
			require.Error(t, err)
		})
	}
	empty, err := Decode([]byte("name: empty\nchecks: []\n"))
	require.NoError(t, err)
	require.Empty(t, empty.Checks)
	for _, stage := range []string{"edit", "commit", "handoff"} {
		set, err := Decode([]byte(strings.Replace(valid, "stage: edit", "stage: "+stage, 1)))
		require.NoError(t, err)
		want := map[string]time.Duration{"edit": 10 * time.Second, "commit": 120 * time.Second, "handoff": 10 * time.Minute}
		require.Equal(t, want[stage], set.Checks[0].Duration())
		require.Contains(t, set.Checks[0].Exclude, ".tmp/**")
		require.Equal(t, "root", set.Checks[0].Cwd)
	}
}

func TestGlobs(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"**/*.go", "file.go", true}, {"**/*.go", "a/b/file.go", true},
		{"*.go", "a/file.go", false}, {"src/**", "src/a/b.go", true},
		{"a/**/b?.[gt]o", "a/b1.go", true}, {"a/**/b?.[gt]o", "a/x/y/b2.to", true},
		{"**/*.go", "file.py", false},
	} {
		require.Equal(t, tc.want, Match(tc.pattern, tc.file), "%s %s", tc.pattern, tc.file)
	}
}

func TestLiteralAllGlob(t *testing.T) {
	set, err := Decode([]byte("name: literal\nchecks:\n  - name: literal\n    stage: edit\n    files: ['all']\n    run: ['true']\n"))
	require.NoError(t, err)
	require.True(t, set.Checks[0].Matches("all"))
	require.False(t, set.Checks[0].Matches("other"))
	for _, pattern := range []Patterns{{All: true}, {Globs: []string{"all"}}, {Globs: []string{"**/*.go", "*.sh"}}} {
		data, err := yaml.Marshal(pattern)
		require.NoError(t, err)
		var fromYAML Patterns
		require.NoError(t, yaml.Unmarshal(data, &fromYAML))
		require.Equal(t, pattern, fromYAML)
		data, err = json.Marshal(pattern)
		require.NoError(t, err)
		var fromJSON Patterns
		require.NoError(t, json.Unmarshal(data, &fromJSON))
		require.Equal(t, pattern, fromJSON)
	}
	for _, invalid := range []string{`"other"`, `true`, `null`, `{}`, `[1]`} {
		var pattern Patterns
		require.Error(t, json.Unmarshal([]byte(invalid), &pattern))
	}
	var empty Patterns
	require.NoError(t, json.Unmarshal([]byte(`[]`), &empty))
	require.Empty(t, empty.Globs)
	set, err = Decode([]byte("name: stage\nchecks:\n  - name: stage\n    stage: commit\n    files: all\n    run: ['true']\n"))
	require.NoError(t, err)
	require.True(t, set.Checks[0].Matches("other"))
	require.False(t, set.Checks[0].Matches(".tmp/other"))
}

func TestLayeredStore(t *testing.T) {
	repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
	for _, layer := range []struct{ root, description string }{{global, "global"}, {bundle, "bundle"}, {repo, "repo"}} {
		require.NoError(t, os.MkdirAll(filepath.Join(layer.root, "checks"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(layer.root, "checks/test.yaml"), []byte("name: test\ndescription: "+layer.description+"\nchecks:\n  - name: test\n    stage: edit\n    files: ['**']\n    run: ['true']\n"), 0o600))
	}
	s := NewLayeredStoreWithBundle(repo, bundle, global, false)
	for _, layer := range []struct{ root, description string }{{repo, "repo"}, {bundle, "bundle"}, {global, "global"}} {
		set, err := s.Load("test")
		require.NoError(t, err)
		require.Equal(t, layer.description, set.Description)
		require.NoError(t, os.Remove(filepath.Join(layer.root, "checks/test.yaml")))
	}
	require.Error(t, s.ValidateRef("missing"))
	require.NoError(t, s.ValidateRef("none"))
	require.NoError(t, s.ValidateRef(""))
	require.Error(t, s.ValidateRef("../bad"))
	require.NoError(t, os.WriteFile(filepath.Join(global, "checks/test.yaml"), []byte("name: test\nchecks: []\n"), 0o600))
	_, err := NewLayeredStoreWithBundle(repo, bundle, global, true).Load("test")
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "checks/test.yaml"), []byte("name: test\nchecks: []\n"), 0o600))
	_, err = NewLayeredStoreWithBundle(repo, bundle, global, true).Load("test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "checks/test.yaml"), []byte("name: other\nchecks: []\n"), 0o600))
	_, err = s.Load("test")
	require.ErrorContains(t, err, "match filename")
	names, err := s.List()
	require.NoError(t, err)
	require.Equal(t, []string{"test"}, names)
}

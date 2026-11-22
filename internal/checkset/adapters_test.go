package checkset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGoAdapterRoundTrip(t *testing.T) {
	set, err := Decode([]byte("name: go-checks\nchecks:\n  - name: vet\n    stage: edit\n    files: ['**/*.go']\n    adapter: go\n    go: vet\n"))
	require.NoError(t, err)
	require.Empty(t, set.Checks[0].Run)
	for _, marshal := range []func(any) ([]byte, error){yaml.Marshal, json.Marshal} {
		data, err := marshal(set)
		require.NoError(t, err)
		again, err := Decode(data)
		require.NoError(t, err, "%s", data)
		require.Empty(t, again.Checks[0].Run)
	}
}

func TestAdapterYAMLAliases(t *testing.T) {
	set, err := Decode([]byte(`name: aliases
checks:
  - name: base
    stage: edit
    files: ['**']
    adapter: &kind exec
    run: ['true']
  - name: inherited
    stage: edit
    files: ['**']
    adapter: *kind
    run: ['true']
  - name: make-base
    stage: edit
    files: ['**']
    adapter: make
    target: &target lint
    vars: &vars {MODE: &value fast}
  - name: make-inherited
    stage: edit
    files: ['**']
    adapter: make
    target: *target
    vars: *vars
  - name: make-value
    stage: edit
    files: ['**']
    adapter: make
    target: lint
    vars: {MODE: *value}
`))
	require.NoError(t, err)
	require.Len(t, set.Checks, 5)
}

func TestCheckYAMLMerge(t *testing.T) {
	set, err := Decode([]byte(`name: merged
checks:
  - &base
    name: base
    stage: edit
    files: ['**']
    run: ['true']
  - <<: *base
    name: inherited
`))
	require.NoError(t, err)
	require.Len(t, set.Checks, 2)
	require.Equal(t, Strings{"true"}, set.Checks[1].Run)
	for _, fields := range []string{"unknown: value", "adapter: make\n    target: check", "script: lint"} {
		_, err := Decode([]byte("name: merged\nchecks:\n  - &base\n    name: base\n    stage: edit\n    files: ['**']\n    run: ['true']\n  - <<: *base\n    name: inherited\n    " + fields + "\n"))
		require.Error(t, err, fields)
	}
}

func TestAdapterArgv(t *testing.T) {
	for _, tc := range []struct {
		name, stage, files, fields string
		want                       []string
	}{
		{"implicit", "edit", "['**']", "run: [lint, '{file}']", []string{"lint", "{file}"}},
		{"exec", "commit", "all", "adapter: exec\n    run: [lint]", []string{"lint"}},
		{"make", "edit", "['**']", "adapter: make\n    target: check-file\n    vars: {Z: 'two words', A: 'one=two'}", []string{"make", "check-file", "A=one=two", "Z=two words"}},
		{"go-test", "commit", "all", "adapter: go\n    go: test", []string{"go", "test", "./..."}},
		{"go-build", "handoff", "['**']", "adapter: go\n    go: build", []string{"go", "build", "{package_dir}"}},
		{"uv", "edit", "['**']", "adapter: uv\n    uv: [ruff, check, '{file}']", []string{"uv", "run", "ruff", "check", "{file}"}},
		{"npm-edit", "edit", "['**']", "adapter: npm\n    script: lint", []string{"npm", "run", "lint", "--", "{file}"}},
		{"npm-commit", "commit", "['**']", "adapter: npm\n    script: lint", []string{"npm", "run", "lint", "--", "{files}"}},
		{"npm-handoff", "handoff", "all", "adapter: npm\n    script: lint", []string{"npm", "run", "lint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, err := Decode([]byte("name: adapters\nchecks:\n  - name: probe\n    stage: " + tc.stage + "\n    files: " + tc.files + "\n    " + tc.fields + "\n"))
			require.NoError(t, err)
			argv, err := set.Checks[0].Argv()
			require.NoError(t, err)
			require.Equal(t, tc.want, argv)
			for _, marshal := range []func(any) ([]byte, error){yaml.Marshal, json.Marshal} {
				data, err := marshal(set)
				require.NoError(t, err)
				again, err := Decode(data)
				require.NoError(t, err, "%s", data)
				got, err := again.Checks[0].Argv()
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				if tc.name != "exec" && tc.name != "implicit" {
					require.Empty(t, again.Checks[0].Run)
				}
			}
		})
	}
}

func TestAdapterProbes(t *testing.T) {
	for _, tc := range []struct{ adapter, fields, marker, content string }{
		{"make", "target: check", "Makefile", "check:\n\t@echo MUST-NOT-RUN\n"},
		{"make", "target: check", "makefile", ""},
		{"make", "target: check", "GNUmakefile", ""},
		{"go", "go: vet", "go.mod", "module test"},
		{"uv", "uv: [ruff]", "pyproject.toml", ""},
		{"npm", "script: lint", "package.json", `{"scripts":{"lint":""}}`},
	} {
		t.Run(tc.adapter+"-"+tc.marker, func(t *testing.T) {
			set, err := Decode([]byte("name: adapters\nchecks:\n  - name: probe\n    stage: handoff\n    files: all\n    adapter: " + tc.adapter + "\n    " + tc.fields + "\n"))
			require.NoError(t, err)
			c := set.Checks[0]
			root := t.TempDir()
			require.ErrorContains(t, c.Probe(root), tc.marker)
			path := filepath.Join(root, tc.marker)
			require.NoError(t, os.Mkdir(path, 0700))
			require.Error(t, c.Probe(root))
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0600))
			require.NoError(t, c.Probe(root))
			if tc.adapter == "npm" {
				for _, bad := range []string{"{", "{}", `{"scripts":{"lint":null}}`, `{"scripts":{"lint":42}}`} {
					require.NoError(t, os.WriteFile(path, []byte(bad), 0600))
					require.Error(t, c.Probe(root))
				}
			}
		})
	}
}

func TestAdapterInvalidShapes(t *testing.T) {
	for _, fields := range []string{
		"adapter: unknown", "adapter: exec", "adapter: go", "adapter: go\n    go: run",
		"adapter: go\n    go: vet\n    run: []", "run: [true]\n    go: ''", "adapter: make", "adapter: uv", "adapter: npm",
		"adapter: make\n    target: --eval=x", "adapter: make\n    target: check file", "adapter: make\n    target: check\n    vars: {BAD-NAME: x}",
		"adapter: make\n    target: check\n    vars: {OK: '$HOME'}", "adapter: make\n    target: check\n    vars: {OK: '{file}'}",
		"adapter: make\n    target: check\n    vars: {OK: 42}", "adapter: uv\n    uv: []", "adapter: uv\n    uv: ['']",
		"adapter: uv\n    uv: [ruff, 'prefix{file}']", "adapter: npm\n    script: ''", "adapter: npm\n    script: '{bad}'",
		"run: [true]\n    vars: {}", "run: [true]\n    target: ''", "run: [true]\n    uv: []", "run: [true]\n    script: ''",
		"adapter: make\n    target: check\n    run: null", "adapter: make\n    target: check\n    script: x", "adapter: make\n    target: 123",
		"adapter: make\n    target: check\n    vars: null", "adapter: npm\n    script: 42", "run: [true]\n    adapter: null",
	} {
		t.Run(strings.ReplaceAll(fields, "\n    ", ";"), func(t *testing.T) {
			_, err := Decode([]byte("name: adapters\nchecks:\n  - name: probe\n    stage: edit\n    files: ['**']\n    " + fields + "\n"))
			require.Error(t, err)
		})
	}
}

func TestNpmScriptLiteral(t *testing.T) {
	for _, script := range []string{"{file}", "{root}", "lint{files}", "}", "-s", "--help"} {
		t.Run(script, func(t *testing.T) {
			_, err := (Check{Adapter: "npm", Script: script}).Argv()
			require.Error(t, err)
		})
	}
}

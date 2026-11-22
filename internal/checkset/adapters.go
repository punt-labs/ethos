package checkset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Adapter builds literal argv and probes the invocation's working directory.
type Adapter interface {
	Argv(Check) ([]string, error)
	Probe(Check, string) error
}

const MakeTargetPattern = `^[A-Za-z0-9_][A-Za-z0-9_.:/-]*$`
const MakeVariablePattern = `^[A-Za-z_][A-Za-z0-9_]*$`

var makeTarget = regexp.MustCompile(MakeTargetPattern)
var makeVariable = regexp.MustCompile(MakeVariablePattern)

var adapters = map[string]Adapter{"exec": execAdapter{}, "go": goAdapter{}, "make": makeAdapter{}, "uv": uvAdapter{}, "npm": npmAdapter{}}

func (c Check) adapter() (Adapter, error) {
	name := c.Adapter
	if name == "" {
		name = "exec"
	}
	a, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q", name)
	}
	return a, nil
}

// Argv leaves Run untouched so serialized adapter policies decode again.
func (c Check) Argv() ([]string, error) {
	a, err := c.adapter()
	if err != nil {
		return nil, err
	}
	for field, present := range map[string]bool{
		"go": c.Go != "" || c.fields["go"], "target": c.Target != "" || c.fields["target"],
		"vars": c.Vars != nil || c.fields["vars"], "uv": c.UV != nil || c.fields["uv"], "script": c.Script != "" || c.fields["script"],
	} {
		owner := map[string]string{"go": "go", "target": "make", "vars": "make", "uv": "uv", "script": "npm"}[field]
		if present && c.Adapter != owner {
			return nil, fmt.Errorf("%s requires adapter: %s", field, owner)
		}
	}
	if c.Adapter != "" && c.Adapter != "exec" && (c.Run != nil || c.fields["run"]) {
		return nil, fmt.Errorf("adapter %s forbids run", c.Adapter)
	}
	return a.Argv(c)
}

type makeAdapter struct{}

func (makeAdapter) Argv(c Check) ([]string, error) {
	if !makeTarget.MatchString(c.Target) {
		return nil, fmt.Errorf("make target must match %s", MakeTargetPattern)
	}
	keys := make([]string, 0, len(c.Vars))
	for key, value := range c.Vars {
		if !makeVariable.MatchString(key) {
			return nil, fmt.Errorf("invalid make variable name %q", key)
		}
		if strings.ContainsAny(value, "${}") {
			return nil, fmt.Errorf("make variable %s must be literal, without $ or placeholders", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	argv := []string{"make", c.Target}
	for _, key := range keys {
		argv = append(argv, key+"="+c.Vars[key])
	}
	return argv, nil
}

func (makeAdapter) Probe(_ Check, cwd string) error {
	return probeMarker(cwd, "Makefile", "makefile", "GNUmakefile")
}

type uvAdapter struct{}

func (uvAdapter) Argv(c Check) ([]string, error) {
	if len(c.UV) == 0 || strings.TrimSpace(c.UV[0]) == "" {
		return nil, fmt.Errorf("uv must be a non-empty argv")
	}
	return append([]string{"uv", "run"}, c.UV...), nil
}

func (uvAdapter) Probe(_ Check, cwd string) error { return probeMarker(cwd, "pyproject.toml") }

type npmAdapter struct{}

func (npmAdapter) Argv(c Check) ([]string, error) {
	if strings.TrimSpace(c.Script) == "" {
		return nil, fmt.Errorf("npm script is required")
	}
	if strings.HasPrefix(c.Script, "-") || strings.ContainsAny(c.Script, "{}") {
		return nil, fmt.Errorf("npm script must be a literal name without braces or a leading -")
	}
	argv := []string{"npm", "run", c.Script}
	if !c.Files.All {
		files := "{files}"
		if c.Stage == "edit" {
			files = "{file}"
		}
		argv = append(argv, "--", files)
	}
	return argv, nil
}

func (npmAdapter) Probe(c Check, cwd string) error {
	if err := probeMarker(cwd, "package.json"); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		return fmt.Errorf("npm adapter package.json: %w", err)
	}
	var pkg struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &pkg); err != nil {
		return fmt.Errorf("npm adapter package.json: %w", err)
	}
	if _, ok := pkg.Scripts[c.Script].(string); !ok {
		return fmt.Errorf("npm adapter package.json has no string script %q", c.Script)
	}
	return nil
}

func (c Check) Probe(cwd string) error {
	a, err := c.adapter()
	if err != nil {
		return err
	}
	return a.Probe(c, cwd)
}

type execAdapter struct{}

func (execAdapter) Argv(c Check) ([]string, error) { return append([]string(nil), c.Run...), nil }
func (execAdapter) Probe(Check, string) error      { return nil }

type goAdapter struct{}

func (goAdapter) Argv(c Check) ([]string, error) {
	if c.Go != "vet" && c.Go != "test" && c.Go != "build" {
		return nil, fmt.Errorf("go must be vet, test or build")
	}
	pkg := "{package_dir}"
	if c.Cwd == "package_dir" || c.Cwd == "file_dir" {
		pkg = "."
	}
	if c.Files.All {
		pkg = "./..."
	}
	return []string{"go", c.Go, pkg}, nil
}

func (goAdapter) Probe(_ Check, cwd string) error { return probeMarker(cwd, "go.mod") }

func probeMarker(cwd string, names ...string) error {
	for _, name := range names {
		info, err := os.Stat(filepath.Join(cwd, name))
		if err == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	return fmt.Errorf("adapter requires %v in cwd %s", names, cwd)
}

package checkset

import (
	"fmt"
	"os"
	"path/filepath"
)

// Adapter builds literal argv and probes the invocation's working directory.
type Adapter interface {
	Argv(Check) ([]string, error)
	Probe(Check, string) error
}

var adapters = map[string]Adapter{"exec": execAdapter{}, "go": goAdapter{}}

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
	if c.Adapter != "go" && (c.Go != "" || c.fields["go"]) {
		return nil, fmt.Errorf("go requires adapter: go")
	}
	if c.Adapter != "" && c.Adapter != "exec" && (c.Run != nil || c.fields["run"]) {
		return nil, fmt.Errorf("adapter %s forbids run", c.Adapter)
	}
	return a.Argv(c)
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

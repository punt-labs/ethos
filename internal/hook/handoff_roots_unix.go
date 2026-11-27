//go:build linux || darwin

package hook

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/checkset"
)

type handoffRoots struct {
	checkout, runtime, scratch, temp string
}

func (roots handoffRoots) home() string {
	return filepath.Join(roots.scratch, "home")
}

const worktreeSandboxPATH = "/usr/local/bin:/usr/bin:/bin"

func insideGatePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

func gatePlanInputsPresent(snapshot string, argv []string) bool {
	checkout, err := os.OpenRoot(snapshot)
	if err != nil {
		return false
	}
	defer checkout.Close()
	for _, arg := range argv {
		if arg != gateRoot && !strings.HasPrefix(arg, gateRoot+"/") {
			continue
		}
		rel := strings.TrimPrefix(arg, gateRoot+"/")
		if arg == gateRoot {
			rel = "."
		}
		if _, err := checkout.Lstat(rel); err != nil {
			return false
		}
	}
	return true
}

func (roots handoffRoots) replacer() *strings.Replacer {
	return strings.NewReplacer("{root}", roots.checkout, "{runtime}", roots.runtime, "{scratch}", roots.scratch)
}

func (roots handoffRoots) expandArgs(argv []string) []string {
	replace := roots.replacer()
	expanded := make([]string, len(argv))
	for i, arg := range argv {
		expanded[i] = replace.Replace(arg)
	}
	return expanded
}

func (roots handoffRoots) environment(profile *checkset.WorktreeSnapshot, platform checkset.SnapshotPlatform, defaultPath string) ([]string, error) {
	paths := roots.expandArgs(slices.Concat(profile.Path, platform.Path))
	for _, dir := range paths {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, ":\x00") {
			return nil, errors.New("invalid snapshot runtime PATH entry")
		}
	}
	if len(paths) != 0 {
		defaultPath = strings.Join(append(paths, defaultPath), string(filepath.ListSeparator))
	}
	values := make(map[string]string, len(profile.Env)+len(platform.Env))
	maps.Copy(values, profile.Env)
	maps.Copy(values, platform.Env)
	env := []string{
		"PATH=" + defaultPath, "TZ=UTC",
		"HOME=" + roots.home(), "TMPDIR=" + roots.temp,
		"ETHOS_HANDOFF_ROOT=" + roots.checkout,
		"ETHOS_HANDOFF_RUNTIME=" + roots.runtime,
		"ETHOS_HANDOFF_SCRATCH=" + roots.scratch,
	}
	if _, ok := values["LANG"]; !ok {
		env = append(env, "LANG=C")
	}
	replace := roots.replacer()
	var extra []string
	for key, value := range values {
		switch key {
		case "PATH", "HOME", "TMPDIR", "TZ", "GOCOVERDIR":
			return nil, errors.New("snapshot runtime environment overrides a runner variable")
		}
		if key == "" || strings.HasPrefix(key, "ETHOS_HANDOFF_") || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid snapshot runtime environment variable")
		}
		extra = append(extra, key+"="+replace.Replace(value))
	}
	slices.Sort(extra)
	return append(env, extra...), nil
}

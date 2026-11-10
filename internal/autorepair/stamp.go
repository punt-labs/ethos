package autorepair

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// StampName is the file name of both layout stamps.
const StampName = "layout.yaml"

// Stamp records that a store was converted to a layout. A missing stamp means
// unknown, so every step runs. A higher version is current: a downgraded
// binary never converts backward.
type Stamp struct {
	Version    int        `yaml:"version"`
	Ethos      string     `yaml:"ethos,omitempty"`
	RepairedAt string     `yaml:"repaired_at,omitempty"`
	Items      int        `yaml:"items"`
	Residual   []Residual `yaml:"residual,omitempty"`
}

// Current reports whether the stamp is at or above the layout this binary
// writes.
func (s Stamp) Current() bool { return s.Version >= LayoutVersion }

// GlobalStampPath is the stamp of the global store rooted at global.
func GlobalStampPath(global string) string { return filepath.Join(global, StampName) }

// RepositoryStampPath is the stamp of the repository behind p, in its local
// zone: S/.punt-labs/local/ethos/layout.yaml.
func RepositoryStampPath(p layout.Policy) string {
	return filepath.Join(p.LocalRoot(), StampName)
}

// ReadStamp reads the stamp at path. An absent file is an error wrapping
// fs.ErrNotExist and a zero Stamp, which is not Current. A stamp that cannot
// be parsed is an error and also a zero Stamp: the caller repairs.
func ReadStamp(path string) (Stamp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Stamp{}, fmt.Errorf("reading layout stamp: %w", err)
	}
	var s Stamp
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Stamp{}, fmt.Errorf("parsing layout stamp %s: %w", path, err)
	}
	return s, nil
}

// WriteStamp writes s to path atomically: a staging file in the same
// directory (so the rename never crosses a device), then rename. ensureDir
// creates the directory; for a repository it is layout.EnsureLocalDir so a
// symlinked ancestor is refused.
func WriteStamp(path string, s Stamp, ensureDir func(dir string) error) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return fmt.Errorf("creating stamp directory %s: %w", dir, err)
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding layout stamp: %w", err)
	}
	f, err := os.CreateTemp(dir, layout.DotTempPattern(filepath.Base(path)))
	if err != nil {
		return fmt.Errorf("staging layout stamp in %s: %w", dir, err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := firstErr(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing layout stamp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing layout stamp %s: %w", path, err)
	}
	return nil
}

// mkdirPrivate creates the global stamp directory.
func mkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

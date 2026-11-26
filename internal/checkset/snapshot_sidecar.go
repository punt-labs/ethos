package checkset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type WorktreeSnapshot struct {
	Profile    string              `yaml:"profile" json:"profile"`
	Epoch      string              `yaml:"epoch" json:"epoch"`
	Sources    SnapshotSources     `yaml:"sources" json:"sources"`
	Runtime    SnapshotRuntime     `yaml:"runtime" json:"runtime"`
	System     SnapshotSystem      `yaml:"system,omitempty" json:"system,omitempty"`
	Scratch    SnapshotScratch     `yaml:"scratch" json:"scratch"`
	Git        *SnapshotGit        `yaml:"git,omitempty" json:"git,omitempty"`
	Network    string              `yaml:"network,omitempty" json:"network,omitempty"`
	Prehydrate *SnapshotPrehydrate `yaml:"prehydrate,omitempty" json:"prehydrate,omitempty"`
}

type SnapshotSources struct {
	Mode string `yaml:"mode" json:"mode"`
}

type SnapshotRuntime struct {
	Platforms map[string]SnapshotPlatform `yaml:"platforms" json:"platforms"`
}

type SnapshotSystem struct {
	Tools Strings `yaml:"tools,omitempty" json:"tools,omitempty"`
}

type SnapshotPlatform struct {
	Bundle string            `yaml:"bundle" json:"bundle"`
	SHA256 string            `yaml:"sha256" json:"sha256"`
	URL    string            `yaml:"url,omitempty" json:"url,omitempty"`
	Path   []string          `yaml:"path,omitempty" json:"path,omitempty"`
	Env    map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

type SnapshotScratch struct {
	Checkout []string          `yaml:"checkout,omitempty" json:"checkout,omitempty"`
	Seed     map[string]string `yaml:"seed,omitempty" json:"seed,omitempty"`
}

type SnapshotGit struct {
	BaseRef string `yaml:"base_ref" json:"base_ref"`
}

type SnapshotPrehydrate struct {
	Run []string `yaml:"run" json:"run"`
}

func loadSnapshotSidecar(root, slug string, set *Set) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	path := filepath.Join("checks", "snapshots", slug+".yaml")
	info, err := dir.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("snapshot sidecar must be a regular file")
	}
	file, err := dir.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	const maxSidecar = 64 << 10
	if info.Size() > maxSidecar {
		return errors.New("snapshot sidecar is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSidecar+1))
	if err != nil {
		return err
	}
	if len(data) > maxSidecar {
		return errors.New("snapshot sidecar is too large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var sidecar struct {
		Checks map[string]WorktreeSnapshot `yaml:"checks"`
	}
	if err := dec.Decode(&sidecar); err != nil {
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("snapshot sidecar must contain one YAML document")
	}
	if len(sidecar.Checks) == 0 {
		return errors.New("snapshot sidecar must name a handoff check")
	}
	eligible := make(map[string]int, len(set.Checks))
	for i, check := range set.Checks {
		if check.Stage == "handoff" && check.Snapshot == nil {
			eligible[check.Name] = i
		}
	}
	for name, snapshot := range sidecar.Checks {
		if _, ok := eligible[name]; !ok {
			return fmt.Errorf("snapshot sidecar check %q is unknown, not a handoff, or already has an inline snapshot", name)
		}
		if err := snapshot.validate(); err != nil {
			return fmt.Errorf("snapshot sidecar check %q: %w", name, err)
		}
	}
	for name, snapshot := range sidecar.Checks {
		set.Checks[eligible[name]].WorktreeSnapshot = &snapshot
	}
	return nil
}

func (s WorktreeSnapshot) validate() error {
	if s.Profile != "linux-worktree-v1" || s.Epoch == "" || s.Sources.Mode != "worktree" {
		return errors.New("snapshot sidecar needs the linux-worktree-v1 profile, an epoch and worktree sources")
	}
	if s.Network != "" && s.Network != "loopback" {
		return errors.New("snapshot network must be loopback or omitted")
	}
	for _, tool := range s.System.Tools {
		const toolChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.+-"
		if tool == "" || tool == "." || tool == ".." || strings.Trim(tool, toolChars) != "" {
			return errors.New("snapshot system tools must be executable names, not paths")
		}
	}
	if s.Git != nil && !strings.HasPrefix(s.Git.BaseRef, "refs/") {
		return errors.New("snapshot Git base must be a full ref")
	}
	if s.Prehydrate != nil && (len(s.Prehydrate.Run) == 0 || s.Prehydrate.Run[0] == "") {
		return errors.New("snapshot prehydrate needs a command")
	}
	if len(s.Runtime.Platforms) == 0 {
		return errors.New("snapshot runtime needs at least one platform")
	}
	for name, platform := range s.Runtime.Platforms {
		goos, goarch, ok := strings.Cut(name, "-")
		if !ok || !isPlatformPart(goos) || !isPlatformPart(goarch) {
			return errors.New("invalid snapshot runtime platform; expected GOOS-GOARCH")
		}
		digest, err := hex.DecodeString(platform.SHA256)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != platform.SHA256 {
			return errors.New("snapshot runtime needs a lowercase SHA-256 digest")
		}
		if platform.Bundle == "" || strings.ContainsAny(platform.Bundle, "/\\") {
			return errors.New("snapshot runtime bundle must be a filename hint")
		}
		if platform.URL != "" {
			u, err := url.Parse(platform.URL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("snapshot runtime URL must be a public HTTPS asset URL")
			}
		}
	}
	return nil
}

func isPlatformPart(part string) bool {
	return part != "" && strings.Trim(part, "abcdefghijklmnopqrstuvwxyz0123456789") == ""
}

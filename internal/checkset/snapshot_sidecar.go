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
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	IsolationSnapshot     = "snapshot"
	IsolationLiveVerified = "live-verified"
)

type WorktreeSnapshot struct {
	Profile    string              `yaml:"profile" json:"profile"`
	Isolation  string              `yaml:"-" json:"isolation"`
	Epoch      string              `yaml:"epoch" json:"epoch"`
	Sources    SnapshotSources     `yaml:"sources" json:"sources"`
	Runtime    *SnapshotRuntime    `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Path       []string            `yaml:"path,omitempty" json:"path,omitempty"`
	Env        map[string]string   `yaml:"env,omitempty" json:"env,omitempty"`
	System     SnapshotSystem      `yaml:"system,omitempty" json:"system,omitempty"`
	Data       []SnapshotData      `yaml:"data,omitempty" json:"data,omitempty"`
	Scratch    SnapshotScratch     `yaml:"scratch" json:"scratch"`
	Git        *SnapshotGit        `yaml:"git,omitempty" json:"git,omitempty"`
	Network    string              `yaml:"network,omitempty" json:"network,omitempty"`
	Requires   Strings             `yaml:"requires,omitempty" json:"requires,omitempty"`
	Prehydrate *SnapshotPrehydrate `yaml:"prehydrate,omitempty" json:"prehydrate,omitempty"`
}

type SnapshotSources struct {
	Mode string `yaml:"mode" json:"mode"`
}

type SnapshotRuntime struct {
	Platforms map[string]SnapshotPlatform `yaml:"platforms" json:"platforms"`
}

type SnapshotSystem struct {
	SnapshotSystemInputs `yaml:",inline"`
	Platforms            map[string]SnapshotSystemInputs `yaml:"platforms,omitempty" json:"platforms,omitempty"`
}

type SnapshotSystemInputs struct {
	Tools Strings `yaml:"tools,omitempty" json:"tools,omitempty"`
	Files Strings `yaml:"files,omitempty" json:"files,omitempty"`
	Dirs  Strings `yaml:"dirs,omitempty" json:"dirs,omitempty"`
}

type SnapshotData struct {
	Name        string `yaml:"name" json:"name"`
	URL         string `yaml:"url" json:"url"`
	MaxAgeHours int    `yaml:"max_age_hours" json:"max_age_hours"`
	Mount       string `yaml:"mount" json:"mount"`
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
	Files    []string          `yaml:"files,omitempty" json:"files,omitempty"`
}

type SnapshotGit struct {
	BaseRef string   `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	Refs    []string `yaml:"refs,omitempty" json:"refs,omitempty"`
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
		Checks map[string]struct {
			WorktreeSnapshot `yaml:",inline"`
			Isolation        yaml.Node `yaml:"isolation"`
		} `yaml:"checks"`
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
	snapshots := make(map[string]WorktreeSnapshot, len(sidecar.Checks))
	for name, entry := range sidecar.Checks {
		if _, ok := eligible[name]; !ok {
			return fmt.Errorf("snapshot sidecar check %q is unknown, not a handoff, or already has an inline snapshot", name)
		}
		snapshot := entry.WorktreeSnapshot
		snapshot.Isolation = IsolationSnapshot
		if entry.Isolation.Kind != 0 {
			switch node := entry.Isolation; {
			case node.Kind == yaml.ScalarNode && node.Tag == "!!str" && (node.Value == IsolationSnapshot || node.Value == IsolationLiveVerified):
				snapshot.Isolation = node.Value
			default:
				return fmt.Errorf("snapshot sidecar check %q: isolation must be snapshot or live-verified", name)
			}
		}
		if err := snapshot.validate(); err != nil {
			return fmt.Errorf("snapshot sidecar check %q: %w", name, err)
		}
		snapshots[name] = snapshot
	}
	for name, snapshot := range snapshots {
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
	for _, requirement := range s.Requires {
		switch requirement {
		case "pty", "posix_shm", "mach_lookup":
		default:
			return fmt.Errorf("unsupported snapshot requirement %q", requirement)
		}
	}
	for _, file := range s.Scratch.Files {
		if !isSnapshotDataPath(file) || slices.Contains(strings.Split(file, "/"), ".git") {
			return errors.New("snapshot scratch files must be checkout-relative and outside .git")
		}
	}
	if err := s.System.SnapshotSystemInputs.validate(); err != nil {
		return err
	}
	for name, inputs := range s.System.Platforms {
		if !isSnapshotPlatform(name) {
			return errors.New("invalid snapshot system platform; expected GOOS-GOARCH")
		}
		if err := inputs.validate(); err != nil {
			return err
		}
	}
	if err := validateSnapshotData(s.Data); err != nil {
		return err
	}
	if s.Git != nil {
		if s.Git.BaseRef != "" && !strings.HasPrefix(s.Git.BaseRef, "refs/") {
			return errors.New("snapshot Git base must be a full ref")
		}
		for _, ref := range s.Git.Refs {
			if !validSnapshotGitRef(ref) {
				return errors.New("snapshot git.refs entries must be full branch, remote-tracking or tag refs")
			}
		}
	}
	if s.Prehydrate != nil && (len(s.Prehydrate.Run) == 0 || s.Prehydrate.Run[0] == "") {
		return errors.New("snapshot prehydrate needs a command")
	}
	if s.Runtime == nil {
		return nil
	}
	if len(s.Runtime.Platforms) == 0 {
		return errors.New("snapshot runtime needs at least one platform")
	}
	for name, platform := range s.Runtime.Platforms {
		if !isSnapshotPlatform(name) {
			return errors.New("invalid snapshot runtime platform; expected GOOS-GOARCH")
		}
		digest, err := hex.DecodeString(platform.SHA256)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != platform.SHA256 {
			return errors.New("snapshot runtime needs a lowercase SHA-256 digest")
		}
		if platform.Bundle == "" || strings.ContainsAny(platform.Bundle, "/\\") {
			return errors.New("snapshot runtime bundle must be a filename hint")
		}
		if platform.URL != "" && !isSnapshotHTTPSURL(platform.URL) {
			return errors.New("snapshot runtime URL must be a public HTTPS asset URL")
		}
	}
	return nil
}

func validSnapshotGitRef(ref string) bool {
	if !(strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/remotes/") || strings.HasPrefix(ref, "refs/tags/")) ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") ||
		strings.ContainsAny(ref, "~^:?*[\\") || strings.ContainsFunc(ref, func(c rune) bool { return c <= ' ' || c == 127 }) {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (s SnapshotSystemInputs) validate() error {
	for _, tool := range s.Tools {
		const toolChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.+-"
		if tool == "" || tool == "." || tool == ".." || strings.Trim(tool, toolChars) != "" {
			return errors.New("snapshot system tools must be executable names, not paths")
		}
	}
	for _, list := range []Strings{s.Files, s.Dirs} {
		for _, name := range list {
			if !path.IsAbs(name) || path.Clean(name) != name || name == "/" || strings.ContainsRune(name, 0) {
				return errors.New("snapshot system files and dirs must be clean absolute paths below the host root")
			}
		}
	}
	return nil
}

func validateSnapshotData(entries []SnapshotData) error {
	names := make(map[string]struct{}, len(entries))
	mounts := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !isSnapshotDataPath(entry.Name) || path.Base(entry.Name) != entry.Name ||
			!isSnapshotDataPath(entry.Mount) || entry.MaxAgeHours <= 0 || !isSnapshotHTTPSURL(entry.URL) {
			return errors.New("snapshot data needs a local name and mount, a positive max_age_hours and a public HTTPS URL")
		}
		_, duplicateName := names[entry.Name]
		_, duplicateMount := mounts[entry.Mount]
		if duplicateName || duplicateMount {
			return errors.New("snapshot data names and mounts must be unique")
		}
		names[entry.Name] = struct{}{}
		mounts[entry.Mount] = struct{}{}
	}
	return nil
}

func isSnapshotDataPath(name string) bool {
	return name != "." && name != ".." && !strings.HasPrefix(name, "../") &&
		!path.IsAbs(name) && path.Clean(name) == name && !strings.ContainsAny(name, "\\\x00")
}

func isSnapshotHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func isSnapshotPlatform(name string) bool {
	goos, goarch, ok := strings.Cut(name, "-")
	return ok && isPlatformPart(goos) && isPlatformPart(goarch)
}

func isPlatformPart(part string) bool {
	return part != "" && strings.Trim(part, "abcdefghijklmnopqrstuvwxyz0123456789") == ""
}

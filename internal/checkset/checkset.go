// Package checkset loads and validates project-defined check policies.
package checkset

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"gopkg.in/yaml.v3"
)

type Set struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description,omitempty" json:"description,omitempty"`
	Checks      []Check `yaml:"checks" json:"checks"`
}

type Check struct {
	Name         string   `yaml:"name" json:"name"`
	Stage        string   `yaml:"stage" json:"stage"`
	Files        Patterns `yaml:"files" json:"files"`
	Exclude      Strings  `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Run          Strings  `yaml:"run" json:"run"`
	Adapter      string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	OnFail       string   `yaml:"on_fail,omitempty" json:"on_fail,omitempty"`
	FailOnOutput bool     `yaml:"fail_on_output,omitempty" json:"fail_on_output,omitempty"`
	Timeout      string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Cwd          string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
}

// Patterns accepts a list of globs or the scalar "all".
type Patterns []string

// Strings rejects YAML coercions such as an integer argv element.
type Strings []string

func (s *Strings) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("expected a list of strings")
	}
	values := make(Strings, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return fmt.Errorf("list elements must be strings")
		}
		values = append(values, item.Value)
	}
	*s = values
	return nil
}

func (p *Patterns) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Value == "all" {
		*p = Patterns{"all"}
		return nil
	}
	var values Strings
	if err := values.UnmarshalYAML(node); err != nil {
		return fmt.Errorf("files must be a glob list or all: %w", err)
	}
	*p = Patterns(values)
	return nil
}

func Decode(data []byte) (*Set, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var set Set
	if err := decoder.Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding check set: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("check set must contain exactly one YAML document")
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return &set, nil
}

func ValidateReference(ref string) error {
	if ref == "" || ref == "none" {
		return nil
	}
	if err := attribute.ValidateSlug(ref); err != nil {
		return fmt.Errorf("checks reference %q: %w", ref, err)
	}
	return nil
}

func (s *Set) Validate() error {
	if err := attribute.ValidateSlug(s.Name); err != nil {
		return fmt.Errorf("check set name: %w", err)
	}
	if s.Checks == nil {
		return fmt.Errorf("check set %q: checks is required", s.Name)
	}
	seen := make(map[string]bool)
	for i := range s.Checks {
		c := &s.Checks[i]
		if seen[c.Name] {
			return fmt.Errorf("check set %q: duplicate check name %q", s.Name, c.Name)
		}
		seen[c.Name] = true
		if err := c.validate(); err != nil {
			return fmt.Errorf("check set %q, check %q: %w", s.Name, c.Name, err)
		}
	}
	return nil
}

func (c *Check) validate() error {
	if err := attribute.ValidateSlug(c.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	defaults := map[string]time.Duration{"edit": 10 * time.Second, "commit": 120 * time.Second, "handoff": 10 * time.Minute}
	budget, ok := defaults[c.Stage]
	if !ok {
		return fmt.Errorf("unknown stage %q", c.Stage)
	}
	if len(c.Files) == 0 {
		return fmt.Errorf("files is required")
	}
	for _, glob := range c.Files {
		if glob == "all" {
			if c.Stage == "edit" || len(c.Files) != 1 {
				return fmt.Errorf("files: all is only supported alone at commit or handoff")
			}
		} else if err := validateGlob(glob); err != nil {
			return err
		}
	}
	for _, glob := range c.Exclude {
		if err := validateGlob(glob); err != nil {
			return err
		}
	}
	if c.Adapter != "" && c.Adapter != "exec" {
		return fmt.Errorf("adapter %q is not yet supported", c.Adapter)
	}
	if len(c.Run) == 0 || strings.TrimSpace(c.Run[0]) == "" {
		return fmt.Errorf("run must be a non-empty argv")
	}
	for _, arg := range c.Run {
		if !strings.ContainsAny(arg, "{}") {
			continue
		}
		switch arg {
		case "{file}":
			if c.Stage != "edit" {
				return fmt.Errorf("{file} is only valid at edit; use {files}")
			}
		case "{files}", "{package_dir}", "{root}":
		default:
			return fmt.Errorf("unknown or spliced placeholder %q; placeholders must be whole argv elements", arg)
		}
	}
	if c.OnFail == "" {
		c.OnFail = "block"
		if c.Stage == "edit" {
			c.OnFail = "advisory"
		}
	}
	if c.OnFail != "advisory" && c.OnFail != "block" {
		return fmt.Errorf("unknown on_fail %q", c.OnFail)
	}
	if c.Cwd == "" {
		c.Cwd = "root"
	}
	if c.Cwd != "root" && c.Cwd != "file_dir" && c.Cwd != "package_dir" {
		return fmt.Errorf("unknown cwd %q", c.Cwd)
	}
	if c.Timeout == "" {
		c.Timeout = budget.String()
	}
	duration, err := time.ParseDuration(c.Timeout)
	cap := map[string]time.Duration{"edit": time.Minute, "commit": 15 * time.Minute, "handoff": 30 * time.Minute}[c.Stage]
	if err != nil || duration <= 0 || duration > cap {
		return fmt.Errorf("timeout %q must be a positive duration within the %s %s stage cap", c.Timeout, c.Stage, cap)
	}
	c.Exclude = append(c.Exclude, ".tmp/**", ".punt-labs/local/**")
	return nil
}

// Duration is defined for validated checks.
func (c Check) Duration() time.Duration {
	duration, _ := time.ParseDuration(c.Timeout)
	return duration
}

func (c Check) Matches(file string) bool {
	for _, glob := range c.Exclude {
		if Match(glob, file) {
			return false
		}
	}
	for _, glob := range c.Files {
		if glob == "all" || Match(glob, file) {
			return true
		}
	}
	return false
}

func validateGlob(glob string) error {
	if glob == "" || path.IsAbs(glob) {
		return fmt.Errorf("invalid relative glob %q", glob)
	}
	for _, segment := range strings.Split(glob, "/") {
		if segment == ".." || (strings.Contains(segment, "**") && segment != "**") {
			return fmt.Errorf("invalid glob %q: ** must be a whole path segment", glob)
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("invalid glob %q: %w", glob, err)
		}
	}
	return nil
}

// Match adds zero-or-more-directory ** segments to path.Match's glob syntax.
func Match(pattern, file string) bool {
	patterns, parts := strings.Split(pattern, "/"), strings.Split(file, "/")
	type position struct{ pattern, file int }
	seen, results := make(map[position]bool), make(map[position]bool)
	var match func(int, int) bool
	match = func(p, f int) bool {
		key := position{p, f}
		if seen[key] {
			return results[key]
		}
		seen[key] = true
		ok := false
		switch {
		case p == len(patterns):
			ok = f == len(parts)
		case patterns[p] == "**":
			ok = match(p+1, f) || (f < len(parts) && match(p, f+1))
		case f < len(parts):
			segment, _ := path.Match(patterns[p], parts[f])
			ok = segment && match(p+1, f+1)
		}
		results[key] = ok
		return ok
	}
	return match(0, 0)
}

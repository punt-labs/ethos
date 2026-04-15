// Package bundle provides discovery and resolution of team bundles.
//
// A team bundle is a self-contained tree of ethos content (identities,
// personalities, writing styles, roles, teams, pipelines, archetypes)
// that can be activated via the active_bundle field in a repo's
// .punt-labs/ethos.yaml file. See DES-051 for design rationale.
package bundle

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ValidName is the regex every bundle name must match: a slug starting
// with a letter or digit, containing only lowercase letters, digits,
// and hyphens. Shared with other ethos name-validation sites.
var ValidName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateName rejects names that could escape the bundle root via
// path traversal or refer to a sibling directory. Callers pass names
// that will be joined into a filesystem path.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("contains path separator")
	}
	if name != filepath.Clean(name) {
		return fmt.Errorf("not a clean path component")
	}
	if name != filepath.Base(name) {
		return fmt.Errorf("not a basename")
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("contains parent reference")
	}
	if !ValidName.MatchString(name) {
		return fmt.Errorf("must match %s", ValidName.String())
	}
	return nil
}

// Source identifies where a bundle was discovered.
type Source string

const (
	// SourceRepo: <repo>/.punt-labs/ethos-bundles/<name>/
	SourceRepo Source = "repo"
	// SourceGlobal: <globalRoot>/bundles/<name>/
	SourceGlobal Source = "global"
	// SourceLegacy: <repo>/.punt-labs/ethos/ (pre-bundle layout).
	SourceLegacy Source = "legacy"
)

// Bundle describes a discovered team bundle.
type Bundle struct {
	Name        string   // from manifest or directory name
	Path        string   // absolute path to the bundle root
	Source      Source   // where it was found
	Manifest    Manifest // parsed bundle.yaml (zero-valued if absent)
	HasManifest bool     // true if bundle.yaml exists
}

// Manifest is the bundle.yaml schema.
type Manifest struct {
	Name            string `yaml:"name"`
	Version         int    `yaml:"version,omitempty"`
	Description     string `yaml:"description,omitempty"`
	EthosMinVersion string `yaml:"ethos_min_version,omitempty"`
}

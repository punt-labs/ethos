// Package bundle provides discovery and resolution of team bundles.
//
// A team bundle is a self-contained tree of ethos content (identities,
// personalities, writing styles, roles, teams, pipelines, archetypes)
// that can be activated via the active_bundle field in a repo's
// .punt-labs/ethos.yaml file. See DES-051 for design rationale.
package bundle

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

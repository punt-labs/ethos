package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ManifestName is the install manifest's filename, kept at the seed root.
const ManifestName = ".seed-manifest.json"

const manifestSchema = 1

// Entry records what seed last wrote to one seeded path. hash is the content
// hash of that write; scope names the root the key resolves against; version
// and written are provenance, not used in the seed decision.
type Entry struct {
	Scope   string `json:"scope"`
	Hash    string `json:"hash"`
	Version string `json:"ethos_version"`
	Written string `json:"written"`
}

// Manifest is the per-machine record of seeded content, keyed by logical seed
// path (dest-relative under the ethos root, or "skills/..." under the skills
// root). It lets a later seed tell an unmodified shipped file from a user edit.
type Manifest struct {
	Schema  int              `json:"schema"`
	Entries map[string]Entry `json:"entries"`
}

// loadManifest reads the manifest at root. An unreadable manifest — missing,
// or under a destRoot that does not yet exist — yields an empty manifest, the
// state of a machine that has never run a manifest-aware seed; the seed then
// proceeds and any real problem surfaces as a placement or save error. Only a
// present-but-corrupt manifest is a hard error: garbage must not be silently
// discarded.
func loadManifest(root string) (*Manifest, error) {
	empty := func() *Manifest {
		return &Manifest{Schema: manifestSchema, Entries: map[string]Entry{}}
	}
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return empty(), nil
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing seed manifest in %q: %w", root, err)
	}
	if m.Entries == nil {
		m.Entries = map[string]Entry{}
	}
	m.Schema = manifestSchema
	return &m, nil
}

// save writes the manifest to root atomically, so a crash mid-write leaves the
// old manifest intact rather than a truncated one.
func (m *Manifest) save(root string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding seed manifest: %w", err)
	}
	if err := atomicWrite(filepath.Join(root, ManifestName), append(data, '\n')); err != nil {
		return fmt.Errorf("writing seed manifest in %q: %w", root, err)
	}
	return nil
}

// hashBytes returns the "sha256:"-prefixed content hash used throughout the
// manifest and decision logic.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// hashFile hashes the file at path with the same scheme as hashBytes.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

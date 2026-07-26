package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// loadManifest reads the manifest at root. When root is not an existing
// directory — a fresh machine whose root does not exist yet, or a root blocked
// by a non-directory — no manifest can be present, so it yields an empty one and
// lets seed itself report any blockage per file. When root is a directory, a
// missing manifest is empty too, but a present-but-unreadable manifest (bad
// perms, a directory, an I/O fault) or a corrupt body is a HARD error: silently
// treating it as empty would reclassify every tracked file as untracked, drop
// its upgrade as a no-clobber skip, and — once save() rewrites a fresh
// manifest — make that tracking loss durable. The caller must not seed or save
// on this error.
func loadManifest(root string) (*Manifest, error) {
	empty := &Manifest{Schema: manifestSchema, Entries: map[string]Entry{}}

	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return empty, nil
	}

	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading seed manifest in %q: %w", root, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing seed manifest in %q: %w", root, err)
	}
	if m.Schema > manifestSchema {
		// A newer ethos wrote this manifest. Fail closed rather than adopt it
		// and rewrite at the old schema, which would drop fields this binary
		// does not understand.
		return nil, fmt.Errorf(
			"seed manifest in %q has unsupported schema %d (this ethos supports %d) — upgrade ethos",
			root, m.Schema, manifestSchema)
	}
	if m.Entries == nil {
		m.Entries = map[string]Entry{}
	}
	// A missing or zero schema on a v1-era file defaults to the current schema.
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

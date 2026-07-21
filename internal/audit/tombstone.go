package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A Tombstone records that `ethos session purge` removed a session's roster
// entry while the session may still have had unsealed audit lines
// (docs/audit-seal.md §Two zones, §Seal failure policy). It lets the seal's
// vacuum cross-check keep looking at a session whose roster entry is gone, so
// the crash -> purge -> checkout-deleted -> commit sequence never goes silent.
//
// It carries no wall-clock timestamp of its own — the start date is a fixed
// property of the session and "when purged" lives in git/filesystem metadata.
type Tombstone struct {
	Session   string `json:"session"`
	StartDate string `json:"start_date,omitempty"` // YYYY-MM-DD
	Repo      string `json:"repo,omitempty"`       // repo identity the session was bound to
	Checkout  string `json:"checkout,omitempty"`   // recorded checkout path
	// UnsealedLines is set when purge was forced past a live file that still
	// held lines above its sealed watermark.
	UnsealedLines bool `json:"unsealed_lines,omitempty"`
	// LiveFileGone is set when the recorded live file was already absent at
	// purge time — a checkout deleted before its lines sealed.
	LiveFileGone bool `json:"live_file_gone,omitempty"`
}

// Flagged reports whether the tombstone carries an unsealed-lines signal that
// the vacuum cross-check must warn on until acknowledged.
func (t Tombstone) Flagged() bool {
	return t.UnsealedLines || t.LiveFileGone
}

// tombstonePath is the tombstone file for a session in the global sessions
// directory.
func tombstonePath(globalSessionsDir, session string) string {
	return filepath.Join(globalSessionsDir, filepath.Base(session)+".purged")
}

// WriteTombstone writes a session's tombstone. If a flagged, un-acked
// tombstone already stands at the name (a re-purge before the operator
// acknowledged the prior loss), it is retired first via the same
// never-overwrite sequence AckTombstone uses, so a fresh tombstone never drops
// the earlier loss record (OPT-2, docs/audit-seal.md §Two zones). An unflagged
// existing tombstone carries no loss signal and is simply replaced.
func WriteTombstone(globalSessionsDir string, t Tombstone) error {
	if err := os.MkdirAll(globalSessionsDir, 0o700); err != nil {
		return fmt.Errorf("creating sessions dir: %w", err)
	}
	if prior, err := ReadTombstone(tombstonePath(globalSessionsDir, t.Session)); err == nil && prior.Flagged() {
		if _, aErr := AckTombstone(globalSessionsDir, t.Session); aErr != nil {
			return fmt.Errorf("retiring prior flagged tombstone before re-purge: %w", aErr)
		}
	}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshaling tombstone: %w", err)
	}
	return os.WriteFile(tombstonePath(globalSessionsDir, t.Session), append(data, '\n'), 0o600)
}

// ReadTombstone reads a tombstone file. A torn or undecodable file is an
// error so a consumer can treat it as absent.
func ReadTombstone(path string) (Tombstone, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Tombstone{}, err
	}
	var t Tombstone
	if err := json.Unmarshal(data, &t); err != nil {
		return Tombstone{}, fmt.Errorf("decoding tombstone %s: %w", path, err)
	}
	if t.Session == "" {
		return Tombstone{}, fmt.Errorf("tombstone %s: empty session", path)
	}
	return t, nil
}

// ListTombstones returns every live (un-acked) tombstone in the global
// sessions directory. A missing directory yields nil.
func ListTombstones(globalSessionsDir string) ([]Tombstone, error) {
	entries, err := os.ReadDir(globalSessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", globalSessionsDir, err)
	}
	var out []Tombstone
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".purged") {
			continue
		}
		t, err := ReadTombstone(filepath.Join(globalSessionsDir, e.Name()))
		if err != nil {
			continue // torn tombstone reads as absent
		}
		out = append(out, t)
	}
	return out, nil
}

// AckTombstone retires a session's tombstone so the vacuum cross-check stops
// warning on it, without discarding the loss record. It renames <id>.purged
// to <id>.purged.acked; when that name is taken (the session was purged and
// acked before), it retires under a content-derived
// <id>.purged.acked-<hash> and, if that too exists, the first free
// -<hash>-N in a deterministic sequence — never an overwrite, so two distinct
// loss records both survive. Returns the retired filename.
func AckTombstone(globalSessionsDir, session string) (string, error) {
	src := tombstonePath(globalSessionsDir, session)
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no tombstone to acknowledge for session %q", session)
		}
		return "", fmt.Errorf("reading tombstone %s: %w", src, err)
	}
	base := filepath.Base(src) + ".acked"
	candidate := base
	if _, err := os.Stat(filepath.Join(globalSessionsDir, candidate)); err == nil {
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])[:12]
		candidate = base + "-" + hash
		for n := 2; ; n++ {
			if _, err := os.Stat(filepath.Join(globalSessionsDir, candidate)); errors.Is(err, fs.ErrNotExist) {
				break
			}
			candidate = fmt.Sprintf("%s-%s-%d", base, hash, n)
		}
	}
	if err := os.Rename(src, filepath.Join(globalSessionsDir, candidate)); err != nil {
		return "", fmt.Errorf("acking tombstone: %w", err)
	}
	return candidate, nil
}

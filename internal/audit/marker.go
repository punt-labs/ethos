package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Marker retires a corrupt sealed chunk. It carries deterministic content
// only — no wall-clock timestamp — so two checkouts quarantining the same
// chunk from the same state produce byte-identical markers that merge with
// no conflict (docs/audit-seal.md §Seal failure policy). "When" lives in the
// git commit metadata.
type Marker struct {
	// Chunk is the retired chunk's stem (audit-<first>-<last> or
	// log-<session>-<first>-<last>).
	Chunk string `json:"chunk"`
	// VerifiedLast is the max ts the corrupt bytes actually reached and of
	// any lines quarantine re-sealed from the live file. It — not the
	// filename <last> on faith — is what the marker contributes to the
	// watermark.
	VerifiedLast int64 `json:"verified_last"`
	// UnrecoveredFirst and UnrecoveredLast bound the sub-range the live file
	// no longer holds. Both zero means full recovery — no gap.
	UnrecoveredFirst int64 `json:"unrecovered_first,omitempty"`
	UnrecoveredLast  int64 `json:"unrecovered_last,omitempty"`
	// Reason names the corruption class.
	Reason string `json:"reason"`
}

// HasGap reports whether the marker records an unrecovered sub-range.
func (m Marker) HasGap() bool {
	return m.UnrecoveredFirst != 0 || m.UnrecoveredLast != 0
}

// ReadMarker reads and decodes a marker file. A missing, torn, or
// undecodable file is an error so every consumer can treat it as absent (a
// torn marker reads as absent everywhere).
func ReadMarker(path string) (Marker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Marker{}, os.ErrNotExist
		}
		return Marker{}, fmt.Errorf("reading marker %s: %w", path, err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("decoding marker %s: %w", path, err)
	}
	if m.Chunk == "" {
		return Marker{}, fmt.Errorf("marker %s: empty chunk field", path)
	}
	return m, nil
}

// MarshalMarker renders a marker to its canonical on-disk bytes. json sorts
// object keys, so two checkouts producing the same marker produce identical
// bytes.
func MarshalMarker(m Marker) ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshaling marker: %w", err)
	}
	return append(data, '\n'), nil
}

package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// A quarantine marker retires a corrupt sealed chunk. It carries
// deterministic content only — no wall-clock timestamp — so two checkouts
// quarantining the same chunk from the same state produce byte-identical
// markers that merge with no conflict (docs/audit-seal.md §Seal failure
// policy). "When" lives in the git commit metadata.
//
// The full quarantine verb (retire, re-seal, write marker, stage) is a
// later slice; this file defines the on-disk format and its reader/writer
// so the watermark and read paths can consult a marker that exists.
type quarantineMarker struct {
	// Chunk is the retired chunk's stem (audit-<first>-<last> or
	// log-<session>-<first>-<last>) — the name before its .jsonl suffix.
	Chunk string `json:"chunk"`
	// VerifiedLast is the max ts the corrupt bytes actually reached and
	// of any lines quarantine re-sealed from the live file. It — not the
	// filename <last> on faith — is what the marker contributes to the
	// watermark.
	VerifiedLast int64 `json:"verified_last"`
	// UnrecoveredFirst and UnrecoveredLast bound the sub-range the live
	// file no longer holds. Both zero means full recovery — no gap.
	UnrecoveredFirst int64 `json:"unrecovered_first,omitempty"`
	UnrecoveredLast  int64 `json:"unrecovered_last,omitempty"`
	// Reason names the corruption class (parse failure, ts mismatch).
	Reason string `json:"reason"`
}

// hasGap reports whether the marker records an unrecovered sub-range.
func (m quarantineMarker) hasGap() bool {
	return m.UnrecoveredFirst != 0 || m.UnrecoveredLast != 0
}

// readQuarantineMarker reads and decodes a marker file. A missing file, a
// torn file, or one that fails to decode is reported as an error so every
// consumer can treat it as absent (§Seal failure policy: a torn marker
// reads as absent everywhere).
func readQuarantineMarker(path string) (quarantineMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return quarantineMarker{}, os.ErrNotExist
		}
		return quarantineMarker{}, fmt.Errorf("reading marker %s: %w", path, err)
	}
	var m quarantineMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return quarantineMarker{}, fmt.Errorf("decoding marker %s: %w", path, err)
	}
	if m.Chunk == "" {
		return quarantineMarker{}, fmt.Errorf("marker %s: empty chunk field", path)
	}
	return m, nil
}

// marshalQuarantineMarker renders a marker to its canonical on-disk bytes.
// encoding/json sorts object keys, so two checkouts producing the same
// marker produce identical bytes.
func marshalQuarantineMarker(m quarantineMarker) ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshaling marker: %w", err)
	}
	return append(data, '\n'), nil
}

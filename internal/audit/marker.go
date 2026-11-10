package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
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
	// RetiredSHA256 binds local completion to the latest retained original.
	// Repeated retirement durably clears it before source removal and restores
	// it only after recovery. Empty is not proof of local completion.
	// Published gap markers omit it: their availability does not prove local
	// recovery completed, and they have no local retirement pointer to cover.
	RetiredSHA256 string `json:"retired_sha256,omitempty"`
}

// ErrTornMarker is returned when a marker already at the path cannot be
// decoded. It may have recorded a loss whose range is now unknown, so no writer
// replaces it.
var ErrTornMarker = errors.New("marker is unreadable")

// ErrMarkerCoversOtherChunk is returned when the marker at a stem's name
// covers a different chunk.
var ErrMarkerCoversOtherChunk = errors.New("marker covers another chunk")

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

// MergeMarkers returns the marker that records everything old and next record:
// the union of their unrecovered ranges (a record of loss is only ever widened),
// the higher VerifiedLast, and the retirement digest of either (next's wins when
// both carry one). The two must describe the same chunk; Reason is next's unless
// empty.
func MergeMarkers(old, next Marker) Marker {
	m := next
	if m.Reason == "" {
		m.Reason = old.Reason
	}
	if old.VerifiedLast > m.VerifiedLast {
		m.VerifiedLast = old.VerifiedLast
	}
	if m.RetiredSHA256 == "" {
		m.RetiredSHA256 = old.RetiredSHA256
	}
	switch {
	case !old.HasGap():
	case !next.HasGap():
		m.UnrecoveredFirst, m.UnrecoveredLast = old.UnrecoveredFirst, old.UnrecoveredLast
	default:
		m.UnrecoveredFirst = min(old.UnrecoveredFirst, next.UnrecoveredFirst)
		m.UnrecoveredLast = max(old.UnrecoveredLast, next.UnrecoveredLast)
	}
	return m
}

// writeMarker is the one place an audit marker is written. It reads any marker
// already at path and merges next into it (MergeMarkers), so no write narrows a
// recorded loss or drops a completion digest. clearRetired drops the digest from
// the result: a repeated retirement must invalidate the old completion before
// its source is removed, and a published marker never carries one. An existing
// marker that is torn or undecodable may have recorded a loss of unknown range,
// so it is never replaced: ErrTornMarker. One that cannot be read, or that
// covers another chunk (ErrMarkerCoversOtherChunk), is also an error.
func writeMarker(p layout.Policy, path string, next Marker, clearRetired bool) error {
	old, err := ReadMarker(path)
	var pathErr *os.PathError
	switch {
	case err == nil:
		if old.Chunk != next.Chunk {
			return fmt.Errorf("marker %s covers chunk %s, not %s: %w", path, old.Chunk, next.Chunk, ErrMarkerCoversOtherChunk)
		}
		if clearRetired {
			old.RetiredSHA256 = ""
		}
		next = MergeMarkers(old, next)
	case errors.Is(err, os.ErrNotExist):
	case errors.As(err, &pathErr):
		return err
	default:
		return fmt.Errorf("marker %s: %w: %v", path, ErrTornMarker, err)
	}
	if clearRetired {
		next.RetiredSHA256 = ""
	}
	body, err := MarshalMarker(next)
	if err != nil {
		return err
	}
	if err := ensureDestinationDir(p, filepath.Dir(path)); err != nil {
		return err
	}
	return replaceAuditFile(p, path, body)
}

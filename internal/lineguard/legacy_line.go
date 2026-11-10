package lineguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// IsJSONObject reports whether line is exactly one JSON object.
func IsJSONObject(line []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(line))
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// DedupeLine returns line decoded and re-encoded, so a duplicate key keeps its
// last value, with redacted set. It reports false when the line is not a JSON
// object.
func DedupeLine(line []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	m["redacted"] = true
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// RedactLegacyLine redacts one legacy audit line. tool_input goes through
// RedactToolInput, exactly as a fresh line does, and tool_input_hash and
// tool_input_preview are recomputed over the stored form when it changed
// (DES-054). Free-text fields that carry no tool_input (a preview-only
// line, a sentinel's audit_error) are redacted as text. It never sets
// path_format: a ~ already in a legacy line may be another user's home.
func RedactLegacyLine(raw []byte, repoRoot string, r mission.PathRedactor) ([]byte, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, false, fmt.Errorf("decoding: %w", err)
	}
	changed := false
	tool, _ := m["tool"].(string)
	if before, ok := m["tool_input"].(map[string]any); ok {
		// Compare the stored form, not RedactToolInput's bool: that bool
		// reports content removed, a leak replaced or a key renamed, and
		// ignores a value the path pass rewrote in place.
		red, removed, _ := RedactToolInputQuiet(map[string]any{"tool_input": before}, tool, repoRoot)
		wasJSON, err := json.Marshal(before)
		if err != nil {
			return nil, false, fmt.Errorf("encoding tool_input: %w", err)
		}
		isJSON, err := json.Marshal(red)
		if err != nil {
			return nil, false, fmt.Errorf("encoding redacted tool_input: %w", err)
		}
		if !bytes.Equal(wasJSON, isJSON) {
			m["tool_input"] = red
			env := map[string]any{"tool_input": red}
			if _, ok := m["tool_input_hash"]; ok {
				m["tool_input_hash"] = HashToolInput(env)
			}
			if _, ok := m["tool_input_preview"]; ok {
				m["tool_input_preview"] = ToolInputPreview(env)
			}
			if removed {
				m["redacted"] = true
			}
			changed = true
		}
	}
	// A preview is recomputed when tool_input changed, so it is swept only
	// when tool_input did not. audit_error is free text beside tool_input,
	// not derived from it, and is swept either way.
	textKeys := []string{"audit_error"}
	if !changed {
		textKeys = append(textKeys, "tool_input_preview")
	}
	textChanged := false
	for _, k := range textKeys {
		if s, ok := m[k].(string); ok {
			if red := r.Content(s); red != s {
				m[k], textChanged = red, true
			}
		}
	}
	if textChanged {
		m["redacted"] = true
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false, fmt.Errorf("encoding: %w", err)
	}
	return out, true, nil
}

// Remarshalled is raw decoded and encoded again, the form guardAuditLine
// writes, so a line keys the same before and after it went through the guard.
// A line that does not decode as one JSON object keys on its own bytes.
func Remarshalled(raw []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

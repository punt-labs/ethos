package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// knownInputKeys is the set of valid field names under "inputs:".
var knownInputKeys = map[string]bool{
	"files":      true,
	"ticket":     true,
	"bead":       true,
	"references": true,
	"trigger":    true,
}

// UnmarshalYAML accepts both "ticket" (canonical) and "bead"
// (deprecated alias). Setting both is an error. Unknown keys are
// rejected so that strict decode catches typos inside inputs.
func (in *Inputs) UnmarshalYAML(node *yaml.Node) error {
	*in = Inputs{} // reset for defensive re-decode safety
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("inputs: expected mapping, got kind %d", node.Kind)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !knownInputKeys[key] {
			return fmt.Errorf("inputs: unknown field %q", key)
		}
	}
	var raw struct {
		Ticket     string   `yaml:"ticket,omitempty"`
		Bead       string   `yaml:"bead,omitempty"`
		Files      []string `yaml:"files,omitempty"`
		References []string `yaml:"references,omitempty"`
		Trigger    *Trigger `yaml:"trigger,omitempty"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("inputs: %w", err)
	}
	return in.applyParsed(raw.Files, raw.Ticket, raw.Bead, raw.References, raw.Trigger)
}

// UnmarshalJSON accepts both "ticket" (canonical) and "bead"
// (deprecated alias). Setting both is an error. Unknown keys are
// rejected so that strict decode catches typos inside inputs.
func (in *Inputs) UnmarshalJSON(data []byte) error {
	*in = Inputs{} // reset for defensive re-decode safety
	// Reject unknown fields in JSON.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw struct {
		Ticket     string   `json:"ticket,omitempty"`
		Bead       string   `json:"bead,omitempty"`
		Files      []string `json:"files,omitempty"`
		References []string `json:"references,omitempty"`
		Trigger    *Trigger `json:"trigger,omitempty"`
	}
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("inputs: %w", err)
	}
	return in.applyParsed(raw.Files, raw.Ticket, raw.Bead, raw.References, raw.Trigger)
}

// applyParsed populates the receiver from parsed intermediate fields
// and enforces the ticket/bead exclusion. Shared by UnmarshalYAML and
// UnmarshalJSON.
//
// It does NOT emit the bead deprecation warning: decode runs on every
// store load, including the conflict scan that reads unrelated old
// missions, so a warning here fired on every create/dispatch (ethos-
// c0yp). The warning is a user-facing signal about a contract the
// caller submitted, so the create paths raise it via BeadAlias — see
// WarnBeadDeprecated.
func (in *Inputs) applyParsed(files []string, ticket, bead string, references []string, trigger *Trigger) error {
	if ticket != "" && bead != "" {
		return fmt.Errorf("inputs: both 'ticket' and 'bead' set; use 'ticket' (bead is deprecated)")
	}
	in.Files = files
	in.References = references
	in.Trigger = trigger
	if ticket != "" {
		in.Ticket = ticket
	} else if bead != "" {
		in.Ticket = bead
	}
	return nil
}

// BeadAlias returns the deprecated inputs.bead value a contract body
// sets, or "" when it uses inputs.ticket (or neither). It probes the
// raw bytes through a plain struct — no custom unmarshaler, no strict
// decode — so it triggers no side effect and tolerates a body that the
// strict decoder will reject on its own. The create paths call it to
// decide whether to warn about a contract the user actually submitted,
// which is why the warning no longer lives in the decode path.
func BeadAlias(data []byte) string {
	var probe struct {
		Inputs struct {
			Bead string `yaml:"bead"`
		} `yaml:"inputs"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Inputs.Bead)
}

// WarnBeadDeprecated writes the DES-049 deprecation warning to w,
// naming the bead value the user supplied. Callers guard on a non-empty
// BeadAlias result.
func WarnBeadDeprecated(w io.Writer, bead string) {
	fmt.Fprintf(w,
		"ethos: deprecation warning: 'inputs.bead' is deprecated — use 'inputs.ticket' (value: %q)\n", bead)
}

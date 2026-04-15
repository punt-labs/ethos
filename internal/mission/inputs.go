package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// beadDeprecationOnce ensures the bead→ticket deprecation warning is
// emitted at most once per process, avoiding N duplicate warnings when
// Store.checkWriteSetConflicts loads many old missions.
var beadDeprecationOnce sync.Once

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

// applyParsed populates the receiver from parsed intermediate fields,
// enforces the ticket/bead exclusion, and emits the deprecation warning
// when bead is used. Shared by UnmarshalYAML and UnmarshalJSON.
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
		beadDeprecationOnce.Do(func() {
			fmt.Fprintf(os.Stderr,
				"ethos: deprecation warning: 'inputs.bead' is deprecated — use 'inputs.ticket' (first seen value: %q)\n", bead)
		})
	}
	return nil
}


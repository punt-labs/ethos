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

// RenameBeadAlias rewrites a contract's deprecated inputs.bead key to
// inputs.ticket and reports whether it changed anything. A block-style key is
// renamed in place so every other byte is untouched; any other shape (a flow
// mapping, an odd quoting) is renamed through the YAML node tree. A contract
// that sets both keys is refused: neither value is ours to choose.
//
// The result is not what a record writer installs. The repair step writes it
// through ReplaceRecordFile, which re-encodes the file in its canonical form:
// comments and other syntax the canonical form does not carry are dropped, and
// every value is kept.
func RenameBeadAlias(data []byte) ([]byte, bool, error) {
	if BeadAlias(data) == "" {
		return data, false, nil
	}
	var probe struct {
		Inputs map[string]any `yaml:"inputs"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, false, fmt.Errorf("parsing contract: %w", err)
	}
	if _, both := probe.Inputs["ticket"]; both {
		return nil, false, fmt.Errorf("inputs: both 'ticket' and 'bead' set")
	}
	if out, ok := renameBeadLine(data); ok {
		if BeadAlias(out) == "" {
			return out, true, nil
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parsing contract: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("contract is not a mapping")
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "inputs" || top.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		in := top.Content[i+1]
		for j := 0; j+1 < len(in.Content); j += 2 {
			if in.Content[j].Value == "bead" {
				in.Content[j].Value = "ticket"
				var buf bytes.Buffer
				enc := yaml.NewEncoder(&buf)
				enc.SetIndent(4)
				if err := enc.Encode(&doc); err != nil {
					return nil, false, fmt.Errorf("encoding contract: %w", err)
				}
				if err := enc.Close(); err != nil {
					return nil, false, fmt.Errorf("encoding contract: %w", err)
				}
				return buf.Bytes(), true, nil
			}
		}
	}
	return nil, false, fmt.Errorf("inputs.bead not found in the contract's node tree")
}

// renameBeadLine renames the first "bead:" key line inside the top-level
// inputs block, leaving every other byte as it is.
func renameBeadLine(data []byte) ([]byte, bool) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	inInputs := false
	for i, line := range lines {
		text := string(line)
		if strings.HasPrefix(text, "inputs:") {
			if strings.TrimSpace(strings.TrimPrefix(text, "inputs:")) != "" {
				return nil, false // flow or inline value, not a block
			}
			inInputs = true
			continue
		}
		if !inInputs {
			continue
		}
		if len(text) > 0 && text[0] != ' ' && text[0] != '\t' && text[0] != '\n' && text[0] != '\r' && text[0] != '#' {
			return nil, false // left the block without finding the key
		}
		trimmed := strings.TrimLeft(text, " ")
		if strings.HasPrefix(trimmed, "bead:") {
			indent := text[:len(text)-len(trimmed)]
			lines[i] = []byte(indent + "ticket:" + strings.TrimPrefix(trimmed, "bead:"))
			return bytes.Join(lines, nil), true
		}
	}
	return nil, false
}

package mission

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML accepts both "ticket" (canonical) and "bead"
// (deprecated alias). Setting both is an error.
func (in *Inputs) UnmarshalYAML(node *yaml.Node) error {
	*in = Inputs{} // reset for defensive re-decode safety
	var raw struct {
		Ticket     string   `yaml:"ticket,omitempty"`
		Bead       string   `yaml:"bead,omitempty"`
		Files      []string `yaml:"files,omitempty"`
		References []string `yaml:"references,omitempty"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.Ticket != "" && raw.Bead != "" {
		return fmt.Errorf("inputs: both 'ticket' and 'bead' set; use 'ticket' (bead is deprecated)")
	}
	in.Files = raw.Files
	in.References = raw.References
	if raw.Ticket != "" {
		in.Ticket = raw.Ticket
	} else if raw.Bead != "" {
		in.Ticket = raw.Bead
		fmt.Fprintf(os.Stderr,
			"ethos: deprecation warning: 'inputs.bead' is deprecated — use 'inputs.ticket' (value %q carried forward)\n",
			raw.Bead)
	}
	return nil
}

// UnmarshalJSON accepts both "ticket" (canonical) and "bead"
// (deprecated alias). Setting both is an error.
func (in *Inputs) UnmarshalJSON(data []byte) error {
	*in = Inputs{} // reset for defensive re-decode safety
	var raw struct {
		Ticket     string   `json:"ticket,omitempty"`
		Bead       string   `json:"bead,omitempty"`
		Files      []string `json:"files,omitempty"`
		References []string `json:"references,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Ticket != "" && raw.Bead != "" {
		return fmt.Errorf("inputs: both 'ticket' and 'bead' set; use 'ticket' (bead is deprecated)")
	}
	in.Files = raw.Files
	in.References = raw.References
	if raw.Ticket != "" {
		in.Ticket = raw.Ticket
	} else if raw.Bead != "" {
		in.Ticket = raw.Bead
		fmt.Fprintf(os.Stderr,
			"ethos: deprecation warning: 'inputs.bead' is deprecated — use 'inputs.ticket' (value %q carried forward)\n",
			raw.Bead)
	}
	return nil
}

package resolve

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const (
	RecordBlame = "blame"
	RecordFull  = "full"
)

var (
	_ yaml.Unmarshaler = (*RepoConfig)(nil)
	_ yaml.Marshaler   = RepoConfig{}
)

// RecordingMode validates the canonical record setting. Unset means blame;
// invalid settings never silently select a different destination.
func (c RepoConfig) RecordingMode() (layout.RecordMode, error) {
	switch c.Record {
	case "", RecordBlame:
		return layout.RecordBlame, nil
	case RecordFull:
		return layout.RecordFull, nil
	default:
		return 0, &layout.ValidationError{Field: "record", Message: fmt.Sprintf("unknown value %q: must be %q or %q", c.Record, RecordBlame, RecordFull)}
	}
}

// UnmarshalYAML rejects wrong types as well as unknown modes. yaml.v3 normally
// coerces scalar numbers/bools into Go strings and null into an empty string;
// inspect the node first so a present null is not mistaken for an unset field.
func (c *RepoConfig) UnmarshalYAML(node *yaml.Node) error {
	// Keep record failures typed even when another field is malformed or keys
	// are duplicated. Repair commands may tolerate unrelated config errors but
	// must not treat an explicit invalid policy as an omitted setting.
	seenRecord := false
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value != "record" {
				continue
			}
			if seenRecord {
				return &layout.ValidationError{Field: "record", Message: "must not be specified more than once"}
			}
			seenRecord = true
			if err := validateRecordNode(node.Content[i+1]); err != nil {
				return err
			}
		}
	}
	var field struct {
		Record yaml.Node `yaml:"record"`
	}
	if err := node.Decode(&field); err != nil {
		return err
	}
	if err := validateRecordNode(&field.Record); err != nil {
		return err
	}
	type plain RepoConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	cfg := RepoConfig(decoded)
	if _, err := cfg.RecordingMode(); err != nil {
		return err
	}
	*c = cfg
	return nil
}

func validateRecordNode(r *yaml.Node) error {
	for r.Kind == yaml.AliasNode {
		r = r.Alias
	}
	if r.Kind == 0 {
		return nil
	}
	if r.Kind != yaml.ScalarNode || r.ShortTag() != "!!str" {
		return &layout.ValidationError{Field: "record", Message: "must be a string: blame or full"}
	}
	_, err := (RepoConfig{Record: r.Value}).RecordingMode()
	return err
}

// MarshalYAML validates typed RepoConfig values during marshaling. Encoding a
// yaml.Node does not invoke this check. The interface{} result is
// yaml.Marshaler's contract.
func (c RepoConfig) MarshalYAML() (interface{}, error) {
	if _, err := c.RecordingMode(); err != nil {
		return nil, err
	}
	type plain RepoConfig
	return plain(c), nil
}

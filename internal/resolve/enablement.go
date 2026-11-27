package resolve

import (
	"path/filepath"
	"strings"
)

const (
	ProjectEnabledFile = ".punt-labs/ethos/enabled"
	LocalEnabledFile   = ".punt-labs/local/ethos/enabled"
	LocalDisabledFile  = ".punt-labs/local/ethos/disabled"
	ProjectOptOutFile  = ".punt-labs/ethos/opt-out"
)

// EnablementState records the effective state and its checkout-relative marker.
// With no marker, DecidingFile is empty and the checkout is off.
type EnablementState struct {
	Enabled      bool   `json:"enabled"`
	DecidingFile string `json:"deciding_file,omitempty"`
}

// EffectiveEnablement checks the invoking checkout, not the shared store.
func EffectiveEnablement(checkout string) (EnablementState, error) {
	if checkout == "" {
		return EnablementState{}, nil
	}
	for _, candidate := range []EnablementState{
		{DecidingFile: ProjectOptOutFile},
		{DecidingFile: LocalDisabledFile},
		{Enabled: true, DecidingFile: ProjectEnabledFile},
		{Enabled: true, DecidingFile: LocalEnabledFile},
	} {
		parts := strings.Split(candidate.DecidingFile, "/")
		parent, err := readableEthosPath(checkout, parts[:len(parts)-1]...)
		if err != nil {
			return EnablementState{}, err
		}
		if parent == "" {
			continue
		}
		present, err := readEnablementMarker(filepath.Join(parent, parts[len(parts)-1]))
		if err != nil {
			return EnablementState{}, err
		}
		if present {
			return candidate, nil
		}
	}
	return EnablementState{}, nil
}

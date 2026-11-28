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

func (s EnablementState) String() string {
	if s.DecidingFile == "" {
		return "ethos is off: no enablement marker"
	}
	if s.Enabled {
		return "ethos is on: " + s.DecidingFile
	}
	return "ethos is off: " + s.DecidingFile
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
		present, err := checkoutMarker(checkout, candidate.DecidingFile)
		if err != nil {
			return EnablementState{}, err
		}
		if present {
			return candidate, nil
		}
	}
	return EnablementState{}, nil
}

// ProjectOptedOut checks the committed opt-out independently of other markers.
func ProjectOptedOut(checkout string) (bool, error) {
	return checkoutMarker(checkout, ProjectOptOutFile)
}

func checkoutMarker(checkout, relative string) (bool, error) {
	parts := strings.Split(relative, "/")
	parent, err := readableEthosPath(checkout, parts[:len(parts)-1]...)
	if err != nil || parent == "" {
		return false, err
	}
	return readEnablementMarker(filepath.Join(parent, parts[len(parts)-1]))
}

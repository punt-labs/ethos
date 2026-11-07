package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// FindOperation looks up a prepared mission event across all sessions and all
// read layers. Equal copies collapse; divergent content for the same operation
// is an error. No current-mode eligibility or watermark filter is applied.
func FindOperation(p layout.Policy, missionID, operationID string) (*Line, error) {
	if missionID == "" || operationID == "" {
		return nil, errors.New("prepared event requires mission and operation identity")
	}
	streams, err := ListStreams(p)
	if err != nil {
		return nil, err
	}
	var found *Line
	var canonical []byte
	for _, s := range streams {
		if s.Namespace != MissionNS || s.Mission != missionID {
			continue
		}
		data, err := ReadStream(p, s)
		if err != nil {
			return nil, err
		}
		for _, l := range append(data.Lines, data.Legacy...) {
			var h struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.Unmarshal(l.Raw, &h); err != nil {
				return nil, err
			}
			if h.OperationID != operationID {
				continue
			}
			b, err := CanonicalPayload(l.Raw)
			if err != nil {
				return nil, err
			}
			if found != nil && (found.Session != l.Session || !bytes.Equal(canonical, b)) {
				return nil, fmt.Errorf("prepared event operation %q has divergent payloads or sessions", operationID)
			}
			copy := l
			found, canonical = &copy, b
		}
	}
	return found, nil
}

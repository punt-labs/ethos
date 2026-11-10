package mission

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// LegacyEventSession names the event session recorded in an envelope built from
// a reflection that predates round decisions: no session submitted it.
const LegacyEventSession = "legacy-import"

// LegacyReflectionEnvelope returns the local per-round detail envelope
// (trace/missions/<id>/reflection-<round>.yaml) for a reflection that was
// published in the record tree before DES-079. decision is the shared
// decision the contract already holds for that round; the envelope must
// carry it exactly or ReadReflections reports a conflict and drops the
// detail. redacted is the content stored. The envelope is never marked for
// publication.
func LegacyReflectionEnvelope(decision RoundDecision, redacted Reflection) ([]byte, error) {
	created, err := time.Parse(time.RFC3339, redacted.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("reflection round %d has no usable created_at: %w", redacted.Round, err)
	}
	in := reflectionIntent{
		Version: 1, Kind: "reflect", Mission: redacted.Mission,
		Decision:     decision,
		Reflection:   redacted,
		EventSession: LegacyEventSession,
	}
	in.Event = reflectionEvent(redacted, decision.ID, "blame")
	in.Event.TS = created.UTC().Format(time.RFC3339Nano)
	if err := in.validate(redacted.Mission); err != nil {
		return nil, fmt.Errorf("reflection round %d: %w", redacted.Round, err)
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encoding reflection envelope: %w", err)
	}
	return data, nil
}

// DecodeLegacyReflections decodes a reflections.yaml aggregate for mission id,
// applying the same validation and mission back-fill the readers do.
func DecodeLegacyReflections(data []byte, id string) ([]Reflection, error) {
	return decodeReflectionsFile(data, id)
}

// RedactReflection returns r with every free-text field redacted for repoRoot.
func RedactReflection(repoRoot string, r Reflection) (Reflection, error) {
	return RedactRecord(repoRoot, r)
}

// DecisionFor returns the shared decision the contract holds for round, or
// nil when round_decisions are absent or do not cover it.
func (c *Contract) DecisionFor(round int) *RoundDecision {
	return c.decision(round)
}

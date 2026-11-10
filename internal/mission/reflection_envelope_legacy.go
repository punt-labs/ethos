package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// LegacyEventSession names the event session recorded in an envelope built from
// a reflection that predates round decisions: no session submitted it.
const LegacyEventSession = "legacy-import"

// LegacyReflectionEnvelope returns the local per-round detail envelope
// (trace/missions/<id>/reflection-<round>.yaml) for a reflection that was
// published in the record tree before DES-079. The decision identifier is the
// one importLegacyDecisions derives from original, so the envelope joins the
// shared decision that import produces; redacted is the content stored. The
// envelope is never marked for publication.
func LegacyReflectionEnvelope(original, redacted Reflection) (round int, data []byte, err error) {
	b, err := json.Marshal(original)
	if err != nil {
		return 0, nil, fmt.Errorf("encoding reflection for round %d: %w", original.Round, err)
	}
	hash := sha256.Sum256(b)
	created, err := time.Parse(time.RFC3339, redacted.CreatedAt)
	if err != nil {
		return 0, nil, fmt.Errorf("reflection round %d has no usable created_at: %w", redacted.Round, err)
	}
	in := reflectionIntent{
		Version: 1, Kind: "reflect", Mission: redacted.Mission,
		Decision: RoundDecision{ID: hex.EncodeToString(hash[:16]), Round: redacted.Round,
			CreatedAt: redacted.CreatedAt, Author: redacted.Author, Recommendation: redacted.Recommendation},
		Reflection:   redacted,
		EventSession: LegacyEventSession,
	}
	in.Event = reflectionEvent(redacted, in.Decision.ID, "blame")
	in.Event.TS = created.UTC().Format(time.RFC3339Nano)
	if err := in.validate(redacted.Mission); err != nil {
		return 0, nil, fmt.Errorf("reflection round %d: %w", redacted.Round, err)
	}
	data, err = yaml.Marshal(in)
	if err != nil {
		return 0, nil, fmt.Errorf("encoding reflection envelope: %w", err)
	}
	return redacted.Round, data, nil
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

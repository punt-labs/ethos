package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// lineGuard judges one line about to be stored: the line to store, the rule it
// broke ("" when it was clean), and whether it became a loss marker.
type lineGuard func(line []byte) (out []byte, rule string, marker bool)

// missionLineGuard is the guard for a mission event line. The audit guard marks
// a rewritten line with a top-level redacted field, which the mission-event
// schema does not have: the strict decoder would drop the sealed event. A
// decodable event is instead redacted by the mission writer's own record pass
// and checked by its sink guard, and stays an event. A line that is not an
// event, or that cannot be redacted into one, falls back to the audit guard.
func missionLineGuard(repoRoot string) lineGuard {
	return func(line []byte) ([]byte, string, bool) {
		if out, rule, ok := guardMissionEvent(repoRoot, line); ok {
			return out, rule, false
		}
		return guardAuditLineFull(line)
	}
}

func guardMissionEvent(repoRoot string, line []byte) ([]byte, string, bool) {
	if mission.InvalidText(line) != "" {
		return nil, "", false
	}
	// A duplicate key is looked for in the original bytes: once decoded, the
	// first value is gone and the line would be rewritten with no notice.
	rule := ""
	if _, k, dup := redact.DuplicateJSONKey(line); dup {
		rule = "holds duplicate key " + quoteUnlessLeak(k)
	}
	// Keys that differ only in case are folded onto one field by the typed
	// decode below, silently; they are looked for in a plain map decode first,
	// and such a line is left to the audit guard (a marker and a notice).
	var generic map[string]any
	gdec := json.NewDecoder(bytes.NewReader(line))
	gdec.UseNumber()
	if err := gdec.Decode(&generic); err != nil || generic == nil || caseCollision(generic) || caseVariantOfRewritten(generic) {
		return nil, "", false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	dec.UseNumber() // a number is kept digit for digit, not rounded through float64
	var e mission.Event
	if err := dec.Decode(&e); err != nil {
		return nil, "", false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, "", false
	}
	red, err := mission.RedactRecord(repoRoot, e)
	switch {
	case err != nil:
		// Details the passes refuse lose only the leaves that still leak, and are
		// marked; the whole map goes only if even that is refused.
		r, rerr := mission.NewPathRedactor(repoRoot)
		if rerr != nil {
			return nil, "", false
		}
		scrubbed := e
		scrubbed.Details = scrubLeaves(r, e.Details)
		markerKey := ""
		if scrubbed.Details != nil {
			// The marker never overwrites a key the caller had: it takes the
			// first free redacted#N (N from 1).
			key := "redacted"
			for n := 1; ; n++ {
				if _, taken := scrubbed.Details[key]; !taken {
					break
				}
				key = "redacted#" + strconv.Itoa(n)
			}
			if key != "redacted" {
				markerKey = key
			}
			scrubbed.Details[key] = true
		}
		if red, err = mission.RedactRecord(repoRoot, scrubbed); err != nil {
			scrubbed.Details = nil
			if red, err = mission.RedactRecord(repoRoot, scrubbed); err != nil {
				return nil, "", false
			}
			rule = joinRules(rule, "held details that could not be redacted")
		} else {
			rule = joinRules(rule, "held details with a leaf that could not be redacted")
			if markerKey != "" {
				rule += `; marked details under "` + markerKey + `" (key "redacted" was taken)`
			}
		}
	case !reflect.DeepEqual(red, e):
		rule = joinRules(rule, "held a home path, a credential or an address")
	}
	raw, err := json.Marshal(red)
	if err != nil {
		return nil, "", false
	}
	canon, err := mission.CheckBytes("log.jsonl", append(raw, '\n'))
	if err != nil {
		if red.Details != nil {
			// The last resort keeps the event and drops its details.
			red.Details = nil
			if raw, err = json.Marshal(red); err == nil {
				canon, err = mission.CheckBytes("log.jsonl", append(raw, '\n'))
			}
			rule = joinRules(rule, "held details that could not be redacted")
		}
		if err != nil {
			return nil, "", false
		}
	}
	return bytes.TrimRight(canon, "\n"), rule, true
}

// scrubLeaves returns a copy of v in which every string and key has been through
// the credential pass and then the content pass, and a string or key that still
// names a home afterwards is replaced by a placeholder. A leaf that does not
// leak keeps its content-pass form (a path rewritten to ~, say), never a
// placeholder.
func scrubLeaves(r mission.PathRedactor, v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	// Credentials first: a key the content pass renames (password@example.com)
	// would otherwise lose the name that says its value is one.
	judged, _ := redact.SecretsValue(v)
	out, _ := scrubLeaf(r, judged).(map[string]any)
	return out
}

func scrubLeaf(r mission.PathRedactor, v any) any {
	switch x := v.(type) {
	case string:
		c := r.Content(x)
		if r.Leaks(c) {
			return redactedPath
		}
		return c
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = scrubLeaf(r, e)
		}
		return out
	case map[string]any:
		out, _ := redact.RenameKeys(x, func(k string) string {
			c := r.Content(k)
			if r.Leaks(c) {
				return redactedKey
			}
			return c
		}, false, func(_ string, e any) any { return scrubLeaf(r, e) })
		return out
	}
	return v
}

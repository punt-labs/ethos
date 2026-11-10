package layout

import "testing"

func TestTrackedLocal(t *testing.T) {
	for rel, want := range map[string]bool{
		"": false,
		".punt-labs/local/ethos/sessions/s.audit.jsonl":  true,
		".punt-labs/ethos/sessions/s/audit.jsonl":        true,
		".punt-labs/ethos/sessions/s.audit.jsonl":        true,
		".punt-labs/ethos/missions.jsonl.lock":           true,
		".punt-labs/ethos/identities/a.ext/b.local.yaml": true,
		".punt-labs/ethos/missions/m-1/contract.yaml":    false,
		".punt-labs/ethos/missions/m-1/log-s-1-2.jsonl":  false,
		".punt-labs/ethos.yaml":                          false,
		"src/.punt-labs/local/x":                         false,
	} {
		if got := TrackedLocal(rel); got != want {
			t.Errorf("TrackedLocal(%q) = %v, want %v", rel, got, want)
		}
	}
}

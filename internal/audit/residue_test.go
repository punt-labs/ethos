package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaxLastBySession(t *testing.T) {
	chunks := []ChunkName{
		{Session: "A", Last: 100},
		{Session: "A", Last: 300},
		{Session: "A", Last: 200},
		{Session: "B", Last: 50},
	}
	got := MaxLastBySession(chunks)
	if got["A"] != 300 {
		t.Errorf("A max last = %d, want 300", got["A"])
	}
	if got["B"] != 50 {
		t.Errorf("B max last = %d, want 50", got["B"])
	}
	if _, ok := got["C"]; ok {
		t.Error("unsealed session C must be absent from the map")
	}
}

func TestResidueLinesFiltered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.jsonl")
	body := "" +
		`{"ts":"` + FormatLineTS(150) + `","session":"A","event":"x"}` + "\n" + // A sealed to 200 → drop
		`{"ts":"` + FormatLineTS(250) + `","session":"A","event":"x"}` + "\n" + // past A's 200 → keep
		`{"ts":"` + FormatLineTS(10) + `","session":"B","event":"x"}` + "\n" + // B has no chunk → keep
		`{"ts":"` + FormatLineTS(10) + `","event":"x"}` + "\n" + // no attribution → keep
		`not json` + "\n" // unparseable → skipped, no error
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResidueLinesFiltered(path, map[string]int64{"A": 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("kept %d lines, want 3 (A@250, B@10, unattributed@10)", len(got))
	}
	// Survivors carry no session tag — never re-attributed.
	for _, l := range got {
		if l.Session != "" {
			t.Errorf("survivor carries session %q, want empty", l.Session)
		}
	}
	// A's already-sealed 150 line must be gone; the surviving A line is 250.
	for _, l := range got {
		if l.TS == 150 {
			t.Error("A's already-sealed line (ts 150) must be dropped")
		}
	}
}

func TestResidueLinesFilteredMissing(t *testing.T) {
	got, err := ResidueLinesFiltered(filepath.Join(t.TempDir(), "nope.jsonl"), nil)
	if err != nil || got != nil {
		t.Fatalf("missing residue = %v, %v; want nil, nil", got, err)
	}
}

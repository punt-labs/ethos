package audit

import (
	"testing"
	"time"
)

func TestTSToField(t *testing.T) {
	tests := []struct {
		ns   int64
		want string
	}{
		{0, "0000000000000000000"},
		{1, "0000000000000000001"},
		{1_700_000_000_000_000_000, "1700000000000000000"},
		{9223372036854775807, "9223372036854775807"},
	}
	for _, tt := range tests {
		got := TSToField(tt.ns)
		if got != tt.want {
			t.Errorf("TSToField(%d) = %q, want %q", tt.ns, got, tt.want)
		}
		if len(got) < Digits {
			t.Errorf("TSToField(%d) width %d < %d", tt.ns, len(got), Digits)
		}
	}
}

func TestChunkNamesSortChronologically(t *testing.T) {
	a := SessionChunkFile(1_700_000_000_000_000_000, 1_700_000_000_000_000_010)
	b := SessionChunkFile(1_700_000_000_000_000_011, 1_700_000_000_000_000_020)
	if !(a < b) {
		t.Errorf("chunk names do not sort chronologically: %q !< %q", a, b)
	}
}

func TestSessionChunkRoundTrip(t *testing.T) {
	cn, kind := Classify(SessionChunkFile(100, 200), SessionNS)
	if kind != KindValid {
		t.Fatalf("kind = %v, want KindValid", kind)
	}
	if cn.First != 100 || cn.Last != 200 || cn.Session != "" {
		t.Errorf("parsed = %+v, want {First:100 Last:200 Session:}", cn)
	}
	if cn.ChunkFile() != SessionChunkFile(100, 200) {
		t.Errorf("ChunkFile() round-trip = %q", cn.ChunkFile())
	}
}

func TestMissionChunkRoundTripHyphenatedSession(t *testing.T) {
	session := "abc-123-def"
	cn, kind := Classify(MissionChunkFile(session, 100, 200), MissionNS)
	if kind != KindValid {
		t.Fatalf("kind = %v, want KindValid", kind)
	}
	if cn.Session != session || cn.First != 100 || cn.Last != 200 {
		t.Errorf("parsed = %+v, want session %q range [100,200]", cn, session)
	}
	if cn.MarkerFile() != "log-"+session+"-"+TSToField(100)+"-"+TSToField(200)+".quarantine" {
		t.Errorf("MarkerFile() = %q", cn.MarkerFile())
	}
}

func TestClassify(t *testing.T) {
	valid := SessionChunkFile(100, 200)
	tests := []struct {
		name string
		file string
		ns   Namespace
		want Kind
	}{
		{"valid session chunk", valid, SessionNS, KindValid},
		{"frozen legacy audit", "audit.jsonl", SessionNS, KindOther},
		{"frozen legacy log", "log.jsonl", MissionNS, KindOther},
		{"mission contract", "contract.yaml", MissionNS, KindOther},
		{"near-miss short ts", "audit-100-200.jsonl", SessionNS, KindNearMiss},
		{"near-miss non-numeric", "audit-abc-def.jsonl", SessionNS, KindNearMiss},
		{"quarantine marker", "audit-" + TSToField(100) + "-" + TSToField(200) + ".quarantine", SessionNS, KindQuarantine},
		{"corrupt artifact", valid + ".corrupt", SessionNS, KindCorrupt},
		{"corrupt-hash artifact", valid + ".corrupt-deadbeef", SessionNS, KindCorrupt},
		{"temp file", SessionTempFile(100, 200), SessionNS, KindTemp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, kind := Classify(tt.file, tt.ns); kind != tt.want {
				t.Errorf("Classify(%q, %v) = %v, want %v", tt.file, tt.ns, kind, tt.want)
			}
		})
	}
}

func TestParseLineTSRoundTrip(t *testing.T) {
	ns := time.Date(2026, 7, 20, 12, 0, 0, 123456789, time.UTC).UnixNano()
	got, err := ParseLineTS(FormatLineTS(ns))
	if err != nil {
		t.Fatal(err)
	}
	if got != ns {
		t.Errorf("round-trip = %d, want %d", got, ns)
	}
}

func TestParseLineTSLegacyRFC3339(t *testing.T) {
	got, err := ParseLineTS("2026-07-20T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).UnixNano(); got != want {
		t.Errorf("legacy ts = %d, want %d", got, want)
	}
}

func TestSplitLines(t *testing.T) {
	got := SplitLines([]byte("a\nb\nc\n"))
	if len(got) != 3 {
		t.Fatalf("terminated: got %d lines, want 3", len(got))
	}
	// A trailing non-terminated run is still returned (callers pre-truncate).
	got = SplitLines([]byte("a\nb"))
	if len(got) != 2 {
		t.Fatalf("non-terminated: got %d lines, want 2", len(got))
	}
}

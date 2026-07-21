package hook

import (
	"testing"
	"time"
)

func TestTSToChunkField(t *testing.T) {
	tests := []struct {
		ns   int64
		want string
	}{
		{0, "0000000000000000000"},
		{1, "0000000000000000001"},
		{1_700_000_000_000_000_000, "1700000000000000000"},
		{9223372036854775807, "9223372036854775807"}, // max int64, 19 digits
	}
	for _, tt := range tests {
		got := tsToChunkField(tt.ns)
		if got != tt.want {
			t.Errorf("tsToChunkField(%d) = %q, want %q", tt.ns, got, tt.want)
		}
		if len(got) < chunkDigits {
			t.Errorf("tsToChunkField(%d) = %q, width %d < %d", tt.ns, got, len(got), chunkDigits)
		}
	}
}

func TestChunkFilenamesSortChronologically(t *testing.T) {
	// Two chunks a nanosecond apart must sort by name in ts order.
	a := sessionChunkFile(1_700_000_000_000_000_000, 1_700_000_000_000_000_010)
	b := sessionChunkFile(1_700_000_000_000_000_011, 1_700_000_000_000_000_020)
	if !(a < b) {
		t.Errorf("chunk names do not sort chronologically: %q !< %q", a, b)
	}
}

func TestSessionChunkRoundTrip(t *testing.T) {
	name := sessionChunkFile(100, 200)
	cn, kind := classifySessionChunk(name)
	if kind != chunkValid {
		t.Fatalf("classify(%q) kind = %v, want chunkValid", name, kind)
	}
	if cn.First != 100 || cn.Last != 200 {
		t.Errorf("parsed range = [%d,%d], want [100,200]", cn.First, cn.Last)
	}
	if cn.Session != "" {
		t.Errorf("session namespace chunk carried session %q, want empty", cn.Session)
	}
}

func TestMissionChunkRoundTrip(t *testing.T) {
	// A session id containing hyphens must survive the parse.
	session := "abc-123-def"
	name := missionChunkFile(session, 100, 200)
	cn, kind := classifyMissionChunk(name)
	if kind != chunkValid {
		t.Fatalf("classify(%q) kind = %v, want chunkValid", name, kind)
	}
	if cn.Session != session {
		t.Errorf("parsed session = %q, want %q", cn.Session, session)
	}
	if cn.First != 100 || cn.Last != 200 {
		t.Errorf("parsed range = [%d,%d], want [100,200]", cn.First, cn.Last)
	}
}

func TestClassifyChunk(t *testing.T) {
	valid := sessionChunkFile(100, 200)
	tests := []struct {
		name string
		file string
		ns   chunkNamespace
		want chunkKind
	}{
		{"valid session chunk", valid, sessionNamespace, chunkValid},
		{"frozen legacy audit", "audit.jsonl", sessionNamespace, chunkOther},
		{"frozen legacy log", "log.jsonl", missionNamespace, chunkOther},
		{"mission contract", "contract.yaml", missionNamespace, chunkOther},
		{"unrelated file", "results.yaml", sessionNamespace, chunkOther},
		{"near-miss short ts", "audit-100-200.jsonl", sessionNamespace, chunkNearMiss},
		{"near-miss non-numeric", "audit-abc-def.jsonl", sessionNamespace, chunkNearMiss},
		{"near-miss missing field", "audit-" + tsToChunkField(100) + ".jsonl", sessionNamespace, chunkNearMiss},
		{"quarantine marker", "audit-" + tsToChunkField(100) + "-" + tsToChunkField(200) + ".quarantine", sessionNamespace, chunkQuarantine},
		{"corrupt artifact", valid + ".corrupt", sessionNamespace, chunkCorrupt},
		{"corrupt-hash artifact", valid + ".corrupt-deadbeef", sessionNamespace, chunkCorrupt},
		{"temp file", sessionTempFile(100, 200), sessionNamespace, chunkTemp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, kind := classifyChunk(tt.file, tt.ns)
			if kind != tt.want {
				t.Errorf("classifyChunk(%q, %v) = %v, want %v", tt.file, tt.ns, kind, tt.want)
			}
		})
	}
}

func TestSessionTempFileName(t *testing.T) {
	got := sessionTempFile(100, 200)
	want := ".audit-" + tsToChunkField(100) + "-" + tsToChunkField(200) + ".jsonl.tmp"
	if got != want {
		t.Errorf("sessionTempFile = %q, want %q", got, want)
	}
}

func TestParseLineTSRoundTrip(t *testing.T) {
	// A nanosecond-precision timestamp must round-trip through the
	// on-disk string form to the same integer.
	ns := time.Date(2026, 7, 20, 12, 0, 0, 123456789, time.UTC).UnixNano()
	s := formatLineTS(ns)
	got, err := parseLineTS(s)
	if err != nil {
		t.Fatalf("parseLineTS(%q): %v", s, err)
	}
	if got != ns {
		t.Errorf("round-trip ts = %d, want %d (via %q)", got, ns, s)
	}
}

func TestParseLineTSLegacyRFC3339(t *testing.T) {
	// A legacy second-precision RFC3339 line still parses.
	got, err := parseLineTS("2026-07-20T12:00:00Z")
	if err != nil {
		t.Fatalf("parseLineTS legacy: %v", err)
	}
	want := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).UnixNano()
	if got != want {
		t.Errorf("legacy ts = %d, want %d", got, want)
	}
}

func TestParseLineTSInvalid(t *testing.T) {
	if _, err := parseLineTS("not-a-timestamp"); err == nil {
		t.Error("parseLineTS(invalid) = nil error, want error")
	}
}

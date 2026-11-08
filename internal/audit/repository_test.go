package audit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func repositoryPolicy(t *testing.T, mode layout.RecordMode) layout.Policy {
	t.Helper()
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), mode)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepositoryCanonicalRejectsMalformedObjects(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `42`, `{`, `{} {}`, `{"record":"blame","record":"full"}`, `{"input":{"x":1,"x":2}}`} {
		if _, err := CanonicalPayload([]byte(raw)); err == nil {
			t.Errorf("accepted malformed or ambiguous payload %s", raw)
		}
	}
}

func TestRepositoryLiveCorruptionIsNotAbsence(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	good := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n")
	for _, bad := range []string{"not-json\n", "{}\n", `{"ts":"invalid"}` + "\n", `{"ts":"` + FormatLineTS(100) + `","record":"blame","record":"full"}` + "\n"} {
		if err := ReplaceLocalFile(p, s.LivePath(p), append(append([]byte(nil), good...), bad...)); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadStream(p, s); err == nil || !strings.Contains(err.Error(), s.LivePath(p)) {
			t.Fatalf("complete live corruption silently skipped: %v", err)
		}
	}
	torn := append(append([]byte(nil), good...), []byte(`{"ts":"torn`)...)
	if err := ReplaceLocalFile(p, s.LivePath(p), torn); err != nil {
		t.Fatal(err)
	}
	data, err := ReadStream(p, s)
	if err != nil || len(data.Lines) != 1 {
		t.Fatalf("torn-tail read=%+v %v", data, err)
	}
	b, err := os.ReadFile(s.LivePath(p))
	if err != nil || !bytes.Equal(b, torn) {
		t.Fatalf("reader rewrote live evidence: %q %v", b, err)
	}
}

func TestRepositoryQuarantineRecoversCanonicalStoreLive(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	live := s.LivePath(p)
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, line, 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	source := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", cn.ChunkFile())
	if err := WriteLocalAtomic(p, source, []byte("broken\n")); err != nil {
		t.Fatal(err)
	}
	guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
	if err != nil || m.HasGap() {
		t.Fatalf("canonical store recovery=%+v %v", m, err)
	}
	data, err := ReadStream(p, s)
	if err != nil || len(data.Lines) != 1 {
		t.Fatalf("recovered union=%+v %v", data, err)
	}
	b, err := os.ReadFile(live)
	if err != nil || !bytes.Equal(b, line) {
		t.Fatal("live source was rewritten")
	}
}

func TestRepositoryIgnoresCheckoutLocalStreams(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for _, s := range []Stream{{Namespace: SessionNS, Session: "stray"}, {Namespace: MissionNS, Mission: "m", Session: "stray"}} {
		path := LiveAuditPath(checkout, s.Session)
		if s.Namespace == MissionNS {
			path = LiveMissionLogPath(checkout, s.Mission, s.Session)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		// Deliberately corrupt: a noncanonical source must neither supply lines
		// nor cause canonical reads and stream discovery to fail.
		if err := os.WriteFile(path, []byte("stray checkout-local data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := ReadStream(p, s)
		if err != nil || len(data.Lines) != 0 {
			t.Fatalf("read checkout-local %s: %+v %v", s.Key(), data, err)
		}
	}
	streams, err := ListStreams(p)
	if err != nil || len(streams) != 0 {
		t.Fatalf("discovered checkout-local streams: %+v %v", streams, err)
	}
}

func TestRepositoryInstallDirectorySyncFailureRetainsRetryBytes(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	dest := (Stream{Namespace: SessionNS, Session: "s"}).LivePath(p)
	old := syncAuditDirectory
	syncAuditDirectory = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncAuditDirectory = old })
	if err := WriteLocalAtomic(p, dest, []byte("retained\n")); err == nil {
		t.Fatal("directory durability failure hidden")
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != "retained\n" {
		t.Fatalf("installed bytes lost: %q %v", b, err)
	}
	syncAuditDirectory = old
	if err := WriteLocalAtomic(p, dest, b); err != nil {
		t.Fatalf("exact retry failed: %v", err)
	}
	if err := WriteLocalAtomic(p, dest, []byte("different\n")); err == nil {
		t.Fatal("divergent retry replaced installed history")
	}
}

func TestRepositoryQuarantineResumesEveryPersistenceBoundary(t *testing.T) {
	for failAt := 1; failAt <= 5; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			s := Stream{Namespace: SessionNS, Session: "s"}
			dir, err := ArchiveDir(p, s, "2026-11-04")
			if err != nil {
				t.Fatal(err)
			}
			cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
			source := filepath.Join(dir, cn.ChunkFile())
			line := func(ts int64) []byte {
				return []byte(`{"ts":"` + FormatLineTS(ts) + `","tool":"Read","record":"blame"}` + "\n")
			}
			original := append(line(300), []byte("corrupt tail\n")...)
			if err := WriteLocalAtomic(p, source, original); err != nil {
				t.Fatal(err)
			}
			if err := WriteLocalAtomic(p, s.LivePath(p), append(line(100), line(200)...)); err != nil {
				t.Fatal(err)
			}
			guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
			old := fsyncFile
			calls := 0
			fsyncFile = func(f *os.File) error {
				calls++
				if calls == failAt {
					return errors.New("injected quarantine fsync failure")
				}
				return f.Sync()
			}
			t.Cleanup(func() { fsyncFile = old })
			if _, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard); err == nil {
				t.Fatal("injected persistence failure hidden")
			}
			fsyncFile = old
			m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
			if err != nil || !m.HasGap() || m.UnrecoveredLast < 300 || m.VerifiedLast != 300 {
				t.Fatalf("retry=%+v %v", m, err)
			}
			data, err := ReadStream(p, s)
			if err != nil || len(data.Lines) != 2 || data.Floor != 300 {
				t.Fatalf("recovery=%+v %v", data, err)
			}
			if _, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*.corrupt"))
			if err != nil || len(files) != 1 {
				t.Fatalf("originals=%v %v", files, err)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil || !bytes.Equal(raw, original) {
				t.Fatalf("original corrupt evidence lost: %q %v", raw, err)
			}
		})
	}
}

func TestRepositoryQuarantineRepeatedDamageRetainsDistinctEvidence(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	source := filepath.Join(dir, cn.ChunkFile())
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n")
	if err := WriteLocalAtomic(p, s.LivePath(p), line); err != nil {
		t.Fatal(err)
	}
	guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
	for _, damage := range []string{"first damage\n", "second damage\n"} {
		if err := ReplaceLocalFile(p, source, []byte(damage)); err != nil {
			t.Fatal(err)
		}
		m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
		if err != nil || m.HasGap() {
			t.Fatalf("recovery=%+v %v", m, err)
		}
		data, err := ReadStream(p, s)
		if err != nil || len(data.Lines) != 1 {
			t.Fatalf("recovered stream=%+v %v", data, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*.corrupt"))
	if err != nil || len(files) != 2 {
		t.Fatalf("evidence=%v %v", files, err)
	}
	seen := map[string]bool{}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(b)] = true
	}
	if !seen["first damage\n"] || !seen["second damage\n"] {
		t.Fatalf("raw evidence changed: %v", seen)
	}
}

func TestRepositoryPublishedQuarantineInterruptedNeverLooksEmpty(t *testing.T) {
	for failAt := 1; failAt <= 6; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			s := Stream{Namespace: SessionNS, Session: "s"}
			cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
			source := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", cn.ChunkFile())
			if err := WriteLocalAtomic(p, source, []byte("damage\n")); err != nil {
				t.Fatal(err)
			}
			dir, err := ArchiveDir(p, s, "2026-11-04")
			if err != nil {
				t.Fatal(err)
			}
			guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
			old := fsyncFile
			calls := 0
			fsyncFile = func(f *os.File) error {
				calls++
				if calls == failAt {
					return errors.New("injected quarantine sync failure")
				}
				return f.Sync()
			}
			t.Cleanup(func() { fsyncFile = old })
			_, interrupted := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
			fsyncFile = old
			// Five durable files without recoverable live lines; the sixth case
			// exercises normal completion as well as the interrupted prefixes.
			if failAt <= 5 && interrupted == nil {
				t.Fatal("injected persistence error hidden")
			}
			data, err := ReadStream(p, s)
			if err == nil && len(data.Gaps) == 0 {
				t.Fatalf("interrupted retirement looks empty: %+v", data)
			}
			if _, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard); err != nil {
				t.Fatal(err)
			}
			marker, err := ReadMarker(filepath.Join(filepath.Dir(source), cn.MarkerFile()))
			if err != nil || !marker.HasGap() {
				t.Fatalf("published gap not retained: %+v %v", marker, err)
			}
		})
	}
}

func TestRepositoryLegacyIdentityConflictBeforeCopySuppression(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read","tool_input_hash":"h","delegation_id":"one"}` + "\n")
	if err := WriteLocalAtomic(p, filepath.Join(dir, "audit.jsonl"), line); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", "audit.jsonl")
	if err := WriteLocalAtomic(p, published, bytes.ReplaceAll(line, []byte("one"), []byte("two"))); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStream(p, s); err == nil || !strings.Contains(err.Error(), "legacy audit identity conflict") {
		t.Fatalf("divergent attribution hidden: %v", err)
	}
}

func TestRepositoryFrozenLegacyMultisetRemainsSeparate(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordFull)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	ts := FormatLineTS(100)
	loss := []byte(`{"ts":"` + ts + `","audit_error":"lost","future":9007199254740993}` + "\n")
	other := bytes.ReplaceAll(loss, []byte("9007199254740993"), []byte("9007199254740992"))
	legacy := append(append(append([]byte(nil), loss...), loss...), other...)
	if err := WriteLocalAtomic(p, filepath.Join(dir, "audit.jsonl"), legacy); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s")
	if err := WriteLocalAtomic(p, filepath.Join(published, "audit.jsonl"), legacy); err != nil {
		t.Fatal(err)
	}
	if err := WriteLocalAtomic(p, filepath.Join(dir, SessionChunkFile(100, 100)), []byte(`{"ts":"`+ts+`","tool":"Read"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadStream(p, s)
	if err != nil || len(data.Legacy) != 3 || len(data.Lines) != 1 {
		t.Fatalf("legacy union=%+v %v", data, err)
	}
}

func TestRepositoryEligibilityIsPartOfAppend(t *testing.T) {
	s := Stream{Namespace: SessionNS, Session: "s"}
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		p := repositoryPolicy(t, mode)
		raw := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read","record":"full","unknown":{"b":2,"a":1}}`)
		out, err := StampEligibility(p, s, 100, raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := Eligible(s, Line{TS: 100, Raw: out}); got != (mode == layout.RecordFull) {
			t.Fatalf("eligible=%v for mode %d: %s", got, mode, out)
		}
		if !bytes.Contains(out, []byte(`"unknown"`)) {
			t.Fatal("unknown payload lost")
		}
		if _, err := StampEligibility(p, s, 101, raw); err == nil {
			t.Fatal("mismatched identity accepted")
		}
	}
	if Eligible(s, Line{Raw: []byte(`{"tool":"Read"}`)}) {
		t.Fatal("legacy payload became eligible")
	}
	for _, raw := range []string{`{"Record":"full"}`, `{"record":"full","Record":"blame"}`, `{"record":true}`, `{"record":null}`} {
		if Eligible(s, Line{Raw: []byte(raw)}) {
			t.Errorf("ambiguous consent accepted: %s", raw)
		}
	}
}

func TestRepositoryMergeChecksCanonicalPayload(t *testing.T) {
	a := Line{Session: "s", TS: 100, Raw: []byte(`{"ts":"x","input":{"b":2,"a":9007199254740993}}`), Source: "archive"}
	b := Line{Session: "s", TS: 100, Raw: []byte(`{"input":{"a":9007199254740993,"b":2},"ts":"x"}`), Source: "live"}
	got, err := MergeLines([]Line{a, b})
	if err != nil || len(got) != 1 {
		t.Fatalf("merge=%v, %v", got, err)
	}
	b.Raw = []byte(`{"input":{"a":9007199254740992,"b":2},"ts":"x"}`)
	if _, err := MergeLines([]Line{a, b}); err == nil || !strings.Contains(err.Error(), "archive and live") {
		t.Fatalf("want source-bearing conflict, got %v", err)
	}
}

func TestRepositoryUnionDetectsCoveredLiveConflict(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","session":"s","tool":"Read"}` + "\n")
	if err := WriteLocalAtomic(p, filepath.Join(dir, SessionChunkFile(100, 100)), line); err != nil {
		t.Fatal(err)
	}
	if err := WriteLocalAtomic(p, s.LivePath(p), bytes.ReplaceAll(line, []byte("Read"), []byte("Edit"))); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStream(p, s); err == nil || !strings.Contains(err.Error(), "identity conflict") {
		t.Fatalf("covered live conflict hidden: %v", err)
	}
}

func TestRepositoryLocalStagingRefusesCrossDeviceAndCleans(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordFull)
	old := installLocalFile
	installLocalFile = func(_, _ string) error { return syscall.EXDEV }
	t.Cleanup(func() { installLocalFile = old })
	dest := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", SessionChunkFile(100, 100))
	if err := WriteLocalAtomic(p, dest, []byte("payload\n")); !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("want EXDEV, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination survived: %v", err)
	}
	entries, err := os.ReadDir(p.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging debris: %v %v", entries, err)
	}
	err = filepath.WalkDir(p.CheckoutRecordRoot(), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			t.Errorf("publication-tree staging file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryFindOperationChecksPayloadAndSession(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: MissionNS, Mission: "m", Session: "original"}
	raw := []byte(`{"ts":"` + FormatLineTS(100) + `","event":"reflect","operation_id":"original","record":"blame"}` + "\n")
	dir, err := ArchiveDir(p, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteLocalAtomic(p, filepath.Join(dir, MissionChunkFile(s.Session, 100, 100)), raw); err != nil {
		t.Fatal(err)
	}
	found, err := FindOperation(p, s.Mission, "original")
	if err != nil || found == nil || found.Session != s.Session || found.TS != 100 {
		t.Fatalf("lookup=%+v %v", found, err)
	}
	other := s
	other.Session = "different"
	if err := WriteLocalAtomic(p, other.LivePath(p), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := FindOperation(p, s.Mission, "original"); err == nil {
		t.Fatal("divergent operation session accepted")
	}
}

package hook

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readAuditEntries walks an audit JSONL file and decodes each line
// into an auditEntry. The reader is permissive by design — see the
// KnownFields asymmetry note in audit_log.go.
//
// Partial trailing line: a writer that crashed mid-line leaves a
// final fragment with no '\n'. The reader emits a stderr warning
// naming the line number, skips the fragment, and returns the
// well-formed entries. Phase 2 will flock-serialize appends per
// session; in phase 1, the per-line f.Sync inside writeAuditEntry
// is the only atomicity guarantee.
//
// Missing file is not an error: returns (nil, nil).
//
// On read error (permission denied, directory at path) returns the
// wrapped error and the entries decoded up to the failure.
func readAuditEntries(path string) ([]auditEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	return decodeAuditEntries(f, path)
}

// decodeAuditEntries is the testable core of readAuditEntries. The
// reader is split out so unit tests can drive it with a
// bytes.Reader, and so the io.Reader contract — not the file-open
// contract — is what the decoder is bound to.
func decodeAuditEntries(r io.Reader, source string) ([]auditEntry, error) {
	var entries []auditEntry
	err := walkAuditLines(r, source, func(_ []byte, e auditEntry) {
		entries = append(entries, e)
	})
	return entries, err
}

// walkAuditLines is the shared JSONL line walker behind the decode and the
// raw readers. For each well-formed, decodable line it calls fn with the raw
// line bytes and the decoded entry. A partial trailing fragment (writer crash)
// and an undecodable line are each skipped with a stderr warning, never an
// error; only an underlying read failure returns an error.
func walkAuditLines(r io.Reader, source string, fn func(raw []byte, e auditEntry)) error {
	br := bufio.NewReader(r)
	lineNo := 0
	for {
		line, readErr := br.ReadString('\n')
		hadTerminator := strings.HasSuffix(line, "\n")
		if line != "" {
			lineNo++
			body := strings.TrimRight(line, "\n")
			body = strings.TrimRight(body, "\r")
			if !hadTerminator {
				if strings.TrimSpace(body) != "" {
					fmt.Fprintf(os.Stderr,
						"ethos: audit-log: %s: line %d: partial trailing line, skipping\n",
						source, lineNo)
				}
			} else if strings.TrimSpace(body) != "" {
				raw := []byte(body)
				e, err := decodeAuditLine(raw)
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"ethos: audit-log: %s: line %d: %v\n",
						source, lineNo, err)
				} else {
					fn(raw, e)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", source, readErr)
		}
	}
}

// rawAuditLine is one JSONL line kept as its exact on-disk bytes plus its
// dedupe key. Migration and the loss-marker scan use it so unknown fields the
// auditEntry struct drops — notably the audit_error sentinel key — survive.
type rawAuditLine struct {
	key string
	raw []byte
	// n is the line's number in the file it was read from, counting every
	// physical line; 0 when the reader does not record it.
	n int
	// note says what migration did to the line, for the notice printed when
	// the line is appended. It is nil for a line left as it was.
	note *migrateNote
}

// migrateNote is what migration did to legacy line n, as the kinds of rewrite
// that applied: never a value.
type migrateNote struct {
	n     int
	kinds []string
}

// readRawAuditLines walks an audit JSONL file returning each well-formed line's
// raw bytes and dedupe key. Torn/undecodable lines are skipped with a warning,
// matching readAuditEntries. Missing file → (nil, nil).
func readRawAuditLines(path string) ([]rawAuditLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	var out []rawAuditLine
	err = walkAuditLines(f, path, func(raw []byte, e auditEntry) {
		out = append(out, rawAuditLine{key: rawAuditLineKey(e, raw), raw: raw})
	})
	return out, err
}

// readLegacyAuditLines reads every non-blank physical line of a legacy audit
// file, including one that does not decode as an entry and a torn trailing
// fragment, so that migration can route each through the guard instead of
// losing it. A line that does not decode has an empty key, which migration
// replaces once the guard has made it a line that does. Missing file → nil.
func readLegacyAuditLines(path string) ([]rawAuditLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	var out []rawAuditLine
	br := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, readErr := br.ReadString('\n')
		body := strings.TrimRight(strings.TrimRight(line, "\n"), "\r")
		if strings.TrimSpace(body) != "" {
			raw := []byte(body)
			l := rawAuditLine{raw: raw, n: n}
			if e, err := decodeAuditLine(raw); err == nil {
				l.key = rawAuditLineKey(e, raw)
			}
			out = append(out, l)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("reading %s: %w", path, readErr)
		}
	}
}

// decodeAuditLine decodes one JSONL line into an auditEntry. The
// decode is permissive — unknown fields are accepted so a v3.11.0
// reader handles a v3.12.0 line, and a v3.12.0 reader handles a
// v3.11.0 line (the new fields decode to zero-value).
func decodeAuditLine(raw []byte) (auditEntry, error) {
	var e auditEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return auditEntry{}, fmt.Errorf("decoding: %w", err)
	}
	return e, nil
}

// auditEntriesEqual is a deep-equality test on two auditEntry slices
// used by the round-trip tests. Exposed so the test for
// readAuditEntries does not have to reproduce the comparison logic
// inline. Returns nil when equal; otherwise an error naming the
// first diverging field.
func auditEntriesEqual(want, got []auditEntry) error {
	if len(want) != len(got) {
		return fmt.Errorf("entry count: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		wantData, err := json.Marshal(want[i])
		if err != nil {
			return fmt.Errorf("entry %d: marshaling want: %w", i, err)
		}
		gotData, err := json.Marshal(got[i])
		if err != nil {
			return fmt.Errorf("entry %d: marshaling got: %w", i, err)
		}
		if !bytes.Equal(wantData, gotData) {
			return fmt.Errorf("entry %d: want %s, got %s",
				i, string(wantData), string(gotData))
		}
	}
	return nil
}

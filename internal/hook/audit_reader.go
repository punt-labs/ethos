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
// well-formed entries. This is the failure mode DES-054
// I10-audit-atomic accepts — appends are flock-serialized per
// session, but a power loss can still truncate the most recent line.
//
// Missing file is not an error: returns an empty slice and nil.
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
	br := bufio.NewReader(r)
	var entries []auditEntry
	lineNo := 0
	for {
		line, readErr := br.ReadString('\n')
		hadTerminator := strings.HasSuffix(line, "\n")
		if line != "" {
			lineNo++
			body := strings.TrimRight(line, "\n")
			body = strings.TrimRight(body, "\r")
			if !hadTerminator {
				// Partial trailing fragment. The writer either
				// crashed mid-line or the file was truncated. Emit
				// a warning so an operator running -v sees the
				// drift, then drop the fragment.
				if strings.TrimSpace(body) != "" {
					fmt.Fprintf(os.Stderr,
						"ethos: audit-log: %s: line %d: partial trailing line, skipping\n",
						source, lineNo)
				}
			} else if strings.TrimSpace(body) != "" {
				e, err := decodeAuditLine([]byte(body))
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"ethos: audit-log: %s: line %d: %v\n",
						source, lineNo, err)
				} else {
					entries = append(entries, e)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return entries, nil
			}
			return entries, fmt.Errorf("reading %s: %w", source, readErr)
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

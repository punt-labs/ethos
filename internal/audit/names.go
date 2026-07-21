// Package audit holds the DES-058 sealed-chunk primitives shared by the
// session audit log (internal/hook) and the mission event log
// (internal/mission): the chunk-name grammar, timestamp encoding, the
// live-zone paths and flock, the sealed-directory scan and watermark, the
// generic chunk seal, and the union read. It is line-type-agnostic — it
// operates on raw JSONL bytes plus a timestamp field — so both a session
// audit line and a mission Event share one mechanism without one package
// importing the other.
package audit

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A sealed chunk is an immutable tracked file holding a contiguous run of a
// session's audit or mission-log lines. Its name encodes the first and last
// line timestamps as Unix nanoseconds, zero-padded to 19 digits, so a plain
// directory listing sorts chronologically and the watermark is computable
// from names alone. Two grammars share one shape:
//
//	session namespace:  audit-<first>-<last>.jsonl
//	mission namespace:  log-<session-id>-<first>-<last>.jsonl
//
// The mission name carries the sealing session id because every session
// seals into the one shared missions/<id>/ directory; the session id
// supplies the cross-session separation an audit chunk gets from its own
// dated directory.

// Digits is the zero-padded width of a Unix-nanosecond timestamp in a chunk
// filename. 19 digits covers int64 nanoseconds through the year 2262 and
// makes fixed-width names sort lexically in numeric order.
const Digits = 19

// Kind classifies a directory entry within a chunk namespace.
type Kind int

const (
	// KindOther is a sibling outside the chunk namespace — a frozen legacy
	// audit.jsonl/log.jsonl, a mission contract.yaml, any unrelated file.
	KindOther Kind = iota
	// KindValid is a well-formed chunk file.
	KindValid
	// KindNearMiss carries the namespace's chunk prefix but fails to parse
	// as the full shape. A seal must fail loud rather than skip it:
	// skipping would drop its <last> from the watermark.
	KindNearMiss
	// KindCorrupt is a retired chunk (<stem>.jsonl.corrupt or
	// .corrupt-<hash>), recognized only under a covering marker.
	KindCorrupt
	// KindQuarantine is a quarantine marker (<stem>.quarantine).
	KindQuarantine
	// KindTemp is a stale temp file (.<stem>.jsonl.tmp).
	KindTemp
)

// Namespace distinguishes the two chunk grammars.
type Namespace int

const (
	SessionNS Namespace = iota
	MissionNS
)

// ChunkName is the parsed identity of a chunk filename. Session is empty in
// the session namespace (the directory supplies it there); it carries the
// sealing session id in the mission namespace.
type ChunkName struct {
	Namespace Namespace
	Session   string
	First     int64
	Last      int64
}

// TSToField formats a Unix-nanosecond timestamp as a fixed-width zero-padded
// decimal string.
func TSToField(ns int64) string {
	return fmt.Sprintf("%0*d", Digits, ns)
}

// SessionChunkFile returns the session-namespace chunk filename.
func SessionChunkFile(first, last int64) string {
	return "audit-" + TSToField(first) + "-" + TSToField(last) + ".jsonl"
}

// MissionChunkFile returns the mission-namespace chunk filename.
func MissionChunkFile(session string, first, last int64) string {
	return "log-" + session + "-" + TSToField(first) + "-" + TSToField(last) + ".jsonl"
}

// ChunkFile returns the chunk filename for a parsed name in its namespace.
func (c ChunkName) ChunkFile() string {
	if c.Namespace == MissionNS {
		return MissionChunkFile(c.Session, c.First, c.Last)
	}
	return SessionChunkFile(c.First, c.Last)
}

// Stem returns the chunk name without its .jsonl suffix — the base shared by
// the chunk, its .corrupt artifacts, and its .quarantine marker.
func (c ChunkName) Stem() string {
	if c.Namespace == MissionNS {
		return "log-" + c.Session + "-" + TSToField(c.First) + "-" + TSToField(c.Last)
	}
	return "audit-" + TSToField(c.First) + "-" + TSToField(c.Last)
}

// MarkerFile returns the quarantine marker filename for a parsed name.
func (c ChunkName) MarkerFile() string {
	return c.Stem() + ".quarantine"
}

// SessionTempFile and MissionTempFile return the dotted temp name a seal
// writes before its atomic rename. The name embeds the range so a widened
// tail after a crash yields a different temp name.
func SessionTempFile(first, last int64) string {
	return "." + SessionChunkFile(first, last) + ".tmp"
}

func MissionTempFile(session string, first, last int64) string {
	return "." + MissionChunkFile(session, first, last) + ".tmp"
}

// Classify classifies a filename within a namespace directory. The prefix
// decides candidacy: "audit-" in a session dir, "log-" in a mission dir.
func Classify(name string, ns Namespace) (ChunkName, Kind) {
	prefix := chunkPrefix(ns)

	if strings.HasPrefix(name, "."+prefix) && strings.HasSuffix(name, ".jsonl.tmp") {
		return ChunkName{Namespace: ns}, KindTemp
	}
	if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".quarantine") {
		stem := strings.TrimSuffix(name, ".quarantine")
		if cn, ok := parseChunkStem(stem, ns); ok {
			return cn, KindQuarantine
		}
		return ChunkName{Namespace: ns}, KindNearMiss
	}
	if strings.HasPrefix(name, prefix) && strings.Contains(name, ".jsonl.corrupt") {
		idx := strings.Index(name, ".jsonl.corrupt")
		if cn, ok := parseChunkStem(name[:idx], ns); ok {
			return cn, KindCorrupt
		}
		return ChunkName{Namespace: ns}, KindNearMiss
	}
	if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jsonl") {
		stem := strings.TrimSuffix(name, ".jsonl")
		if cn, ok := parseChunkStem(stem, ns); ok {
			return cn, KindValid
		}
		return ChunkName{Namespace: ns}, KindNearMiss
	}
	if strings.HasPrefix(name, prefix) {
		return ChunkName{Namespace: ns}, KindNearMiss
	}
	return ChunkName{Namespace: ns}, KindOther
}

// chunkPrefix returns the leading token of a namespace's chunk names.
func chunkPrefix(ns Namespace) string {
	if ns == MissionNS {
		return "log-"
	}
	return "audit-"
}

// parseChunkStem parses a chunk stem into a ChunkName. Returns ok=false when
// the stem does not match the namespace's full grammar.
func parseChunkStem(stem string, ns Namespace) (ChunkName, bool) {
	if ns == MissionNS {
		rest, ok := strings.CutPrefix(stem, "log-")
		if !ok {
			return ChunkName{}, false
		}
		firstStr, lastStr, session, ok := splitTrailingTwo(rest)
		if !ok || session == "" {
			return ChunkName{}, false
		}
		first, last, ok := parseTSPair(firstStr, lastStr)
		if !ok {
			return ChunkName{}, false
		}
		return ChunkName{Namespace: ns, Session: session, First: first, Last: last}, true
	}
	rest, ok := strings.CutPrefix(stem, "audit-")
	if !ok {
		return ChunkName{}, false
	}
	firstStr, lastStr, ok := strings.Cut(rest, "-")
	if !ok {
		return ChunkName{}, false
	}
	first, last, ok := parseTSPair(firstStr, lastStr)
	if !ok {
		return ChunkName{}, false
	}
	return ChunkName{Namespace: ns, First: first, Last: last}, true
}

// splitTrailingTwo splits "<session>-<first>-<last>" into its three parts,
// where <session> may contain hyphens. The two timestamp fields are the last
// two hyphen-delimited runs.
func splitTrailingTwo(s string) (first, last, prefix string, ok bool) {
	lastHyphen := strings.LastIndexByte(s, '-')
	if lastHyphen < 0 {
		return "", "", "", false
	}
	last = s[lastHyphen+1:]
	head := s[:lastHyphen]
	firstHyphen := strings.LastIndexByte(head, '-')
	if firstHyphen < 0 {
		return "", "", "", false
	}
	first = head[firstHyphen+1:]
	prefix = head[:firstHyphen]
	return first, last, prefix, true
}

// parseTSPair parses two chunk timestamp fields, each exactly Digits digits.
func parseTSPair(firstStr, lastStr string) (first, last int64, ok bool) {
	f, ok := parseField(firstStr)
	if !ok {
		return 0, 0, false
	}
	l, ok := parseField(lastStr)
	if !ok {
		return 0, 0, false
	}
	return f, l, true
}

// parseField parses one zero-padded timestamp field.
func parseField(s string) (int64, bool) {
	if len(s) != Digits {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// tsFormat is the on-disk timestamp format for a live line: RFC3339 with
// nanosecond precision so a line's ts round-trips to the Unix-nanosecond
// integer a chunk name encodes, while staying human-readable and
// backward-compatible with legacy RFC3339 lines (parsed at second
// granularity).
const tsFormat = time.RFC3339Nano

// ParseLineTS parses a line's ts field to Unix nanoseconds, accepting both
// RFC3339Nano (post-discipline) and plain RFC3339 (legacy) forms.
func ParseLineTS(ts string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0, fmt.Errorf("parsing ts %q: %w", ts, err)
	}
	return t.UnixNano(), nil
}

// FormatLineTS renders a Unix-nanosecond timestamp as the on-disk ts string.
func FormatLineTS(ns int64) string {
	return time.Unix(0, ns).UTC().Format(tsFormat)
}

// tsHolder decodes just the ts field of any audit or event JSONL line.
type tsHolder struct {
	TS string `json:"ts"`
}

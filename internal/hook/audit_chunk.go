package hook

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A sealed chunk is an immutable tracked file holding a contiguous run
// of a session's audit or mission-log lines. Its name encodes the first
// and last line timestamps as Unix nanoseconds, zero-padded to 19
// digits, so a plain directory listing sorts chronologically and the
// watermark (§Watermark in docs/audit-seal.md) is computable from names
// alone. Two grammars share one shape:
//
//	session namespace:  audit-<first>-<last>.jsonl
//	mission namespace:  log-<session-id>-<first>-<last>.jsonl
//
// The mission name carries the sealing session id because every session
// seals into the one shared missions/<id>/ directory; the session id
// supplies the cross-session separation an audit chunk gets from its
// own dated directory.

// chunkDigits is the zero-padded width of a Unix-nanosecond timestamp in
// a chunk filename. 19 digits covers int64 nanoseconds through the year
// 2262 and makes fixed-width names sort lexically in numeric order.
const chunkDigits = 19

// chunkKind classifies a directory entry within a chunk namespace.
type chunkKind int

const (
	// chunkOther is a sibling outside the chunk namespace — a frozen
	// legacy audit.jsonl/log.jsonl, a mission contract.yaml, any
	// unrelated file. The watermark scan and staging ignore it.
	chunkOther chunkKind = iota
	// chunkValid is a well-formed chunk file.
	chunkValid
	// chunkNearMiss carries the namespace's chunk prefix but fails to
	// parse as the full shape. A seal must fail loud (exit 2) rather
	// than skip it: skipping would drop its <last> from the watermark.
	chunkNearMiss
	// chunkCorrupt is a retired chunk (<stem>.jsonl.corrupt or
	// .corrupt-<hash>), recognized only under a covering quarantine
	// marker.
	chunkCorrupt
	// chunkQuarantine is a quarantine marker (<stem>.quarantine).
	chunkQuarantine
	// chunkTemp is a stale temp file (.<stem>.jsonl.tmp).
	chunkTemp
)

// chunkNamespace distinguishes the two chunk grammars.
type chunkNamespace int

const (
	sessionNamespace chunkNamespace = iota
	missionNamespace
)

// chunkName is the parsed identity of a chunk filename. Session is empty
// in the session namespace (the directory supplies it there); it carries
// the sealing session id in the mission namespace.
type chunkName struct {
	Namespace chunkNamespace
	Session   string
	First     int64
	Last      int64
}

// tsToChunkField formats a Unix-nanosecond timestamp as a fixed-width
// zero-padded decimal string. Negative timestamps (pre-1970) cannot
// arise from a monotonic session clock; they are formatted with the
// sign kept and are simply not fixed-width, which never happens in
// practice.
func tsToChunkField(ns int64) string {
	return fmt.Sprintf("%0*d", chunkDigits, ns)
}

// sessionChunkFile returns the session-namespace chunk filename for a
// timestamp range.
func sessionChunkFile(first, last int64) string {
	return "audit-" + tsToChunkField(first) + "-" + tsToChunkField(last) + ".jsonl"
}

// missionChunkFile returns the mission-namespace chunk filename for a
// session and timestamp range.
func missionChunkFile(session string, first, last int64) string {
	return "log-" + session + "-" + tsToChunkField(first) + "-" + tsToChunkField(last) + ".jsonl"
}

// sessionTempFile and missionTempFile return the dotted temp name a seal
// writes before its atomic rename. The name embeds the range so a
// widened tail after a crash yields a different temp name — the stale
// temp is never overwritten naturally and is swept by range-older-than
// cleanup.
func sessionTempFile(first, last int64) string {
	return "." + sessionChunkFile(first, last) + ".tmp"
}

func missionTempFile(session string, first, last int64) string {
	return "." + missionChunkFile(session, first, last) + ".tmp"
}

// classifySessionChunk classifies a filename in a session-namespace
// directory. See chunkKind for the categories.
func classifySessionChunk(name string) (chunkName, chunkKind) {
	return classifyChunk(name, sessionNamespace)
}

// classifyMissionChunk classifies a filename in a mission-namespace
// directory.
func classifyMissionChunk(name string) (chunkName, chunkKind) {
	return classifyChunk(name, missionNamespace)
}

// classifyChunk routes a filename to its namespace classifier. The
// prefix decides candidacy: "audit-" in a session dir, "log-" in a
// mission dir. Temp files (leading dot) and namespace artifacts are
// recognized before the plain-chunk parse.
func classifyChunk(name string, ns chunkNamespace) (chunkName, chunkKind) {
	prefix := chunkPrefix(ns)

	// Temp file: .<stem>.jsonl.tmp — a leading dot plus the chunk
	// prefix. Checked first because the dot hides the prefix from the
	// candidate test below.
	if strings.HasPrefix(name, "."+prefix) && strings.HasSuffix(name, ".jsonl.tmp") {
		return chunkName{Namespace: ns}, chunkTemp
	}

	// Quarantine marker: <stem>.quarantine.
	if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".quarantine") {
		stem := strings.TrimSuffix(name, ".quarantine")
		if cn, ok := parseChunkStem(stem, ns); ok {
			return cn, chunkQuarantine
		}
		return chunkName{Namespace: ns}, chunkNearMiss
	}

	// Corrupt artifact: <stem>.jsonl.corrupt or .jsonl.corrupt-<hash>.
	if strings.HasPrefix(name, prefix) &&
		(strings.Contains(name, ".jsonl.corrupt")) {
		// Recover the stem: strip from ".jsonl.corrupt" onward.
		idx := strings.Index(name, ".jsonl.corrupt")
		stem := name[:idx]
		if cn, ok := parseChunkStem(stem, ns); ok {
			return cn, chunkCorrupt
		}
		return chunkName{Namespace: ns}, chunkNearMiss
	}

	// Plain chunk: <stem>.jsonl.
	if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jsonl") {
		stem := strings.TrimSuffix(name, ".jsonl")
		if cn, ok := parseChunkStem(stem, ns); ok {
			return cn, chunkValid
		}
		return chunkName{Namespace: ns}, chunkNearMiss
	}

	// Any remaining name carrying the chunk prefix but no recognized
	// suffix is a near-miss — a file that could only have been a chunk.
	if strings.HasPrefix(name, prefix) {
		return chunkName{Namespace: ns}, chunkNearMiss
	}
	return chunkName{Namespace: ns}, chunkOther
}

// chunkPrefix returns the leading token of a namespace's chunk names.
func chunkPrefix(ns chunkNamespace) string {
	if ns == missionNamespace {
		return "log-"
	}
	return "audit-"
}

// parseChunkStem parses a chunk stem (the name without its .jsonl or
// artifact suffix) into a chunkName. Returns ok=false when the stem does
// not match the namespace's full grammar.
//
// session stem:  audit-<first>-<last>
// mission stem:  log-<session-id>-<first>-<last>
func parseChunkStem(stem string, ns chunkNamespace) (chunkName, bool) {
	if ns == missionNamespace {
		rest, ok := strings.CutPrefix(stem, "log-")
		if !ok {
			return chunkName{}, false
		}
		// The last two hyphen-separated fields are the timestamps; the
		// session id is everything before them and may itself contain
		// hyphens. Split off the two trailing fields from the right.
		firstStr, lastStr, session, ok := splitTrailingTwo(rest)
		if !ok {
			return chunkName{}, false
		}
		if session == "" {
			return chunkName{}, false
		}
		first, last, ok := parseTSPair(firstStr, lastStr)
		if !ok {
			return chunkName{}, false
		}
		return chunkName{Namespace: ns, Session: session, First: first, Last: last}, true
	}

	rest, ok := strings.CutPrefix(stem, "audit-")
	if !ok {
		return chunkName{}, false
	}
	firstStr, lastStr, ok := strings.Cut(rest, "-")
	if !ok {
		return chunkName{}, false
	}
	first, last, ok := parseTSPair(firstStr, lastStr)
	if !ok {
		return chunkName{}, false
	}
	return chunkName{Namespace: ns, First: first, Last: last}, true
}

// splitTrailingTwo splits "<session>-<first>-<last>" into its three
// parts, where <session> may contain hyphens. Returns first, last,
// session and ok. The two timestamp fields are the last two
// hyphen-delimited runs.
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

// parseTSPair parses two chunk timestamp fields. Each must be exactly
// chunkDigits digits so a malformed short or non-numeric field is a
// near-miss, not a silently accepted chunk.
func parseTSPair(firstStr, lastStr string) (first, last int64, ok bool) {
	f, ok := parseChunkField(firstStr)
	if !ok {
		return 0, 0, false
	}
	l, ok := parseChunkField(lastStr)
	if !ok {
		return 0, 0, false
	}
	return f, l, true
}

// parseChunkField parses one zero-padded timestamp field. It must be
// exactly chunkDigits characters, all digits.
func parseChunkField(s string) (int64, bool) {
	if len(s) != chunkDigits {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// tsFormat is the on-disk timestamp format for a live audit line. It is
// RFC3339 with nanosecond precision so a line's ts round-trips to the
// Unix-nanosecond integer a chunk name encodes, while staying
// human-readable and backward-compatible with legacy RFC3339 lines
// (which parse at second granularity).
const tsFormat = time.RFC3339Nano

// parseLineTS parses an audit or event line's ts field to Unix
// nanoseconds. It accepts RFC3339Nano (post-discipline lines) and plain
// RFC3339 (legacy lines) so a frozen legacy file still yields a
// comparable timestamp.
func parseLineTS(ts string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0, fmt.Errorf("parsing ts %q: %w", ts, err)
	}
	return t.UnixNano(), nil
}

// formatLineTS renders a Unix-nanosecond timestamp as the on-disk ts
// string. The result parses back to the same integer via parseLineTS.
func formatLineTS(ns int64) string {
	return time.Unix(0, ns).UTC().Format(tsFormat)
}

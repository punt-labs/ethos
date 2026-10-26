package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// QuarantineChunk retires a corrupt sealed chunk named by its path and
// recovers what the live file still holds (DES-058 §Seal failure policy). It
// resolves the chunk's namespace, session, and live file from the path, then
// runs the quarantine under the matching live-zone flock so a concurrent seal
// or append cannot interleave.
//
// chunkPath is the path the seal/read error named — a session chunk under
// .punt-labs/ethos/sessions/<date>-<sid>/ or a mission chunk under
// .punt-labs/ethos/missions/<id>/. Returns the marker's summary for the CLI.
func QuarantineChunk(repoRoot, chunkPath, reason string) (audit.Marker, error) {
	// Quarantine is an operator command: with no usable redactor it
	// refuses rather than write a placeholder reason.
	pr, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		return audit.Marker{}, fmt.Errorf("quarantine: building path redactor: %w", err)
	}
	sealedDir := filepath.Dir(chunkPath)
	name := filepath.Base(chunkPath)

	if cn, kind := audit.Classify(name, audit.SessionNS); kind == audit.KindValid {
		sessionID := sessionIDFromDir(filepath.Base(sealedDir))
		if sessionID == "" {
			return audit.Marker{}, fmt.Errorf(
				"quarantine: cannot resolve session from directory %q", sealedDir)
		}
		livePath := liveAuditPath(repoRoot, sessionID)
		redactReason := quarantineReason(pr)
		var marker audit.Marker
		err := WithLiveAuditLock(repoRoot, sessionID, func() error {
			var e error
			marker, e = audit.Quarantine(repoRoot, sealedDir, cn, livePath, reason, redactReason, guardAuditLineNote, corruptRule)
			return e
		})
		return marker, err
	}

	if cn, kind := audit.Classify(name, audit.MissionNS); kind == audit.KindValid {
		missionID := filepath.Base(sealedDir)
		livePath := liveMissionLogPath(repoRoot, missionID, cn.Session)
		redactReason := quarantineReason(pr)
		guard := missionLineGuard(repoRoot)
		guardLine := func(l []byte) ([]byte, string) {
			out, rule, marker := guard(l)
			return out, auditGuardNotice(rule, marker)
		}
		var marker audit.Marker
		err := WithLiveMissionLock(repoRoot, missionID, cn.Session, func() error {
			var e error
			marker, e = audit.Quarantine(repoRoot, sealedDir, cn, livePath, reason, redactReason, guardLine, corruptRule)
			return e
		})
		return marker, err
	}

	return audit.Marker{}, fmt.Errorf(
		"quarantine: %q is not a recognizable audit or mission chunk name", name)
}

// quarantineReason is the redaction of a quarantine reason: the content pass,
// then each word that still names a home (an encoded spelling the pass does not
// decode) is replaced alone, so the rest of the reason survives. When no single
// word can be isolated the reason becomes a fixed text. Either edit prints a
// notice, which does not quote the reason. The marker is staged, so the reason
// is never written unchecked; the quarantine is not refused for it.
func quarantineReason(pr mission.PathRedactor) func(string) string {
	leaks := func(s string) bool { return mission.LeakRule(s) != "" || pr.Leaks(s) }
	return func(s string) string {
		c := pr.Content(s)
		if !leaks(c) {
			return c
		}
		words := strings.Fields(c)
		for i, w := range words {
			core := strings.TrimRight(w, ":,;.)")
			if core != "" && leaks(core) {
				words[i] = "[redacted-path]" + w[len(core):]
			}
		}
		out := strings.Join(words, " ")
		if leaks(out) {
			out = "reason withheld: it named a home directory"
		}
		fmt.Fprintln(os.Stderr, "ethos: quarantine reason edited: it named a home directory")
		return out
	}
}

// corruptRule names the rule the bytes of a retired chunk break, or returns "".
// The bytes are corrupt, so they are judged raw and, line by line, decoded:
// this machine's home, another user's, a credential or an address.
func corruptRule(b []byte) string {
	if r := mission.LeakRule(string(b)); r != "" {
		return r
	}
	if r := secretRule(string(b)); r != "" {
		return r
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if r := lineRule(l); r != "" {
			return r
		}
		if r := decodedSecretRule(l); r != "" {
			return r
		}
		if r := unescapedSecretRule(l); r != "" {
			return r
		}
	}
	return ""
}

// secretRule names the credential or address s holds, without quoting it.
func secretRule(s string) string {
	if redact.SecretsString(s) != s {
		return "holds a credential"
	}
	if redact.EmailPattern.MatchString(s) {
		return "holds an email address"
	}
	return ""
}

// decodedSecretRule is secretRule over every decoded string and key of every
// JSON value on a line, to the end of the line or the first value that does not
// decode (the raw bytes were judged by the caller). It names nothing for a line
// with no JSON value.
func decodedSecretRule(line []byte) string {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	for {
		var v any
		if dec.Decode(&v) != nil {
			return ""
		}
		if r := valueSecretRule(v); r != "" {
			return r
		}
	}
}

// memberRE finds a "key": value member in text: the key, and the start of a
// value that is a non-empty string, a number, or a non-empty list or object.
//
// Groups: 1 the key; 2 a string value (closed, or cut off at the end of the
// text), whose text is group 3; 4 the first digit of a number. A value that is a
// list or object sets none of 2 to 4.
var memberRE = regexp.MustCompile(`"([^"]+)"\s*:\s*(?:("([^"]*)(?:"|$))|\[\s*[^\]\s]|\{\s*[^}\s]|([0-9]))`)

// counterSuffixes end the name of a key that counts or measures something
// (input_tokens, secret_count): a number under it is not a credential.
var counterSuffixes = map[string]bool{
	"count": true, "tokens": true, "total": true, "len": true, "length": true, "size": true,
	"limit": true, "max": true, "min": true, "num": true, "number": true, "ttl": true,
}

// counterKey reports whether name ends in a counter suffix after _ or -.
func counterKey(name string) bool {
	low := strings.ToLower(name)
	if i := strings.LastIndexAny(low, "_-"); i >= 0 {
		low = low[i+1:]
	}
	return counterSuffixes[low]
}

// unescapedSecretRule judges the whole raw line with its JSON escapes
// resolved, for the part of a corrupt chunk no decoder reaches: a truncated
// line, text before or after a value. It applies the home, credential and
// address checks to the text, and to every "key": value member in it, where a
// credential-named key fires on a non-empty string, list or object, and on a
// number unless the key counts something (counterKey).
func unescapedSecretRule(line []byte) string {
	text := unescapeJSON(line)
	if r := mission.LeakRule(text); r != "" {
		return r
	}
	if r := secretRule(text); r != "" {
		return r
	}
	for _, m := range memberRE.FindAllStringSubmatch(text, -1) {
		if redact.IsSecretKey(m[1]) {
			switch {
			case m[2] != "": // a string, closed or cut off: any character fires
				if m[3] != "" {
					return "holds a credential"
				}
			case m[4] != "": // a number: not a counter
				if !counterKey(m[1]) {
					return "holds a credential"
				}
			default: // a non-empty list or object
				return "holds a credential"
			}
		}
		if m[3] != "" {
			if r := secretRule(m[1] + "=" + m[3]); r != "" {
				return r
			}
		}
	}
	return ""
}

// unescapeJSON resolves the escapes a JSON string can hold in b, without
// parsing it: \uXXXX (a surrogate pair as one rune), \/, \", \\, \b, \f, \n, \r
// and \t. A malformed escape is left as it is.
func unescapeJSON(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			out.WriteByte(b[i])
			continue
		}
		switch c := b[i+1]; c {
		case '/', '"', '\\':
			out.WriteByte(c)
			i++
		case 'b':
			out.WriteByte('\b')
			i++
		case 'f':
			out.WriteByte('\f')
			i++
		case 'n':
			out.WriteByte('\n')
			i++
		case 'r':
			out.WriteByte('\r')
			i++
		case 't':
			out.WriteByte('\t')
			i++
		case 'u':
			r, n := hex4(b, i+2)
			if n == 0 {
				out.WriteByte(b[i])
				continue
			}
			i += 1 + n
			if utf16.IsSurrogate(r) && i+6 < len(b) && b[i+1] == '\\' && b[i+2] == 'u' {
				// A high surrogate followed by \uXXXX: one rune if they pair.
				if r2, n2 := hex4(b, i+3); n2 > 0 {
					if d := utf16.DecodeRune(r, r2); d != unicode.ReplacementChar {
						out.WriteRune(d)
						i += 6
						continue
					}
				}
			}
			out.WriteRune(r)
		default:
			out.WriteByte(b[i])
		}
	}
	return out.String()
}

// hex4 parses four hex digits at b[at:], returning the rune and 4, or 0, 0.
func hex4(b []byte, at int) (rune, int) {
	if at+4 > len(b) {
		return 0, 0
	}
	v, err := strconv.ParseUint(string(b[at:at+4]), 16, 32)
	if err != nil {
		return 0, 0
	}
	return rune(v), 4
}

// pairSecretRule is secretRule for a decoded member whose key and scalar value
// are judged together, as they were in the raw bytes before JSON escapes hid the
// pair: "GITHUB\u005fTOKEN":"x" is a credential-named key with a value.
func pairSecretRule(k string, v any) string {
	var val string
	switch x := v.(type) {
	case string:
		val = x
	case json.Number:
		if counterKey(k) {
			return ""
		}
		val = x.String()
	case []any:
		if len(x) > 0 && redact.IsSecretKey(k) {
			return "holds a credential"
		}
		return ""
	case map[string]any:
		if len(x) > 0 && redact.IsSecretKey(k) {
			return "holds a credential"
		}
		return ""
	default:
		return ""
	}
	if val == "" {
		return ""
	}
	if r := secretRule(k + "=" + val); r != "" {
		return r
	}
	return secretRule(`"` + k + `": "` + val + `"`)
}

func valueSecretRule(v any) string {
	switch x := v.(type) {
	case string:
		return secretRule(x)
	case []any:
		for _, e := range x {
			if r := valueSecretRule(e); r != "" {
				return r
			}
		}
	case map[string]any:
		// In key order, so the rule a notice names is the same every run.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if r := secretRule(k); r != "" {
				return r
			}
			if r := pairSecretRule(k, x[k]); r != "" {
				return r
			}
			if r := valueSecretRule(x[k]); r != "" {
				return r
			}
		}
	}
	return ""
}

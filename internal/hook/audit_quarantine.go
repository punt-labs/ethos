package hook

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
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
	for _, l := range bytes.Split(b, []byte("\n")) {
		if r := lineRule(l); r != "" {
			return r
		}
	}
	return ""
}

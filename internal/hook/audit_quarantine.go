package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
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
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return audit.Marker{}, err
	}
	return QuarantineChunkWithPolicy(p, chunkPath, reason)
}

// QuarantineChunkWithPolicy is a local recovery operation in both modes. A
// subsequent full seal publishes eligible recovered lines separately.
func QuarantineChunkWithPolicy(p layout.Policy, chunkPath, reason string) (audit.Marker, error) {
	chunkPath = filepath.Clean(chunkPath)
	for _, ns := range []audit.Namespace{audit.SessionNS, audit.MissionNS} {
		cn, kind := audit.Classify(filepath.Base(chunkPath), ns)
		if kind != audit.KindValid {
			continue
		}
		s := audit.Stream{Namespace: ns}
		if ns == audit.SessionNS {
			s.Session = sessionIDFromDir(filepath.Base(filepath.Dir(chunkPath)))
		} else {
			s.Mission, s.Session = filepath.Base(filepath.Dir(chunkPath)), cn.Session
		}
		dirs, err := audit.StreamDirs(p, s)
		if err != nil {
			return audit.Marker{}, err
		}
		allowed := false
		for _, d := range dirs {
			allowed = allowed || d == filepath.Dir(chunkPath)
		}
		if !allowed {
			return audit.Marker{}, fmt.Errorf("quarantine path outside stream's archive and record trees: %s", chunkPath)
		}
		checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
		pr, err := mission.NewPathRedactor(checkout)
		if err != nil {
			return audit.Marker{}, err
		}
		dir, err := audit.ArchiveDir(p, s, time.Now().UTC().Format(audit.SessionDateFormat))
		if err != nil {
			return audit.Marker{}, err
		}
		var marker audit.Marker
		guard := sessionLineGuard(checkout)
		if ns == audit.MissionNS {
			guard = missionLineGuard(checkout)
		}
		err = audit.WithStreamLock(p, s, func() error {
			var err error
			marker, err = audit.QuarantineLocal(p, s, chunkPath, dir, cn, quarantineReason(pr)(reason), guard)
			return err
		})
		if err == nil {
			rel, relErr := filepath.Rel(audit.ArchiveRoot(p), chunkPath)
			if relErr != nil || !filepath.IsLocal(rel) {
				// Selection authorizes retirement, never hidden index mutation or
				// promotion of a private local replacement. This two-path set is
				// sufficient to commit the conservative published availability gap.
				root := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
				if r, e := filepath.Rel(p.RecordRoot(), chunkPath); e == nil && filepath.IsLocal(r) {
					root = filepath.Dir(filepath.Dir(p.RecordRoot()))
				}
				selected, e := filepath.Rel(root, chunkPath)
				if e != nil {
					return marker, e
				}
				fmt.Fprintf(os.Stderr, "ethos: SHARED REPAIR PENDING; commit the retirement set: git -C %q add -- %q %q (then commit); recovered private lines remain local\n", root, selected, filepath.Join(filepath.Dir(selected), cn.MarkerFile()))
			}
		}
		return marker, err
	}
	return audit.Marker{}, fmt.Errorf("quarantine: unrecognizable chunk %q", chunkPath)
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

// corruptRule is the retired-chunk check: every chunk's bytes are withheld from
// the tracked tree, whatever they hold, so it returns the label audit.WithheldRule
// for any bytes but a record that audit.Quarantine already wrote. A decision that
// read the bytes could be defeated by how they are spelled or cut off.
func corruptRule(b []byte) string {
	if audit.IsWithheldRecord(b) {
		return ""
	}
	return audit.WithheldRule
}

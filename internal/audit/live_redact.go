package audit

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// LineGuard judges one line about to be stored: the line to store, the rule it
// broke ("" when it was clean), and whether it became a loss marker.
type LineGuard func(line []byte) (out []byte, rule string, marker bool)

// ReadLiveLines returns the complete lines of a stream's live file without
// checking them against the archive, so a caller can repair a conflict that
// ReadStream would refuse. A torn tail is ignored; a missing file is empty.
func ReadLiveLines(p layout.Policy, s Stream) ([]Line, error) {
	return readCompleteLive(s.LivePath(p), s.Session)
}

// LiveConflict records a live line dropped because its ts was already
// archived with a different payload. Digests are hex SHA-256 of the canonical
// payloads; the payloads themselves are never logged.
type LiveConflict struct {
	TS            int64
	ArchiveDigest string
	LiveDigest    string
}

// LiveRepair is what RedactLive changed in one live file.
type LiveRepair struct {
	Rewritten int            // lines replaced by their redacted form
	Dropped   []LiveConflict // lines removed because the archive wins
}

// Changed reports whether the live file was rewritten.
func (r LiveRepair) Changed() bool { return r.Rewritten > 0 || len(r.Dropped) > 0 }

// RedactLive makes a stream's live file safe to archive. Each complete line
// whose ts is archived with another payload is dropped (the archive wins);
// each remaining line the guard would change is replaced by the guard's
// output; every other byte, including a torn tail, stays. A line whose ts is
// archived with an equal payload is left alone: the archive already holds it.
// The caller holds the stream lock. The file is replaced atomically, and only
// when something changed.
func RedactLive(p layout.Policy, s Stream, guard LineGuard) (LiveRepair, error) {
	var res LiveRepair
	live, err := ReadLiveLines(p, s)
	if err != nil || len(live) == 0 {
		return res, err
	}
	archived, err := archivedPayloads(p, s)
	if err != nil {
		return res, err
	}
	replace := map[string][]byte{} // raw live line -> its redacted form; nil drops it
	for _, l := range live {
		got, err := CanonicalPayload(l.Raw)
		if err != nil {
			return res, err
		}
		if want, ok := archived[l.TS]; ok {
			if !bytes.Equal(want, got) {
				res.Dropped = append(res.Dropped, LiveConflict{TS: l.TS, ArchiveDigest: digest(want), LiveDigest: digest(got)})
				replace[string(l.Raw)] = nil
			}
			continue
		}
		out, _, _ := guard(l.Raw)
		after, err := CanonicalPayload(out)
		if err != nil {
			return res, fmt.Errorf("guarded audit payload at ts %d is not a valid line: %w", l.TS, err)
		}
		if !bytes.Equal(got, after) {
			replace[string(l.Raw)] = out
			res.Rewritten++
		}
	}
	if !res.Changed() {
		return res, nil
	}
	path := s.LivePath(p)
	b, err := os.ReadFile(path)
	if err != nil {
		return LiveRepair{}, fmt.Errorf("reading live audit %s: %w", path, err)
	}
	var out bytes.Buffer
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out.Write(b) // torn tail stays for the writer's reopen repair
			break
		}
		if r, ok := replace[string(b[:i])]; !ok {
			out.Write(b[:i+1])
		} else if r != nil {
			out.Write(r)
			out.WriteByte('\n')
		}
		b = b[i+1:]
	}
	if err := ReplaceLocalFile(p, path, out.Bytes()); err != nil {
		return LiveRepair{}, fmt.Errorf("replacing live audit %s with its redacted lines: %w", path, err)
	}
	return res, nil
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// archivedPayloads maps each archived ts of the stream to its canonical payload.
func archivedPayloads(p layout.Policy, s Stream) (map[int64][]byte, error) {
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return nil, err
	}
	session := ""
	if s.Namespace == MissionNS {
		session = s.Session
	}
	out := map[int64][]byte{}
	for _, dir := range dirs {
		scan, err := ScanSealedDir(dir, s.Namespace, session)
		if err != nil {
			return nil, err
		}
		for _, c := range scan.Chunks {
			lines, err := ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
			if err != nil {
				return nil, err
			}
			for _, l := range lines {
				b, err := CanonicalPayload(l.Raw)
				if err != nil {
					return nil, err
				}
				out[l.TS] = b
			}
		}
	}
	return out, nil
}

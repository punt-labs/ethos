package audit

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

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

// DropArchivedConflicts removes from the live file each complete line whose ts
// is archived with a different payload: the archive wins. The caller holds the
// stream lock. It returns the lines dropped and leaves a torn tail in place.
func DropArchivedConflicts(p layout.Policy, s Stream) ([]LiveConflict, error) {
	live, err := ReadLiveLines(p, s)
	if err != nil || len(live) == 0 {
		return nil, err
	}
	archived, err := archivedPayloads(p, s)
	if err != nil {
		return nil, err
	}
	var dropped []LiveConflict
	drop := map[string]bool{}
	for _, l := range live {
		want, ok := archived[l.TS]
		if !ok {
			continue
		}
		got, err := CanonicalPayload(l.Raw)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(want, got) {
			continue
		}
		dropped = append(dropped, LiveConflict{TS: l.TS,
			ArchiveDigest: fmt.Sprintf("%x", sha256.Sum256(want)),
			LiveDigest:    fmt.Sprintf("%x", sha256.Sum256(got))})
		drop[string(l.Raw)] = true
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	path := s.LivePath(p)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading live audit %s: %w", path, err)
	}
	var out bytes.Buffer
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out.Write(b) // torn tail stays for the writer's reopen repair
			break
		}
		if !drop[string(b[:i])] {
			out.Write(b[:i+1])
		}
		b = b[i+1:]
	}
	if err := ReplaceLocalFile(p, path, out.Bytes()); err != nil {
		return nil, fmt.Errorf("replacing live audit %s without its archived-conflict lines: %w", path, err)
	}
	return dropped, nil
}

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

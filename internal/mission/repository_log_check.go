package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// checkRepositoryEventSources checks physical source bytes before a tolerant
// audit union can discard malformed lines. A strict import must not turn a
// partially decoded history into initialized, complete correction authority.
func (s *Store) checkRepositoryEventSources(id string, strict bool) ([]string, error) {
	streams, err := s.missionStreams(id)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{} // true for the old session-tagged residue format
	for _, stream := range streams {
		paths[stream.LivePath(s.policy)] = false
		paths[audit.LiveMissionLogPath(s.auditRoot(), id, stream.Session)] = false
		dirs, err := audit.StreamDirs(s.policy, stream)
		if err != nil {
			return nil, err
		}
		for _, dir := range dirs {
			paths[filepath.Join(dir, "log.jsonl")] = false
			scan, err := audit.ScanSealedDir(dir, audit.MissionNS, stream.Session)
			if err != nil {
				return nil, err
			}
			for _, chunk := range scan.Chunks {
				paths[filepath.Join(dir, chunk.ChunkFile())] = false
			}
		}
	}
	for _, root := range []string{s.repoRoot, s.auditRoot()} {
		paths[audit.MissionResiduePath(root, id)] = true
	}
	var warnings []string
	for path, residue := range paths {
		b, err := readEventSource(path)
		if err != nil {
			return nil, err
		}
		if strict && len(b) > 0 && b[len(b)-1] != '\n' {
			return nil, fmt.Errorf("cannot import incomplete correction history: torn event tail in %s", path)
		}
		for i, line := range bytes.Split(b, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if residue {
				_, err = decodeResidueEventLine(line)
			} else {
				_, err = decodeEventLine(line)
			}
			if err != nil {
				warning := sanitizeWarning(fmt.Sprintf("%s line %d: %v", filepath.Base(path), i+1, err))
				if strict {
					return nil, fmt.Errorf("cannot import incomplete correction history: %s", warning)
				}
				warnings = append(warnings, warning)
			}
		}
	}
	return warnings, nil
}

func readEventSource(path string) ([]byte, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening event history: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLogSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading event history: %w", err)
	}
	if len(b) > maxLogSize {
		return nil, fmt.Errorf("event history exceeds %d bytes", maxLogSize)
	}
	return b, nil
}

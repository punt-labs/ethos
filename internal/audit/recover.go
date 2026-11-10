package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// recovery is the outcome of recoverLines: the lines to install, sorted by
// ts, and what could not be carried over.
type recovery struct {
	keep      []Line         // guarded lines to install, ascending ts
	conflicts []LiveConflict // lines refused because surviving evidence disagrees
	// lost is true when a non-empty damaged line does not decode, a decodable
	// line was refused for conflicting, or the recovered lines do not reach
	// the chunk's named bounds. A line held identically by another chunk is
	// not lost.
	lost   bool
	lo, hi int64 // the range the damaged bytes and the name span
	// verifiedLast is the highest parseable ts seen in the damaged bytes, the
	// live lines in range, or the kept lines; zero when none parsed.
	verifiedLast int64
}

// recoverLines is the one recovery shared by SalvageChunk and QuarantineLocal.
// It decodes the damaged bytes, guards every decodable line, adds the live
// lines of the resulting range, and drops a line whose ts another chunk of the
// stream holds with a different payload (the surviving copy wins). A line
// another chunk holds identically is kept: it is not a conflict and not a
// loss. except names the chunk being recovered so its own bytes are not
// counted as "other" evidence.
func recoverLines(p layout.Policy, s Stream, damaged []byte, cn ChunkName, except string, guard LineGuard) (recovery, error) {
	var r recovery
	r.lo, r.hi = cn.First, cn.Last
	byTS := map[int64]Line{}
	for _, raw := range SplitLines(damaged) {
		var h tsHolder
		var lineTS int64
		if json.Unmarshal(raw, &h) == nil {
			if ts, err := ParseLineTS(h.TS); err == nil {
				lineTS = ts
				if ts > r.verifiedLast {
					r.verifiedLast = ts
				}
			}
		}
		l, ok := decodeSalvageLine(raw, guard)
		if !ok {
			// A non-empty line that does not decode held an entry that is
			// gone, wherever it sat: record the loss, and widen the range to
			// its own ts when that much of it parses.
			if len(bytes.TrimSpace(raw)) > 0 {
				r.lost = true
				if lineTS > 0 && lineTS < r.lo {
					r.lo = lineTS
				}
				if lineTS > r.hi {
					r.hi = lineTS
				}
			}
			continue
		}
		if l.TS < r.lo {
			r.lo = l.TS
		}
		if l.TS > r.hi {
			r.hi = l.TS
		}
		if prev, dup := byTS[l.TS]; dup {
			// Two damaged lines at one ts: keeping the last would drop the
			// other without a trace. Keep the first and record the refused
			// payload as a loss, as a conflict with surviving evidence is.
			a, aerr := CanonicalPayload(prev.Raw)
			b, berr := CanonicalPayload(l.Raw)
			if err := errors.Join(aerr, berr); err != nil {
				return r, err
			}
			if !bytes.Equal(a, b) {
				r.conflicts = append(r.conflicts, LiveConflict{TS: l.TS, ArchiveDigest: digest(a), LiveDigest: digest(b)})
				r.lost = true
			}
			continue
		}
		byTS[l.TS] = l
	}
	live, err := ReadLiveLines(p, s)
	if err != nil {
		return r, err
	}
	for _, raw := range live {
		if raw.TS < r.lo || raw.TS > r.hi {
			continue
		}
		l, ok := decodeSalvageLine(raw.Raw, guard)
		if !ok {
			continue
		}
		if _, held := byTS[l.TS]; !held {
			byTS[l.TS] = l
		}
	}
	evidence, err := chunkEvidence(p, s, except)
	if err != nil {
		return r, err
	}
	// A line another chunk already holds identically is kept (it is this
	// chunk's own replacement from an interrupted earlier recovery, or a
	// duplicate seal) and is installed again only when the replacement would
	// otherwise be incomplete: installRecovered replaces a same-named chunk
	// with identical content, so re-installing is idempotent.
	for _, l := range byTS {
		got, err := CanonicalPayload(l.Raw)
		if err != nil {
			return r, err
		}
		if l.TS > r.verifiedLast {
			r.verifiedLast = l.TS
		}
		if want, ok := evidence[l.TS]; ok && !bytes.Equal(want, got) {
			r.conflicts = append(r.conflicts, LiveConflict{TS: l.TS, ArchiveDigest: digest(want), LiveDigest: digest(got)})
			continue
		}
		r.keep = append(r.keep, l)
	}
	sort.Slice(r.keep, func(i, j int) bool { return r.keep[i].TS < r.keep[j].TS })
	sort.Slice(r.conflicts, func(i, j int) bool { return r.conflicts[i].TS < r.conflicts[j].TS })
	if len(r.conflicts) > 0 {
		r.lost = true
	}
	// The named bounds must be reached by something kept or held in another
	// chunk (an earlier interrupted recovery's installed replacement counts);
	// otherwise the content at the bound is lost, which is what happens when
	// the damaged bytes were already deleted and the live file does not hold
	// their lines.
	covered := func(ts int64) bool {
		if _, ok := evidence[ts]; ok {
			return true
		}
		for _, l := range r.keep {
			if l.TS == ts {
				return true
			}
		}
		return false
	}
	if !covered(r.lo) || !covered(r.hi) {
		r.lost = true
	}
	return r, nil
}

// installRecovered writes the kept lines as a chunk in dir and reads it back
// through ReadChunkVerified before reporting success. It returns the path, or
// "" when there was nothing to install.
//
// When the replacement keeps the source's own name it overwrites the damaged
// bytes in place, so the body is verified in memory and beforeReplace (the
// markers that record what the replacement lacks) runs first: the raw bytes
// are never destroyed before the loss is durable. beforeReplace may be nil.
func installRecovered(p layout.Policy, dir string, cn ChunkName, keep []Line, source string, beforeReplace func() error) (string, error) {
	if len(keep) == 0 {
		return "", nil
	}
	re := ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: keep[0].TS, Last: keep[len(keep)-1].TS}
	dest := filepath.Join(dir, re.ChunkFile())
	if dest != source {
		if rest, ok, err := linesMissingFromHeldChunk(dest, keep); err != nil {
			return "", err
		} else if ok {
			return installEachLine(p, dir, cn, rest, source, dest)
		}
	}
	var body bytes.Buffer
	for _, l := range keep {
		body.Write(l.Raw)
		body.WriteByte('\n')
	}
	var err error
	if dest == source {
		if _, err := DecodeChunkStrict(body.Bytes(), dest); err != nil {
			return "", fmt.Errorf("replacement for chunk %s does not decode: %w", source, err)
		}
		if beforeReplace != nil {
			if err := beforeReplace(); err != nil {
				return "", err
			}
		}
		err = ReplaceLocalFile(p, dest, body.Bytes())
	} else {
		err = WriteLocalAtomic(p, dest, body.Bytes())
	}
	if err != nil {
		return "", fmt.Errorf("installing replacement for chunk %s: %w", source, err)
	}
	if _, err := ReadChunkVerified(dest, re.Last); err != nil {
		return "", fmt.Errorf("replacement %s for chunk %s does not read back: %w", dest, source, err)
	}
	return dest, nil
}

// linesMissingFromHeldChunk handles a replacement whose name is already taken
// by a readable chunk with different content: it returns ok and the lines of
// keep that chunk lacks. When dest is absent, or holds exactly the replacement,
// ok is false and the ordinary install applies.
func linesMissingFromHeldChunk(dest string, keep []Line) ([]Line, bool, error) {
	info, err := os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("examining %s: %w", dest, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	existing, err := os.ReadFile(dest)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", dest, err)
	}
	var body bytes.Buffer
	for _, l := range keep {
		body.Write(l.Raw)
		body.WriteByte('\n')
	}
	if bytes.Equal(existing, body.Bytes()) {
		return nil, false, nil
	}
	lines, err := DecodeChunkStrict(existing, dest)
	if err != nil {
		return nil, false, nil // damaged: the ordinary install reports the conflict
	}
	held := map[int64]bool{}
	for _, l := range lines {
		held[l.TS] = true
	}
	var rest []Line
	for _, l := range keep {
		if !held[l.TS] {
			rest = append(rest, l)
		}
	}
	return rest, true, nil
}

// installEachLine installs every line of rest as its own one-line chunk, so no
// name can collide with the chunk that already holds the neighbors. It returns
// the path of the chunk that took the place of the replacement: first of rest,
// or existing when rest is empty (the existing chunk already holds every line).
func installEachLine(p layout.Policy, dir string, cn ChunkName, rest []Line, source, existing string) (string, error) {
	if len(rest) == 0 {
		return existing, nil
	}
	first := ""
	for _, l := range rest {
		one := ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: l.TS, Last: l.TS}
		path := filepath.Join(dir, one.ChunkFile())
		if err := WriteLocalAtomic(p, path, append(append([]byte{}, l.Raw...), '\n')); err != nil {
			return "", fmt.Errorf("installing replacement for chunk %s: %w", source, err)
		}
		if _, err := ReadChunkVerified(path, one.Last); err != nil {
			return "", fmt.Errorf("replacement %s for chunk %s does not read back: %w", path, source, err)
		}
		if first == "" {
			first = path
		}
	}
	return first, nil
}

// publishedSource reports whether source lies outside the local archive, in
// the record tree or the checkout's publication copy.
func publishedSource(p layout.Policy, source string) bool {
	rel, err := filepath.Rel(ArchiveRoot(p), source)
	return err != nil || !filepath.IsLocal(rel)
}

// writeSharedMarker installs the conservative availability marker beside a
// published chunk before its bytes are removed: the published copy no longer
// offers the range, and a reader of that copy alone must not treat the hole
// as empty success. Git's index is not touched.
func writeSharedMarker(p layout.Policy, source string, cn ChunkName, m Marker) error {
	m.RetiredSHA256 = ""
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := ensureDestinationDir(p, filepath.Dir(source)); err != nil {
		return err
	}
	return replaceAuditFile(p, filepath.Join(filepath.Dir(source), cn.MarkerFile()), append(body, '\n'))
}

// removeSource deletes the damaged bytes and confirms the directory entry is
// gone.
func removeSource(source string) error {
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("removing damaged chunk %s: %w", source, err)
	}
	if err := syncAuditDirectory(filepath.Dir(source)); err != nil {
		return fmt.Errorf("confirming removal of %s: %w", source, err)
	}
	return nil
}

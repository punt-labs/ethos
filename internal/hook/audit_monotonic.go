package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appendLiveAudit allocates a strictly-monotonic per-session timestamp and
// appends one redacted line to the live audit file. The caller must hold
// the per-session live-zone flock (liveAuditLockPath); allocation and
// append happen under one lock so the timestamp is strictly increasing and
// never repeats within a session (docs/audit-seal.md §timestamp).
//
// Order of operations, all under the caller's flock:
//
//  1. Reopen recovery — truncate a non-newline-terminated tail (an
//     un-synced partial write, unrecoverable regardless) so the next
//     append cannot glue a complete line onto the fragment.
//  2. Recover last_ts from the live file's complete lines, skipping a
//     terminated-but-unparseable line with a stderr count.
//  3. Seed the floor from the seal watermark's source set so no minted ts
//     sinks below an already-sealed line.
//  4. ts = max(now, floor+1ns); write; fsync.
//
// Returns the entry with its allocated Ts set, so a caller's failure-path
// diagnostic reports the real timestamp.
func appendLiveAudit(livePath, sealedDir, legacyPath string, entry auditEntry, now time.Time) (auditEntry, error) {
	if err := os.MkdirAll(filepath.Dir(livePath), 0o700); err != nil {
		return entry, fmt.Errorf("creating live dir: %w", err)
	}
	f, err := os.OpenFile(livePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return entry, fmt.Errorf("opening %s: %w", livePath, err)
	}
	defer f.Close()

	lastTS, err := truncateTornTailAndRecover(f, livePath)
	if err != nil {
		return entry, err
	}

	wm, err := sessionWatermark(sealedDir, sessionNamespace, "", legacyPath)
	if err != nil {
		return entry, fmt.Errorf("computing watermark for %s: %w", livePath, err)
	}

	floor := lastTS
	if wm > floor {
		floor = wm
	}
	ts := now.UnixNano()
	if ts <= floor {
		ts = floor + 1
	}
	entry.Ts = formatLineTS(ts)

	data, err := json.Marshal(entry)
	if err != nil {
		return entry, fmt.Errorf("marshaling entry: %w", err)
	}
	line := append(data, '\n')

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return entry, fmt.Errorf("seeking %s: %w", livePath, err)
	}
	if _, err := f.Write(line); err != nil {
		return entry, fmt.Errorf("writing %s: %w", livePath, err)
	}
	if err := f.Sync(); err != nil {
		return entry, fmt.Errorf("syncing %s: %w", livePath, err)
	}
	return entry, nil
}

// truncateTornTailAndRecover truncates a non-newline-terminated tail on the
// open live file and returns the max ts over its complete, parseable lines.
// A terminated line that still fails to parse is skipped and counted on
// stderr (§timestamp: an out-of-order page writeback can persist a later
// slice of a line while losing an earlier one); its own f.Sync never
// completed, so the tool call it would record almost certainly never ran.
func truncateTornTailAndRecover(f *os.File, path string) (int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seeking %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(b) == 0 {
		return 0, nil
	}

	// Truncate a torn (non-newline-terminated) tail.
	if b[len(b)-1] != '\n' {
		cut := bytes.LastIndexByte(b, '\n') + 1 // 0 when no newline at all
		if err := f.Truncate(int64(cut)); err != nil {
			return 0, fmt.Errorf("truncating torn tail of %s: %w", path, err)
		}
		if strings.TrimSpace(string(b[cut:])) != "" {
			fmt.Fprintf(os.Stderr,
				"ethos: audit-log: %s: truncated torn trailing line on reopen\n", path)
		}
		b = b[:cut]
	}

	var maxTS int64
	skipped := 0
	for _, raw := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var e auditEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			skipped++
			continue
		}
		ts, err := parseLineTS(e.Ts)
		if err != nil {
			skipped++
			continue
		}
		if ts > maxTS {
			maxTS = ts
		}
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: %s: skipped %d unparseable line(s) on reopen\n", path, skipped)
	}
	return maxTS, nil
}

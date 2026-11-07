package hook

// These low-level guard fixtures exercise the historical tracked-chunk sink.
// They are test-only: product archival uses sealPolicyStream, never this writer
// or its former whole-index snapshot/rollback protocol.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

type sealOutcome struct {
	wroteChunk bool
	lineCount  int
	chunkPath  string
}

type sealDirParams struct {
	repoRoot  string
	ns        audit.Namespace
	session   string
	sealedDir string
	livePath  string
	chunkName func(first, last int64) string
	tempName  func(first, last int64) string
	label     string
}

func sealDirLocked(p sealDirParams, now time.Time, opts SealOptions) (sealOutcome, error) {
	if !opts.DryRun {
		if err := audit.SweepStaleTemps(p.sealedDir, p.ns, p.session, now); err != nil {
			return sealOutcome{}, err
		}
	}
	sc, err := audit.ScanSealedDir(p.sealedDir, p.ns, p.session)
	if err != nil {
		return sealOutcome{}, err
	}
	for _, c := range sc.Chunks {
		if _, err := audit.ReadChunkVerified(filepath.Join(p.sealedDir, c.ChunkFile()), c.Last); err != nil {
			return sealOutcome{}, err
		}
	}
	watermark, err := audit.Watermark(p.sealedDir, p.ns, p.session)
	if err != nil {
		return sealOutcome{}, err
	}
	tail, first, last, err := audit.SelectLiveTail(p.livePath, watermark)
	if err != nil {
		return sealOutcome{}, err
	}
	if len(tail) == 0 {
		return sealOutcome{}, nil
	}
	if opts.DryRun {
		return sealOutcome{lineCount: len(tail)}, nil
	}
	if err := os.MkdirAll(p.sealedDir, 0o700); err != nil {
		return sealOutcome{}, fmt.Errorf("creating sealed dir %s: %w", p.sealedDir, err)
	}
	guard := sessionLineGuard(p.repoRoot)
	if p.ns == audit.MissionNS {
		guard = missionLineGuard(p.repoRoot)
	}
	tail, notes := guardTailWith(tail, p.label, guard)
	name := p.chunkName(first, last)
	if err := audit.WriteChunkAtomic(p.sealedDir, p.tempName(first, last), name, tail); err != nil {
		return sealOutcome{}, err
	}
	printNotices(notes)
	return sealOutcome{wroteChunk: true, lineCount: len(tail), chunkPath: filepath.Join(p.sealedDir, name)}, nil
}

func guardTail(tail [][]byte, label string) ([][]byte, []string) {
	return guardTailWith(tail, label, guardAuditLineFull)
}

func guardTailWith(tail [][]byte, label string, guard lineGuard) ([][]byte, []string) {
	out := make([][]byte, len(tail))
	var tally audit.GuardTally
	for i, l := range tail {
		g, rule, marker := guard(l)
		tally.Add(rule, marker)
		out[i] = g
	}
	return out, tally.Notices("ethos: audit seal: " + label + ":")
}

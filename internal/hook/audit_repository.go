package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// auditPolicy is the compatibility boundary for callers that already supply a
// store root. Linked-checkout callers must pass their Located.Layout snapshot to
// the policy-taking APIs; do not rediscover roots or mutate process state here.
func auditPolicy(repoRoot string) (layout.Policy, error) {
	if repoRoot == "" {
		return layout.Policy{}, fmt.Errorf("audit repository root is empty")
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return layout.Policy{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return layout.Policy{}, err
	}
	cfg, err := resolve.ConfigOf(root)
	if err != nil {
		return layout.Policy{}, err
	}
	return (resolve.Located{Store: root, Checkout: root, Config: cfg}).Layout()
}

// PromptPublicationRetryError distinguishes required captured-consent publication
// failure from warn-only local archival failure in a blame commit context.
type PromptPublicationRetryError struct {
	Err error
}

func (e *PromptPublicationRetryError) Error() string {
	return fmt.Sprintf("captured prompt publication retry incomplete: %v", e.Err)
}

func (e *PromptPublicationRetryError) Unwrap() error { return e.Err }

// SealRepoWithPolicy always archives locally, including full mode. Automatic
// publication is prospective: only entries stamped eligible at their write are
// copied. Blame mode does not inspect or mutate the Git index, even to check
// ignore rules or discover gitlinks. Eligible audit chunks are retried by a full
// seal; captured full-consent prompt copies are retried in either mode, without
// index access in blame. Local completion survives publication failure.
func SealRepoWithPolicy(p layout.Policy, now time.Time, opts SealOptions) (SealResult, error) {
	streams, err := audit.ListStreams(p)
	var res SealResult
	if err == nil {
		res, err = sealPolicyStreams(p, streams, now, opts)
	}
	// This is a retry of previously captured full consent, not new eligibility
	// inferred from the current mode. Run once, outside every audit/prompt lock,
	// even after archival failure; keep both failures and all completed archives.
	// Mission-only seal/export deliberately does not retry unrelated prompts.
	if !opts.DryRun {
		if retryErr := mission.RetryPromptPublications(p); retryErr != nil {
			err = errors.Join(err, &PromptPublicationRetryError{Err: retryErr})
		}
	}
	return res, err
}

func SealMissionWithPolicy(p layout.Policy, missionID string, now time.Time, opts SealOptions) (SealResult, error) {
	streams, err := audit.ListStreams(p)
	if err != nil {
		return SealResult{}, err
	}
	selected := streams[:0]
	for _, s := range streams {
		if s.Namespace == audit.MissionNS && s.Mission == missionID {
			selected = append(selected, s)
		}
	}
	return sealPolicyStreams(p, selected, now, opts)
}

func sealPolicyStreams(p layout.Policy, streams []audit.Stream, now time.Time, opts SealOptions) (SealResult, error) {
	var res SealResult
	var publish []string
	var archiveErrors []error
	for _, s := range streams {
		err := audit.WithStreamLock(p, s, func() error {
			n, paths, err := sealPolicyStream(p, s, now, opts)
			res.LinesSealed += n
			if n > 0 {
				res.SessionsSealed++
			}
			publish = append(publish, paths...)
			return err
		})
		if err != nil {
			// Historical damage in one stream must not prevent independent new
			// streams from being retained. Still report every failure to manual
			// callers, and do not begin full index publication after an error.
			archiveErrors = append(archiveErrors, fmt.Errorf("archiving stream %s (live data retained): %w", s.Key(), err))
		}
	}
	if len(archiveErrors) > 0 {
		return res, errors.Join(archiveErrors...)
	}
	if opts.DryRun || p.RecordMode() == layout.RecordBlame {
		return res, nil
	}
	if err := audit.StagePublication(p, publish); err != nil {
		return res, fmt.Errorf("full audit publication incomplete; local archive retained; retry in full mode: %w", err)
	}
	res.ChunksStaged = len(publish)
	return res, nil
}

func sealPolicyStream(p layout.Policy, s audit.Stream, now time.Time, opts SealOptions) (int, []string, error) {
	data, err := audit.ReadStream(p, s)
	if err != nil {
		return 0, nil, err
	}
	date := ""
	if s.Namespace == audit.SessionNS && opts.StartDate != nil {
		date = opts.StartDate(s.Session)
	}
	if date == "" {
		date = audit.LiveFirstLineDate(s.LivePath(p))
	}
	if date == "" {
		date = now.UTC().Format(audit.SessionDateFormat)
	}
	dir, err := audit.ArchiveDir(p, s, date)
	if err != nil {
		return 0, nil, err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	guard := sessionLineGuard(checkout)
	if s.Namespace == audit.MissionNS {
		guard = missionLineGuard(checkout)
	}
	session := ""
	if s.Namespace == audit.MissionNS {
		session = s.Session
	}
	scan, err := audit.ScanSealedDir(dir, s.Namespace, session)
	if err != nil {
		return 0, nil, err
	}
	archived := map[int64]bool{}
	for _, c := range scan.Chunks {
		lines, err := audit.ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
		if err != nil {
			return 0, nil, err
		}
		for _, l := range lines {
			archived[l.TS] = true
		}
	}
	var pending []audit.Line
	for _, l := range data.Lines {
		if !archived[l.TS] {
			pending = append(pending, l)
		}
	}
	if opts.DryRun {
		reportf(opts, "would archive %d line(s) for stream %s\n", len(pending), s.Key())
		return len(pending), nil, nil
	}
	// New writes are guarded before their append. A pending line still in the
	// live file may predate that guard: it is redacted in place, and the live
	// file replaced, before anything immutable is written. Any other source
	// (frozen legacy, a published copy) is not ours to rewrite; do not
	// manufacture a conflict with its retained source on the next union read.
	livePath := s.LivePath(p)
	rewrite := false
	for i, l := range pending {
		out, _, _ := guard(l.Raw)
		changed, err := payloadChanged(l, out)
		if err != nil {
			return 0, nil, err
		}
		if !changed {
			continue
		}
		if l.Source != livePath {
			return 0, nil, errNeedsMigration(l.TS)
		}
		pending[i].Raw = out
		rewrite = true
	}
	for _, l := range data.Legacy {
		out, _, _ := guard(l.Raw)
		changed, err := payloadChanged(l, out)
		if err != nil {
			return 0, nil, err
		}
		if changed {
			return 0, nil, errNeedsMigration(l.TS)
		}
	}
	if rewrite {
		var live []audit.Line
		for _, l := range pending {
			if l.Source == livePath {
				live = append(live, l)
			}
		}
		if err := audit.ReplaceLocalFile(p, livePath, auditLinesBytes(live)); err != nil {
			return 0, nil, fmt.Errorf("replacing live audit %s with its redacted lines: %w", livePath, err)
		}
	}
	if len(pending) > 0 {
		name := policyChunkName(s, pending[0].TS, pending[len(pending)-1].TS)
		if err := audit.WriteLocalAtomic(p, filepath.Join(dir, name), auditLinesBytes(pending)); err != nil {
			return 0, nil, err
		}
		reportf(opts, "archived %d line(s) for stream %s into %s\n", len(pending), s.Key(), name)
	}
	// Frozen legacy stays frozen and private. Its multiset identity must not
	// be converted to the monotonic timestamp identity by migration/archival.
	if len(data.Legacy) > 0 && (s.Namespace == audit.SessionNS || s.Session == "no-session") {
		name := "audit.jsonl"
		if s.Namespace == audit.MissionNS {
			name = "log.jsonl"
		}
		if err := audit.ReplaceLocalFile(p, filepath.Join(dir, name), auditLinesBytes(data.Legacy)); err != nil {
			return len(pending), nil, err
		}
	}
	if p.RecordMode() != layout.RecordFull {
		return len(pending), nil, nil
	}
	// Publish from immutable archive chunks, never by copying the live file
	// or the whole local directory. This also retries prior full publication.
	scan, err = audit.ScanSealedDir(dir, s.Namespace, session)
	if err != nil {
		return len(pending), nil, err
	}
	var paths []string
	for _, c := range audit.SortChunks(scan.Chunks) {
		lines, err := audit.ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
		if err != nil {
			return len(pending), nil, err
		}
		var eligible []audit.Line
		for _, l := range lines {
			if !audit.Eligible(s, l) {
				continue
			}
			out, _, _ := guard(l.Raw)
			// A changed payload cannot keep evidence for its old digest. Do
			// not silently publish or silently omit it; the operator must see it.
			before, err := audit.CanonicalPayload(l.Raw)
			if err != nil {
				return len(pending), nil, err
			}
			after, err := audit.CanonicalPayload(out)
			if err != nil || !bytes.Equal(before, after) {
				return len(pending), nil, fmt.Errorf("eligible audit payload at ts %d no longer passes redaction unchanged", l.TS)
			}
			eligible = append(eligible, l)
		}
		if len(eligible) == 0 {
			continue
		}
		rel, err := filepath.Rel(audit.ArchiveRoot(p), dir)
		if err != nil {
			return len(pending), nil, err
		}
		path := filepath.Join(p.CheckoutRecordRoot(), rel, policyChunkName(s, eligible[0].TS, eligible[len(eligible)-1].TS))
		if err := audit.WriteLocalAtomic(p, path, auditLinesBytes(eligible)); err != nil {
			return len(pending), nil, err
		}
		paths = append(paths, path)
	}
	return len(pending), paths, nil
}

// payloadChanged reports whether guarding l produced a different payload.
func payloadChanged(l audit.Line, guarded []byte) (bool, error) {
	before, err := audit.CanonicalPayload(l.Raw)
	if err != nil {
		return false, err
	}
	after, err := audit.CanonicalPayload(guarded)
	if err != nil {
		return false, fmt.Errorf("guarded audit payload at ts %d is not a valid line: %w", l.TS, err)
	}
	return !bytes.Equal(before, after), nil
}

func errNeedsMigration(ts int64) error {
	return fmt.Errorf("audit payload at ts %d requires redacted migration or explicit operator cleanup before immutable archival; original source retained", ts)
}

func policyChunkName(s audit.Stream, first, last int64) string {
	if s.Namespace == audit.MissionNS {
		return audit.MissionChunkFile(s.Session, first, last)
	}
	return audit.SessionChunkFile(first, last)
}

func auditLinesBytes(lines []audit.Line) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l.Raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// QueryAuditByDelegationWithPolicy reads all layers irrespective of the mode
// captured for new writes. Corruption and identity conflicts remain errors.
func QueryAuditByDelegationWithPolicy(p layout.Policy, globalSessionsDir, delegationID string) ([]AuditView, error) {
	if delegationID == "" {
		return nil, nil
	}
	streams, err := audit.ListStreams(p)
	if err != nil {
		return nil, err
	}
	var out []AuditView
	legacyCounts := map[string]map[string]int{}
	legacyPayloads := map[string]map[string]string{}
	for _, s := range streams {
		if s.Namespace != audit.SessionNS {
			continue
		}
		data, err := audit.ReadStream(p, s)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		payloads := map[string]string{}
		for _, l := range data.Legacy {
			key, err := audit.CheckLegacyIdentity(audit.SessionNS, l.Raw, payloads)
			if err != nil {
				return nil, err
			}
			counts[key]++
		}
		legacyCounts[s.Session] = counts
		legacyPayloads[s.Session] = payloads
		for _, l := range append(data.Legacy, data.Lines...) {
			e, err := decodeAuditLine(l.Raw)
			if err != nil {
				return nil, err
			}
			if e.DelegationID == delegationID {
				out = append(out, toView(e))
			}
		}
	}
	if globalSessionsDir == "" {
		return out, nil
	}
	files, err := enumerateLegacyAuditFiles(globalSessionsDir)
	if err != nil {
		return nil, err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	legacyGuard := sessionLineGuard(checkout)
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".audit.jsonl")
		if legacyPayloads[id] == nil {
			legacyPayloads[id] = map[string]string{}
		}
		for _, path := range []string{file, file + stagedSuffix} {
			lines, err := readLegacyAuditLines(path)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			lines, err = redactLegacyLines(lines, checkout)
			if err != nil {
				return nil, err
			}
			for _, l := range lines {
				l.Raw, _, _ = legacyGuard(l.Raw)
				l.Key, err = audit.CheckLegacyIdentity(audit.SessionNS, l.Raw, legacyPayloads[id])
				if err != nil {
					return nil, fmt.Errorf("global legacy session %s: %w", id, err)
				}
				if legacyCounts[id][l.Key] > 0 {
					legacyCounts[id][l.Key]--
					continue
				}
				e, err := decodeAuditLine(l.Raw)
				if err != nil {
					return nil, err
				}
				if e.DelegationID == delegationID {
					out = append(out, toView(e))
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ts < out[j].Ts })
	return out, nil
}

// CollectAuditDiagnosticsWithPolicy reads the same union as the query. No
// gitlink/index probe is necessary for a local archive.
func CollectAuditDiagnosticsWithPolicy(p layout.Policy) (AuditDiagnostics, error) {
	streams, err := audit.ListStreams(p)
	if err != nil {
		return AuditDiagnostics{}, err
	}
	var diag AuditDiagnostics
	for _, s := range streams {
		if s.Namespace != audit.SessionNS {
			continue
		}
		data, err := audit.ReadStream(p, s)
		if err != nil {
			return AuditDiagnostics{}, err
		}
		diag.Gaps = append(diag.Gaps, data.Gaps...)
		for _, l := range append(data.Lines, data.Legacy...) {
			var h struct {
				Ts    string `json:"ts"`
				Error string `json:"audit_error"`
			}
			if err := json.Unmarshal(l.Raw, &h); err != nil {
				return AuditDiagnostics{}, err
			}
			if h.Error != "" {
				diag.LossMarkers = append(diag.LossMarkers, LossMarker{Session: s.Session, Ts: h.Ts, Error: h.Error})
			}
		}
	}
	return diag, nil
}

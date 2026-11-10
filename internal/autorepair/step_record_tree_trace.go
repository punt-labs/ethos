package autorepair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	// hook supplies the seal's line guards. Their closure is the live audit
	// writer's own redaction (redactToolInputQuiet, hashToolInput, the
	// sensitive-content sweep) across five files, so it is not a mechanical
	// move into a leaf package. The import direction is therefore fixed:
	// autorepair imports hook; hook must never import autorepair. When the
	// engine is wired into the session-start hook, the wiring goes in
	// cmd/ethos, which imports both.
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/repair"
)

const stepRecordTreeTrace = "record-tree-trace"

func init() {
	Register(Step{Name: stepRecordTreeTrace, Scope: ScopeRepository, Run: runRecordTreeTrace})
}

// runRecordTreeTrace moves trace that older releases wrote into the shared
// record tree (frozen audit and log files, sealed chunks, delegation prompts
// and reflections) into the local archive as redacted copies, then deletes the
// record-tree original. repair.Inventory decides what is obsolete: explicit
// exports, full-mode publications and agent artifacts are never listed.
//
// Each original is deleted only under the lock a current writer takes for
// the same data (the mission-state lock, or the audit stream lock for a
// session chunk), only after the archived copy has been read back and found
// byte-equal to the redacted payload, and only if the original's bytes still
// hash to what the inventory read. Anything else is left in place and
// recorded as a residual.
//
// A mission's reflections.yaml is the only source of its round authority
// until step 3 (round-decisions) has installed round_decisions in the
// contract. Until then it stays; afterwards each round becomes a local
// envelope that joins the contract's own decision, and the aggregate goes.
func runRecordTreeTrace(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	plan, err := repair.Inventory(ctx, env.Policy)
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{Deferred: true}, nil
		}
		env.Log.Logf(stepRecordTreeTrace, "inventory", nil, err)
		out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: "inventory", Reason: err.Error()})
		return out, nil
	}
	a := &archiver{env: env, repo: env.Roots.Checkout}
	if a.repo == "" {
		a.repo = env.Roots.Store
	}
	if a.red, err = mission.NewPathRedactor(a.repo); err != nil {
		out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: "redactor", Reason: err.Error()})
		return out, nil
	}
	a.sessionGuard = hook.SessionLineGuard(a.repo)
	a.missionGuard = hook.MissionLineGuard(a.repo)
	a.store = mission.NewStoreWithRoots(env.Roots.Store, env.Global)
	for _, path := range plan.Unreadable {
		rel, _, ok := a.relative(path)
		if !ok {
			continue
		}
		env.Log.Logf(stepRecordTreeTrace, "unreadable", []string{path}, nil)
		out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: filepath.ToSlash(rel), Reason: "cannot be read; left in place"})
	}
	record := func(path, rel string, deferred bool, err error) {
		switch {
		case deferred:
			env.Log.Logf(stepRecordTreeTrace, "skip-busy-lock", []string{path}, nil)
			out.Deferred = true
		case errors.Is(err, errKept):
		case err != nil:
			env.Log.Logf(stepRecordTreeTrace, "archive", []string{path}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: filepath.ToSlash(rel), Reason: err.Error()})
		default:
			out.Items++
		}
	}
	seen := map[string]bool{}
	for _, item := range plan.Items {
		if ctx.Err() != nil {
			out.Deferred = true
			return out, nil
		}
		rel, _, ok := a.relative(item.Path)
		if !ok {
			continue
		}
		kind, ok := classifyTrace(rel)
		if !ok || kind == kindReflections {
			continue
		}
		seen[item.Path] = true
		deferred, err := a.archiveAndDelete(kind, rel, item)
		record(item.Path, rel, deferred, err)
	}
	// Reflections aggregates are enumerated here, not taken from the
	// inventory: repair lists one only while the contract has no
	// round_decisions, which is exactly when it must stay (it is the
	// mission's round authority until step round-decisions). Which of the
	// others may go is repair.ClassifyReflections's decision, never this
	// step's: a full-mode repository publishes the aggregate, and published
	// history is never removed by a mode switch or a repair.
	for _, root := range []string{env.Policy.RecordRoot(), env.Policy.CheckoutRecordRoot()} {
		if root == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, "missions", "*", "reflections.yaml"))
		for _, path := range matches {
			if ctx.Err() != nil {
				out.Deferred = true
				return out, nil
			}
			rel, _, ok := a.relative(path)
			if !ok || seen[path] {
				continue
			}
			seen[path] = true // S and C are one directory in a plain checkout
			deferred, err := a.archiveReflections(path, rel)
			record(path, rel, deferred, err)
		}
	}
	return out, nil
}

// archiveReflections handles one missions/<id>/reflections.yaml by
// repair.ClassifyReflections's verdict: a current publication (full mode, or
// an export beside it) is left alone with no residual; the mission's round
// authority (no round_decisions yet) is left with a residual naming step 3;
// legacy detail becomes one envelope per round, joining the contract's
// decision, and the aggregate is deleted under the mission-state lock, the
// lock the reflect path holds while it writes the same file
// (Store.withMissionLock, reflection_authority.go). A round whose envelope a
// live reflect already wrote means the aggregate is that reflect's
// publication, and it stays too.
func (a *archiver) archiveReflections(path, rel string) (deferred bool, err error) {
	id := strings.Split(filepath.ToSlash(rel), "/")[1]
	class, err := repair.ClassifyReflections(a.env.Policy, a.env.Policy.RecordRoot(), id)
	if err != nil {
		return false, err
	}
	switch class {
	case repair.TraceCurrentPublication:
		return false, errKept
	case repair.TraceAuthority:
		return false, errors.New("contract has no round_decisions yet; reflections.yaml is its round authority until step round-decisions installs them")
	}
	lock, err := a.env.Policy.LockPath("mission-state", id)
	if err != nil {
		return false, err
	}
	if err := layout.EnsureLocalDir(a.env.Policy, filepath.Dir(lock)); err != nil {
		return false, err
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening lock: %w", err)
	}
	defer f.Close()
	if repair.TryLock(f) != nil {
		return true, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading: %w", err)
	}
	if !utf8.Valid(data) {
		return false, errors.New("not valid UTF-8; left in place")
	}
	if err := a.reflections(id, data); errors.Is(err, errPublishedEnvelope) {
		a.env.Log.Logf(stepRecordTreeTrace, "keep-published", []string{path}, nil)
		return false, errKept
	} else if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("archived but not deleted: %w", err)
	}
	a.env.Log.Logf(stepRecordTreeTrace, "archive-and-delete", []string{path}, nil)
	return false, nil
}

// errKept says a file was examined and deliberately left: not an item, not a
// residual.
var errKept = errors.New("kept")

// liveEnvelopeExists reports whether dest holds an envelope a live reflect
// wrote (its event_session names a real session, not the legacy-import
// placeholder). A missing file is false; an unreadable one is an error.
func liveEnvelopeExists(dest string) (bool, error) {
	b, err := os.ReadFile(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var head struct {
		EventSession string `yaml:"event_session"`
	}
	if err := yaml.Unmarshal(b, &head); err != nil {
		return false, fmt.Errorf("reading envelope %s: %w", filepath.Base(dest), err)
	}
	return head.EventSession != "" && head.EventSession != mission.LegacyEventSession, nil
}

type traceKind int

const (
	kindReflections traceKind = iota + 1
	kindPrompt
	kindMissionLog // frozen log.jsonl or a sealed chunk
	kindSessionLog // frozen audit.jsonl or a sealed chunk
	kindDiagnostic // .corrupt retirement or .quarantine marker
)

// classifyTrace names the trace kind of a record-tree path, or false for a
// file this step leaves to another (locks, temporaries).
func classifyTrace(rel string) (traceKind, bool) {
	a := strings.Split(filepath.ToSlash(rel), "/")
	base := a[len(a)-1]
	switch {
	case len(a) == 3 && a[0] == "missions" && base == "reflections.yaml":
		return kindReflections, true
	case len(a) == 5 && a[0] == "missions" && a[2] == "delegations" && base == "prompt.md":
		return kindPrompt, true
	case len(a) == 3 && a[0] == "missions":
		return classifyChunk(base, audit.MissionNS, "log.jsonl", kindMissionLog)
	case len(a) == 3 && a[0] == "sessions":
		return classifyChunk(base, audit.SessionNS, "audit.jsonl", kindSessionLog)
	}
	return 0, false
}

func classifyChunk(base string, ns audit.Namespace, frozen string, k traceKind) (traceKind, bool) {
	if base == frozen {
		return k, true
	}
	switch _, kind := audit.Classify(base, ns); kind {
	case audit.KindValid:
		return k, true
	case audit.KindCorrupt, audit.KindQuarantine:
		return kindDiagnostic, true
	}
	return 0, false
}

type lineGuard = func(line []byte) (out []byte, rule string, marker bool)

type archiver struct {
	env          Env
	repo         string
	red          mission.PathRedactor
	sessionGuard lineGuard
	missionGuard lineGuard
	store        *mission.Store
}

// relative returns path relative to the record root that holds it.
func (a *archiver) relative(path string) (rel, root string, ok bool) {
	for _, r := range []string{a.env.Policy.RecordRoot(), a.env.Policy.CheckoutRecordRoot()} {
		if r == "" {
			continue
		}
		if rel, err := filepath.Rel(r, path); err == nil && filepath.IsLocal(rel) {
			return rel, r, true
		}
	}
	return "", "", false
}

// archiveAndDelete archives one item and deletes the original under the lock
// that guards it. deferred reports a lock another process holds.
func (a *archiver) archiveAndDelete(kind traceKind, rel string, item repair.Item) (deferred bool, err error) {
	lock, err := a.lockFor(kind, rel)
	if err != nil {
		return false, err
	}
	if err := layout.EnsureLocalDir(a.env.Policy, filepath.Dir(lock)); err != nil {
		return false, err
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening lock: %w", err)
	}
	defer f.Close()
	if repair.TryLock(f) != nil {
		return true, nil
	}
	data, err := os.ReadFile(item.Path)
	if err != nil {
		return false, fmt.Errorf("reading: %w", err)
	}
	if sha256.Sum256(data) != item.SHA256() {
		return false, errors.New("changed since the inventory; left in place")
	}
	if !utf8.Valid(data) {
		return false, errors.New("not valid UTF-8; left in place")
	}
	if err := a.archive(kind, rel, data); err != nil {
		return false, err
	}
	if err := os.Remove(item.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("archived but not deleted: %w", err)
	}
	a.env.Log.Logf(stepRecordTreeTrace, "archive-and-delete", []string{item.Path}, nil)
	return false, nil
}

// lockFor names the lock a current writer takes for the data at rel: the
// mission-state lock for a mission's files, the audit stream lock for a
// session chunk.
func (a *archiver) lockFor(kind traceKind, rel string) (string, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch kind {
	case kindSessionLog:
		return repair.StreamLockPath(a.env.Policy, rel)
	case kindDiagnostic:
		if parts[0] == "sessions" {
			return repair.StreamLockPath(a.env.Policy, rel)
		}
	}
	return a.env.Policy.LockPath("mission-state", parts[1])
}

func (a *archiver) archive(kind traceKind, rel string, data []byte) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	id := parts[1]
	switch kind {
	case kindReflections:
		return a.reflections(id, data)
	case kindPrompt:
		return a.prompt(id, parts[3], data)
	case kindMissionLog:
		dest := filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", id, parts[2])
		return a.jsonl(dest, data, parts[2] == "log.jsonl", a.missionGuard)
	case kindSessionLog:
		dir, err := a.sessionArchiveDir(parts[1])
		if err != nil {
			return err
		}
		return a.jsonl(filepath.Join(dir, parts[2]), data, parts[2] == "audit.jsonl", a.sessionGuard)
	case kindDiagnostic:
		return a.diagnostic(parts, data)
	}
	return nil
}

func (a *archiver) sessionArchiveDir(dir string) (string, error) {
	s := audit.Stream{Namespace: audit.SessionNS, Session: sessionOfDir(dir)}
	return audit.ArchiveDir(a.env.Policy, s, dir[:len(audit.SessionDateFormat)])
}

func sessionOfDir(dir string) string {
	if len(dir) > len(audit.SessionDateFormat)+1 {
		return dir[len(audit.SessionDateFormat)+1:]
	}
	return dir
}

// install puts data at dest and proves it is there. An existing destination
// must already hold exactly data: another copy of the same trace (S and C
// both holding a frozen file, an earlier run, step 2's import) is verified
// by its bytes, never assumed. A different file at dest is a collision, and
// the original stays. After a write the file is read back before the caller
// deletes anything.
func (a *archiver) install(dest string, data []byte) error {
	if existing, err := os.ReadFile(dest); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("archive %s already holds different content; left in place", filepath.Base(dest))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := audit.ReplaceLocalFile(a.env.Policy, dest, data); err != nil {
		return fmt.Errorf("archiving to %s: %w", dest, err)
	}
	back, err := os.ReadFile(dest)
	if err != nil {
		return fmt.Errorf("reading back %s: %w", dest, err)
	}
	if !bytes.Equal(back, data) {
		return fmt.Errorf("archive %s read back differently; left in place", filepath.Base(dest))
	}
	a.env.Log.Logf(stepRecordTreeTrace, "archive", []string{dest}, nil)
	return nil
}

// reflections turns the aggregate into one envelope per round, each carrying
// the shared decision the contract already holds for that round. Without
// round_decisions the aggregate is the mission's only round authority and is
// left for step 3.
// errPublishedEnvelope marks an aggregate a live reflect already paired with
// a local envelope: it is that reflect's publication, not legacy detail.
var errPublishedEnvelope = errors.New("a live reflect already wrote this round's envelope")

func (a *archiver) reflections(id string, data []byte) error {
	c, err := a.store.Load(id)
	if err != nil {
		return fmt.Errorf("loading contract: %w", err)
	}
	if c.RoundDecisions == nil {
		return errors.New("contract has no round_decisions yet; reflections.yaml is its round authority until step round-decisions installs them")
	}
	rs, err := mission.DecodeLegacyReflections(data, id)
	if err != nil {
		return fmt.Errorf("decoding reflections: %w", err)
	}
	type envelope struct {
		dest string
		data []byte
	}
	var envs []envelope
	for _, r := range rs {
		d := c.DecisionFor(r.Round)
		if d == nil {
			return fmt.Errorf("round %d has no shared decision in the contract; left in place", r.Round)
		}
		dest := filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", id, fmt.Sprintf("reflection-%d.yaml", r.Round))
		if live, err := liveEnvelopeExists(dest); err != nil {
			return err
		} else if live {
			return errPublishedEnvelope
		}
		red, err := mission.RedactReflection(a.repo, r)
		if err != nil {
			return err
		}
		b, err := mission.LegacyReflectionEnvelope(*d, red)
		if err != nil {
			return err
		}
		if rule := mission.LeakRule(string(b)); rule != "" {
			return fmt.Errorf("round %d still %s after redaction", r.Round, rule)
		}
		envs = append(envs, envelope{dest, b})
	}
	for _, e := range envs {
		if err := a.install(e.dest, e.data); err != nil {
			return err
		}
	}
	return nil
}

// promptConsent mirrors the record internal/mission reads beside a retained
// prompt. Publish is false: a legacy prompt is private.
type promptConsent struct {
	Version    int    `yaml:"version"`
	Mission    string `yaml:"mission"`
	Delegation string `yaml:"delegation"`
	SHA256     string `yaml:"sha256"`
	Publish    bool   `yaml:"publish"`
}

func (a *archiver) prompt(id, delegation string, data []byte) error {
	body := a.red.Body(data)
	if rule := mission.LeakRule(string(body)); rule != "" {
		return fmt.Errorf("prompt still %s after redaction", rule)
	}
	sum := sha256.Sum256(body)
	consent, err := yaml.Marshal(promptConsent{Version: 1, Mission: id, Delegation: delegation, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	dir := filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", id, "delegations", delegation)
	// Consent first: a payload never exists without it.
	if err := a.install(filepath.Join(dir, "prompt-consent.yaml"), consent); err != nil {
		return err
	}
	return a.install(filepath.Join(dir, "prompt.md"), body)
}

// jsonl passes every line of a frozen or sealed log through the same guard
// the seal and quarantine paths use (credentials, addresses, paths; the
// tool_input_hash recomputed over the stored tool_input) and archives the
// result. A sealed chunk must still decode strictly afterwards; a damaged
// one is step 7's and stays in place.
func (a *archiver) jsonl(dest string, data []byte, frozen bool, guard lineGuard) error {
	var out bytes.Buffer
	for _, raw := range audit.SplitLines(data) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		line, _, _ := guard(raw)
		out.Write(line)
		out.WriteByte('\n')
	}
	if !frozen {
		if _, err := audit.DecodeChunkStrict(out.Bytes(), dest); err != nil {
			return fmt.Errorf("sealed chunk is damaged: %w", err)
		}
	}
	return a.install(dest, out.Bytes())
}

// diagnostic archives a retired chunk or quarantine marker as redacted text.
func (a *archiver) diagnostic(parts []string, data []byte) error {
	body := a.red.Body(data)
	if rule := mission.LeakRule(string(body)); rule != "" {
		return fmt.Errorf("diagnostic still %s after redaction", rule)
	}
	var dest string
	if parts[0] == "missions" {
		dest = filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", parts[1], parts[2])
	} else {
		dir, err := a.sessionArchiveDir(parts[1])
		if err != nil {
			return err
		}
		dest = filepath.Join(dir, parts[2])
	}
	return a.install(dest, body)
}

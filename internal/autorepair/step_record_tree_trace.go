package autorepair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
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
// exports, full-mode publications and agent artifacts are never listed. A file
// whose redacted copy cannot be made is left in place and recorded as a
// residual.
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
	for _, path := range plan.Unreadable {
		rel, _, ok := a.relative(path)
		if !ok {
			continue
		}
		env.Log.Logf(stepRecordTreeTrace, "unreadable", []string{path}, nil)
		out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: filepath.ToSlash(rel), Reason: "cannot be read; left in place"})
	}
	for _, item := range plan.Items {
		if ctx.Err() != nil {
			out.Deferred = true
			return out, nil
		}
		rel, root, ok := a.relative(item.Path)
		if !ok {
			continue
		}
		kind, ok := classifyTrace(rel)
		if !ok {
			continue
		}
		if err := a.archive(kind, rel, root, item.Path); err != nil {
			env.Log.Logf(stepRecordTreeTrace, "archive", []string{item.Path}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: filepath.ToSlash(rel), Reason: err.Error()})
			continue
		}
		err = os.Remove(item.Path)
		env.Log.Logf(stepRecordTreeTrace, "archive-and-delete", []string{item.Path}, err)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			out.Residuals = append(out.Residuals, Residual{Step: stepRecordTreeTrace, ID: filepath.ToSlash(rel), Reason: "archived but not deleted: " + err.Error()})
			continue
		}
		out.Items++
	}
	return out, nil
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

type archiver struct {
	env  Env
	repo string
	red  mission.PathRedactor
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

func (a *archiver) archive(kind traceKind, rel, root, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}
	if !utf8.Valid(data) {
		return errors.New("not valid UTF-8; left in place")
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	id := parts[1]
	switch kind {
	case kindReflections:
		return a.reflections(id, data)
	case kindPrompt:
		return a.prompt(id, parts[3], data)
	case kindMissionLog:
		dest := filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", id, parts[2])
		return a.jsonl(dest, data, parts[2] == "log.jsonl")
	case kindSessionLog:
		s := audit.Stream{Namespace: audit.SessionNS, Session: sessionOfDir(parts[1])}
		dir, err := audit.ArchiveDir(a.env.Policy, s, parts[1][:len(audit.SessionDateFormat)])
		if err != nil {
			return err
		}
		return a.jsonl(filepath.Join(dir, parts[2]), data, parts[2] == "audit.jsonl")
	case kindDiagnostic:
		return a.diagnostic(parts, data)
	}
	return nil
}

func sessionOfDir(dir string) string {
	if len(dir) > len(audit.SessionDateFormat)+1 {
		return dir[len(audit.SessionDateFormat)+1:]
	}
	return dir
}

// install writes data to dest unless an archived copy is already there. An
// existing copy is a redacted archive written earlier (by this step or by the
// current writers) and is kept.
func (a *archiver) install(dest string, data []byte) error {
	if _, err := os.Lstat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := audit.ReplaceLocalFile(a.env.Policy, dest, data); err != nil {
		return fmt.Errorf("archiving to %s: %w", dest, err)
	}
	a.env.Log.Logf(stepRecordTreeTrace, "archive", []string{dest}, nil)
	return nil
}

func (a *archiver) reflections(id string, data []byte) error {
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
		red, err := mission.RedactReflection(a.repo, r)
		if err != nil {
			return err
		}
		round, b, err := mission.LegacyReflectionEnvelope(r, red)
		if err != nil {
			return err
		}
		if rule := mission.LeakRule(string(b)); rule != "" {
			return fmt.Errorf("round %d still %s after redaction", round, rule)
		}
		envs = append(envs, envelope{filepath.Join(a.env.Policy.TraceArchiveRoot(), "missions", id, fmt.Sprintf("reflection-%d.yaml", round)), b})
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

// jsonl redacts every line of a frozen or sealed log and archives it. A sealed
// chunk must decode line by line (a damaged one belongs to step 7 and is left
// in place); a frozen file's undecodable line becomes a loss marker.
func (a *archiver) jsonl(dest string, data []byte, frozen bool) error {
	var out bytes.Buffer
	for n, raw := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		line, err := a.redactLine(raw, frozen)
		if err != nil {
			return fmt.Errorf("line %d: %w", n+1, err)
		}
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

// redactLine returns raw with credentials, addresses and home paths removed
// from every string. ts is kept so chunk names and watermarks stay true.
func (a *archiver) redactLine(raw []byte, frozen bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		if !frozen {
			return nil, errors.New("not a JSON object")
		}
		return lossMarker(raw), nil
	}
	if _, _, dup := redact.DuplicateJSONKey(raw); dup {
		m["redacted"] = true
	}
	ts, hasTS := m["ts"]
	changed := false
	for k, v := range m {
		if k == "ts" {
			continue
		}
		r, c := a.redactValue(v)
		m[k], changed = r, changed || c
	}
	if hasTS {
		m["ts"] = ts
	}
	if changed {
		m["redacted"] = true
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if mission.LeakRule(string(out)) != "" {
		return lossMarker(raw), nil
	}
	return out, nil
}

func (a *archiver) redactValue(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		r := a.red.Content(x)
		return r, r != x
	case map[string]any:
		changed := false
		for k, e := range x {
			r, c := a.redactValue(e)
			x[k], changed = r, changed || c
		}
		return x, changed
	case []any:
		changed := false
		for i, e := range x {
			r, c := a.redactValue(e)
			x[i], changed = r, changed || c
		}
		return x, changed
	}
	return v, false
}

// lossMarker stands for a line that could not be redacted safely; it keeps ts
// when the line had a clean one.
func lossMarker(raw []byte) []byte {
	m := map[string]any{"audit_error": "line could not be redacted", "redacted": true}
	var h struct {
		TS string `json:"ts"`
	}
	if json.Unmarshal(raw, &h) == nil && h.TS != "" && mission.LeakRule(h.TS) == "" {
		if _, err := audit.ParseLineTS(h.TS); err == nil {
			m["ts"] = h.TS
		}
	}
	b, _ := json.Marshal(m)
	return b
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
		s := audit.Stream{Namespace: audit.SessionNS, Session: sessionOfDir(parts[1])}
		dir, err := audit.ArchiveDir(a.env.Policy, s, parts[1][:len(audit.SessionDateFormat)])
		if err != nil {
			return err
		}
		dest = filepath.Join(dir, parts[2])
	}
	return a.install(dest, body)
}

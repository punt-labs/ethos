package mission

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the coverage invariant for ethos-3e3f.
//
// The in-scope writer set is the mission-record and audit writers:
//
//   - missions/<id>/: contract, results, reflections, delegation
//     prompt.md and record.yaml, event-log chunks (internal/mission);
//   - .punt-labs/ethos/missions.jsonl (internal/mission);
//   - the sealed and live audit files and quarantine markers
//     (internal/hook, internal/audit);
//   - `ethos mission export` output (cmd/ethos).
//
// Out of scope, by operator ruling: the identity graph (identities,
// personalities, writing styles, talents, roles, teams, agents),
// generated .claude/agents/*.md, .punt-labs/ethos.yaml and the vendor
// manifests. They carry identity data on purpose; an identity's email is
// its beadle binding and must not be redacted. Those packages are listed
// in outOfScopePackages with that reason, so a package that gains a
// writer without being classified as in or out of scope still fails.
//
// What the rule proves. A function in an in-scope package is a writer if
// it calls an os file-creating primitive or a registered sink. An
// unregistered writer must call a redaction choke point (RedactedYAML,
// RedactRecord, redactYAMLDoc, redactJSONLines, redactToolInput, Prose,
// Body, Content and the others in chokePoints), and the data argument of
// each os.WriteFile call (argument 1) and each sink call (the index
// recorded in the registry; a sink without one fails) must contain a
// choke-point call, directly or through a local variable assigned from
// one. A redacted path or file name in another argument does not count.
// append(a, b...) counts only if every argument is redacted; the one
// exception is the first argument of x = append(x, ...), the variable being
// assigned to. A "var x T" variable counts as redacted only if every
// assignment to it is; a choke point held in a variable (f := r.Content)
// counts when f is called. A writer that is added later and calls no choke
// point fails by default, and each registered redactor entry must still
// call the redactor it names.
//
// What the rule does not prove. It is intra-function and name-based: it
// does not follow data across calls, and a helper that wraps a choke point
// but returns raw bytes is accepted. It ignores statement order and
// scope: x := Body(b); x = b, and a shadowing redeclaration of x, are not
// caught, because any assignment from a choke point counts for a variable
// not declared "var x T"; and acc = append(acc, Body(b)...); acc =
// append(acc, b...) passes, because each assignment is judged alone and
// their order is ignored. os.OpenFile,
// Create and CreateTemp writers followed by f.Write keep only the weaker
// presence rule (a choke-point call somewhere in the function). Functions
// registered as sink, downstream or exempt skip the per-write check
// entirely; their callers are checked instead. Whether the bytes on disk
// are redacted is proved by the byte tests: store_redact_test.go,
// migrate_redact_test.go, cmd/ethos/redact_test.go and
// quarantine_test.go. This test makes "a new writer was added and nobody
// routed it through the redactor" a build failure instead of a review
// hope; it does not replace them.

type writerClass int

const (
	// classCheckpoint: the function must call every identifier in calls.
	// It need not itself be a writer: it is a link in the chain from a
	// choke point to the redactor, asserted so a choke point cannot be
	// hollowed out while the writers that call it keep passing.
	classCheckpoint writerClass = iota
	// classSink: writes bytes its caller hands it. Every caller is
	// discovered as a writer and must be registered.
	classSink
	// classDownstream: receives content already redacted by the function
	// named in via, which must be its only caller and must itself be
	// registered classCheckpoint.
	classDownstream
	// classExempt: writes nothing that reaches a tracked mission or
	// audit record. reason says why.
	classExempt
)

type writerEntry struct {
	class  writerClass
	calls  []string
	via    string
	reason string
	// dataArg is the index of the argument a classSink writes to disk;
	// hasDataArg is false for a sink registered without one, which fails.
	dataArg    int
	hasDataArg bool
}

func checkpoint(calls ...string) writerEntry {
	return writerEntry{class: classCheckpoint, calls: calls}
}
func sink(dataArg int, reason string) writerEntry {
	return writerEntry{class: classSink, reason: reason, dataArg: dataArg, hasDataArg: true}
}
func downstream(via, reason string) writerEntry {
	return writerEntry{class: classDownstream, via: via, reason: reason}
}
func exempt(reason string) writerEntry { return writerEntry{class: classExempt, reason: reason} }

const (
	reasonLock     = "lock file; carries no content"
	reasonCounter  = "ID counter in the global untracked tree; a number, no free text"
	reasonGlobal   = "global untracked tree (~/.punt-labs/ethos); never committed"
	reasonRollback = "rollback rewrite of bytes read back from the same file, which were redacted when first written"
)

// writerRegistry is keyed "<dir>.<Func>" or "<dir>.<Type>.<Method>", dir
// being slash-separated and relative to the repository root.
var writerRegistry = map[string]writerEntry{
	// internal/mission: records.

	"internal/mission.Store.appendLiveEventLocked": downstream("Store.appendEventLocked",
		"the live log that seals into tracked chunks; its only caller redacts the event first"),
	"internal/mission.Store.restoreContract":    exempt(reasonRollback),
	"internal/mission.Store.DisclaimDelegation": exempt(reasonRollback),
	"internal/mission.migrateOneMission": exempt(
		"renames the staging directory whose files copyIfExists wrote through the redactor"),

	"internal/mission.writeContractFile": sink(1, "atomic contract write; callers redact"),
	"internal/mission.writeAtomicFile":   sink(3, "atomic delegation file write; callers redact"),
	"internal/mission.writeSyncedFile":   sink(1, "fsynced file write; callers redact"),

	"internal/mission.AcquireDelegationLock":      exempt(reasonLock),
	"internal/mission.AcquireDispatchPendingLock": exempt(reasonLock),
	"internal/mission.Store.withCreateLock":       exempt(reasonLock),
	"internal/mission.Store.withLock":             exempt(reasonLock),
	"internal/mission.withTraceLock":              exempt(reasonLock),
	"internal/mission.acquireMissionLock":         exempt(reasonLock),
	"internal/mission.newReleaseFunc":             exempt(reasonLock),
	"internal/mission.allocateCounter":            exempt(reasonCounter),
	"internal/mission.writeCounterAtomic":         exempt(reasonCounter),
	"internal/mission.writeSidecarFile":           exempt("active-mission and delegation-binding sidecars: IDs, " + reasonGlobal),

	// internal/hook: audit lines.
	// Choke points: each must still do what the writers that call it rely on.
	"internal/mission.RedactedYAML":    checkpoint("RedactRecord"),
	"internal/mission.RedactRecord":    checkpoint("NewPathRedactor", "RecordPaths"),
	"internal/mission.redactYAMLDoc":   checkpoint(),
	"internal/mission.redactJSONLines": checkpoint("redactOrdered"),

	"internal/hook.buildAuditEntry":        checkpoint("redactToolInput"),
	"internal/hook.redactToolInput":        checkpoint("NewPathRedactor", "redactSensitiveContent"),
	"internal/hook.redactSensitiveContent": checkpoint("sweepSecrets"),
	"internal/hook.sweepSecrets":           checkpoint("SecretsValue"),
	"internal/hook.HandleAuditLog":         checkpoint("buildAuditEntry"),
	"internal/hook.appendLiveAudit":        downstream("HandleAuditLog", "appends the entry buildAuditEntry redacted; HandleAuditLog is its only caller"),
	"internal/hook.writeAuditEntry": downstream("HandleAuditLog",
		"legacy path used only when there is no repo root; appends the entry buildAuditEntry redacted"),
	"internal/hook.appendRawAuditLines":     sink(1, "MigrateAudit's append; migrateOneSession redacts the lines first"),
	"internal/hook.redactReason":            checkpoint("NewPathRedactor"),
	"internal/hook.redactLegacyLines":       checkpoint("NewPathRedactor", "redactLegacyLine"),
	"internal/hook.redactLegacyLine":        checkpoint("redactToolInput"),
	"internal/hook.sealDirLocked":           exempt("seals live-file lines already appended through the redacted writers; adds no content"),
	"internal/hook.GenerateAgentFilesTo":    exempt("out of scope (identity graph): generated .claude/agents definitions from identity content"),
	"internal/hook.InstallAgentDefinitions": exempt("out of scope (identity graph): generated .claude/agents definitions from identity content"),

	// internal/audit: sealed chunks and markers.
	"internal/audit.AppendMonotonic":   sink(3, "appends the line its callback builds; callers redact"),
	"internal/audit.WriteChunkAtomic":  sink(3, "seals lines already in the live file"),
	"internal/audit.writeMarkerAtomic": sink(2, "quarantine marker"),
	"internal/audit.WithLock":          exempt(reasonLock),
	"internal/audit.Quarantine":        exempt("re-seals live-file lines that were redacted when appended; the marker reason goes through the redactReason function its caller passes (hook.QuarantineChunk builds it from NewPathRedactor)"),
	"internal/audit.AckTombstone":      exempt("renames an existing global tombstone: " + reasonGlobal),
	"internal/audit.renameRetire":      exempt("renames an existing sealed chunk to .corrupt; no new content"),
	"internal/audit.writeFileAtomic":   exempt("session tombstone: " + reasonGlobal),

	// cmd/ethos.
	"cmd/ethos.editContent":           exempt("identity graph: operator edits an attribute file by hand"),
	"cmd/ethos.ensureLocalExtIgnored": exempt("writes a .gitignore line, not a record"),
	"cmd/ethos.exportSoulSpec":        exempt("out of scope (identity graph): exports identity content to a directory the operator names"),
	"cmd/ethos.writeConfigFile":       exempt("out of scope: .punt-labs/ethos.yaml repo config"),
}

// outOfScopePackages are packages whose writes are the identity graph,
// operator-authored configuration, or untracked global state, not mission
// or audit records. A package in neither this list nor writerRegistry
// fails the test when it gains a writer.
var outOfScopePackages = map[string]string{
	"cmd/e2e-attribute":  "developer test tool",
	"internal/adr":       reasonGlobal + "; ADRs move into the repo under ethos-yjh0, which adds its writer to the in-scope set",
	"internal/attribute": "operator-authored talents, personalities, writing styles",
	"internal/claudemd":  "import lines in CLAUDE.md",
	"internal/doctor":    "health-check probe files",
	"internal/enable":    "enablement markers, deposited guides, .gitignore lines",
	"internal/githook":   "git hook scripts",
	"internal/identity":  "operator-authored identities and extensions",
	"internal/role":      "operator-authored roles",
	"internal/seed":      "starter content deployed by ethos seed",
	"internal/session":   "session roster: " + reasonGlobal,
	"internal/team":      "operator-authored teams",
	"internal/vendor":    "vendored team content",
}

// chokePoints are the functions that apply the shared redactor. A writer
// in an in-scope package must call one of them.
var chokePoints = map[string]bool{
	"RedactedYAML":      true,
	"RedactRecord":      true,
	"redactYAMLDoc":     true,
	"redactJSONLines":   true,
	"redactToolInput":   true,
	"redactReason":      true,
	"redactLegacyLines": true,
	"Prose":             true,
	"Body":              true,
	"Content":           true,
}

// osWritePrims are the os calls that create or move a file.
var osWritePrims = map[string]bool{
	"WriteFile": true, "OpenFile": true, "Create": true, "CreateTemp": true,
	"Rename": true, "Symlink": true, "Link": true, "Truncate": true,
}

type callSite struct {
	name string
	os   bool // a call to os.<name>
	args []ast.Expr
}

type funcInfo struct {
	key     string
	prims   bool                  // calls an os write primitive directly
	callees map[string]bool       // bare names of every function it calls
	calls   []callSite            // every call, with its arguments
	assigns map[string][]ast.Expr // local name -> every expression assigned to it
	zero    map[string]bool       // local names declared "var x T" with no value
}

// scanFuncs parses sources (path relative to the repo root -> content)
// and returns every function keyed "<dir>.<Func>" or "<dir>.<Type>.<Method>".
func scanFuncs(t *testing.T, sources map[string]string) map[string]*funcInfo {
	t.Helper()
	out := map[string]*funcInfo{}
	fset := token.NewFileSet()
	for path, src := range sources {
		f, err := parser.ParseFile(fset, path, src, 0)
		require.NoError(t, err, path)
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				name = recvName(fd.Recv.List[0].Type) + "." + name
			}
			fi := &funcInfo{key: dir + "." + name, callees: map[string]bool{}, assigns: map[string][]ast.Expr{}, zero: map[string]bool{}}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.AssignStmt:
					noteAssign(fi, n.Lhs, n.Rhs)
				case *ast.RangeStmt:
					noteAssign(fi, []ast.Expr{n.Key, n.Value}, []ast.Expr{n.X})
				case *ast.ValueSpec:
					lhs := make([]ast.Expr, len(n.Names))
					for i, id := range n.Names {
						lhs[i] = id
					}
					noteAssign(fi, lhs, n.Values)
					if len(n.Values) == 0 {
						for _, id := range n.Names {
							fi.zero[id.Name] = true
						}
					}
				case *ast.CallExpr:
					cs := callSite{args: n.Args}
					switch fn := n.Fun.(type) {
					case *ast.Ident:
						cs.name = fn.Name
					case *ast.SelectorExpr:
						cs.name = fn.Sel.Name
						if x, ok := fn.X.(*ast.Ident); ok && x.Name == "os" {
							cs.os = true
							fi.prims = fi.prims || osWritePrims[fn.Sel.Name]
						}
					}
					if cs.name != "" {
						fi.callees[cs.name] = true
						fi.calls = append(fi.calls, cs)
					}
				}
				return true
			})
			// Platform-split files define the same function twice; merge
			// them so neither build's body is invisible.
			if prev, ok := out[fi.key]; ok {
				prev.prims = prev.prims || fi.prims
				for c := range fi.callees {
					prev.callees[c] = true
				}
				prev.calls = append(prev.calls, fi.calls...)
				for k, v := range fi.assigns {
					prev.assigns[k] = append(prev.assigns[k], v...)
				}
				for k := range fi.zero {
					prev.zero[k] = true
				}
				continue
			}
			out[fi.key] = fi
		}
	}
	return out
}

// noteAssign records, for each identifier on the left, the expression
// assigned to it: its own right-hand side, or the sole call it unpacks.
func noteAssign(fi *funcInfo, lhs, rhs []ast.Expr) {
	for i, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok {
			continue
		}
		switch {
		case len(rhs) == len(lhs):
			fi.assigns[id.Name] = append(fi.assigns[id.Name], rhs[i])
		case len(rhs) == 1:
			fi.assigns[id.Name] = append(fi.assigns[id.Name], rhs[0])
		}
	}
}

// redacted reports whether e contains a call to a choke point, directly or
// through a local variable assigned from one. An append call counts only
// when every one of its arguments does. path holds the variables being
// resolved now, to stop cycles; a name is not skipped for having been
// visited elsewhere in the expression.
func redacted(e ast.Expr, fi *funcInfo, choke map[string]bool, path map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := n.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "append" && len(n.Args) > 0 {
					found = found || allRedacted(n.Args, fi, choke, path)
					return false
				}
				found = found || choke[fn.Name]
			case *ast.SelectorExpr:
				found = found || choke[fn.Sel.Name]
			}
		case *ast.Ident:
			found = found || identRedacted(n.Name, fi, choke, path)
		}
		return !found
	})
	return found
}

// allRedacted reports whether every expression in es is redacted and es is
// not empty.
func allRedacted(es []ast.Expr, fi *funcInfo, choke map[string]bool, path map[string]bool) bool {
	ok := len(es) > 0
	for _, a := range es {
		ok = ok && redacted(a, fi, choke, path)
	}
	return ok
}

// identRedacted reports whether the local variable name is fed by a choke
// point. A variable declared "var x T" is redacted only if every later
// assignment to it is; any other is redacted if some assignment is.
func identRedacted(name string, fi *funcInfo, choke map[string]bool, path map[string]bool) bool {
	if path[name] {
		return false
	}
	path[name] = true
	defer delete(path, name)

	as := fi.assigns[name]
	if fi.zero[name] {
		ok := len(as) > 0
		for _, a := range as {
			ok = ok && assignRedacted(name, a, fi, choke, path)
		}
		return ok
	}
	for _, a := range as {
		if assignRedacted(name, a, fi, choke, path) {
			return true
		}
	}
	return false
}

// assignRedacted reports whether the expression a, assigned to name, is
// redacted. In name = append(name, ...) the first argument is name itself,
// which adds no bytes; only the rest must be redacted.
func assignRedacted(name string, a ast.Expr, fi *funcInfo, choke map[string]bool, path map[string]bool) bool {
	// A choke point held as a function value (f := r.Content) is redacted
	// when f is called.
	if s, ok := a.(*ast.SelectorExpr); ok && choke[s.Sel.Name] {
		return true
	}
	if c, ok := a.(*ast.CallExpr); ok {
		if fn, ok := c.Fun.(*ast.Ident); ok && fn.Name == "append" && len(c.Args) > 0 {
			if id, ok := c.Args[0].(*ast.Ident); ok && id.Name == name {
				return allRedacted(c.Args[1:], fi, choke, path)
			}
		}
	}
	return redacted(a, fi, choke, path)
}

func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvName(x.X)
	}
	return "?"
}

func bareName(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[i+1:]
	}
	return key
}

func keyDir(key string) string {
	// "<dir>.<Func>" or "<dir>.<Type>.<Method>": dir ends before the first
	// '.' that follows the last '/'.
	slash := strings.LastIndex(key, "/")
	dot := strings.Index(key[slash+1:], ".")
	return key[:slash+1+dot]
}

// dataArgIndex reports whether cs writes bytes to disk through os.WriteFile
// or a registered sink, and which argument carries them.
func dataArgIndex(cs callSite, sinks map[string]writerEntry) (int, bool) {
	if cs.os && cs.name == "WriteFile" {
		return 1, true
	}
	if e, ok := sinks[cs.name]; ok {
		return e.dataArg, true
	}
	return 0, false
}

// checkWriters returns one message per violation of the coverage
// invariant over funcs.
func checkWriters(funcs map[string]*funcInfo, registry map[string]writerEntry, exemptPkgs map[string]string, choke map[string]bool) []string {
	var bad []string

	sinks := map[string]writerEntry{}
	for k, e := range registry {
		if e.class == classSink {
			sinks[bareName(k)] = e
		}
	}

	writers := map[string]bool{}
	for k, fi := range funcs {
		if _, ok := exemptPkgs[keyDir(k)]; ok {
			continue
		}
		if fi.prims {
			writers[k] = true
			continue
		}
		for c := range fi.callees {
			if _, ok := sinks[c]; ok {
				writers[k] = true
			}
		}
	}

	for k := range writers {
		if _, ok := registry[k]; ok {
			continue
		}
		calls := false
		for c := range funcs[k].callees {
			calls = calls || choke[c]
		}
		if !calls {
			bad = append(bad, fmt.Sprintf(
				"writer %s calls no redaction choke point: build its content with mission.RedactedYAML or mission.RedactRecord, or register it as a sink, downstream or exempt with a reason", k))
			continue
		}
		// Presence is not enough: each write must itself be fed by a
		// choke point, or one redacted file excuses an unredacted one.
		for _, cs := range funcs[k].calls {
			idx, isWrite := dataArgIndex(cs, sinks)
			if !isWrite {
				continue
			}
			if idx >= len(cs.args) || !redacted(cs.args[idx], funcs[k], choke, map[string]bool{}) {
				bad = append(bad, fmt.Sprintf(
					"writer %s: the %s call's data argument is not fed by a redaction choke point; redact the bytes it writes", k, cs.name))
			}
		}
	}

	for k, e := range registry {
		fi, exists := funcs[k]
		if !exists {
			bad = append(bad, fmt.Sprintf("stale registry entry %s: no such function", k))
			continue
		}
		if e.class != classCheckpoint && !writers[k] {
			bad = append(bad, fmt.Sprintf("stale registry entry %s: no longer writes a file or calls a sink", k))
		}
		if e.class == classSink && !e.hasDataArg {
			bad = append(bad, fmt.Sprintf("sink %s has no data-argument index; register it with sink(index, reason)", k))
		}
		if e.class != classCheckpoint && strings.TrimSpace(e.reason) == "" {
			bad = append(bad, fmt.Sprintf("registry entry %s has no reason", k))
		}
		switch e.class {
		case classCheckpoint:
			for _, c := range e.calls {
				if !fi.callees[c] {
					bad = append(bad, fmt.Sprintf("%s is registered as redacting but does not call %s", k, c))
				}
			}
		case classDownstream:
			via := keyDir(k) + "." + e.via
			vfi, vok := funcs[via]
			vreg, vregOK := registry[via]
			redactsHere := vok && (vregOK && vreg.class == classCheckpoint)
			if vok {
				for c := range vfi.callees {
					redactsHere = redactsHere || choke[c]
				}
			}
			if !redactsHere {
				bad = append(bad, fmt.Sprintf("%s is downstream of %s, which neither calls a redaction choke point nor is a registered checkpoint", k, via))
			}
			var callers []string
			for ck, cfi := range funcs {
				if cfi.callees[bareName(k)] {
					callers = append(callers, ck)
				}
			}
			sort.Strings(callers)
			if len(callers) != 1 || callers[0] != via {
				bad = append(bad, fmt.Sprintf("%s must have %s as its only caller, has %v", k, via, callers))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// loadSources reads every non-test .go file under the packages that can
// write into a tracked tree.
func loadSources(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, top := range []string{"cmd", "internal", "plugin"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

// badImports reports every import the writer scan cannot see through:
// io/ioutil (a second spelling of the os write primitives) and an os
// import under any other name (an alias, a dot or a blank import), which
// hides os.WriteFile from the call matcher.
func badImports(t *testing.T, sources map[string]string) []string {
	t.Helper()
	var bad []string
	fset := token.NewFileSet()
	for path, src := range sources {
		f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		require.NoError(t, err, path)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			switch {
			case p == "io/ioutil":
				bad = append(bad, path+": imports io/ioutil; use os")
			case p == "os" && imp.Name != nil:
				bad = append(bad, fmt.Sprintf("%s: imports os as %s; import it unaliased so the writer scan sees it", path, imp.Name.Name))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestScannedSourcesImportOsPlainly(t *testing.T) {
	assert.Empty(t, badImports(t, loadSources(t, filepath.Join("..", ".."))))
}

func TestBadImports_Mutants(t *testing.T) {
	get := func(src string) []string {
		return badImports(t, map[string]string{"internal/p/p.go": src})
	}
	assert.Empty(t, get("package p\nimport \"os\"\n"))
	assert.Empty(t, get("package p\nimport (\n\t\"fmt\"\n\t\"os/exec\"\n)\n"))
	require.Len(t, get("package p\nimport \"io/ioutil\"\n"), 1)
	got := get("package p\nimport o \"os\"\n")
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "imports os as o")
	assert.Len(t, get("package p\nimport . \"os\"\n"), 1)
	assert.Len(t, get("package p\nimport _ \"os\"\n"), 1)
}

// TestTrackedWritersAreRedacted replaces the claim that used to sit in
// PathRedactor's doc comment ("every artifact ethos writes into a
// git-tracked tree passes through one of these") with a check.
func TestTrackedWritersAreRedacted(t *testing.T) {
	funcs := scanFuncs(t, loadSources(t, filepath.Join("..", "..")))
	require.Greater(t, len(funcs), 500, "scan found implausibly few functions; the walk is broken")

	assert.Empty(t, checkWriters(funcs, writerRegistry, outOfScopePackages, chokePoints))
}

// TestCheckWriters_Mutants proves the checker fails when it should: a
// stray WriteFile, a stray caller of a sink, a redactor call removed
// from a writer, a hollowed-out choke point, a stale entry, and a
// downstream entry with a second caller.
func TestCheckWriters_Mutants(t *testing.T) {
	const base = `package p
import "os"
func RedactRecord(s string) string { return s }
func Inner(b []byte) error { return os.WriteFile("x", b, 0o600) }
func WriteRecord(s string) error { return Inner([]byte(RedactRecord(s))) }
func Lock() error { _, err := os.OpenFile("l", 0, 0); return err }
`
	choke := map[string]bool{"RedactRecord": true}
	registry := func() map[string]writerEntry {
		return map[string]writerEntry{
			"internal/p.Inner":        sink(0, "test sink"),
			"internal/p.Lock":         exempt("lock"),
			"internal/p.RedactRecord": checkpoint(),
		}
	}
	run := func(src string, reg map[string]writerEntry) []string {
		return checkWriters(scanFuncs(t, map[string]string{"internal/p/p.go": src}), reg, nil, choke)
	}

	t.Run("baseline passes", func(t *testing.T) {
		assert.Empty(t, run(base, registry()))
	})

	t.Run("a stray WriteFile fails", func(t *testing.T) {
		src := base + "\nfunc Stray(b []byte) error { return os.WriteFile(\"y\", b, 0o600) }\n"
		got := run(src, registry())
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "writer internal/p.Stray calls no redaction choke point")
	})

	t.Run("a stray os.Create fails", func(t *testing.T) {
		src := base + "\nfunc Stray() { os.Create(\"y\") }\n"
		got := run(src, registry())
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Stray")
	})

	t.Run("a stray caller of a sink fails", func(t *testing.T) {
		src := base + "\nfunc Stray(b []byte) error { return Inner(b) }\n"
		got := run(src, registry())
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Stray")
	})

	t.Run("removing the redactor call from a writer fails", func(t *testing.T) {
		src := strings.Replace(base, "Inner([]byte(RedactRecord(s)))", "Inner([]byte(s))", 1)
		got := run(src, registry())
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.WriteRecord calls no redaction choke point")
	})

	t.Run("hollowing out a choke point fails", func(t *testing.T) {
		reg := registry()
		reg["internal/p.RedactRecord"] = checkpoint("NewPathRedactor")
		got := run(base, reg)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "RedactRecord is registered as redacting but does not call NewPathRedactor")
	})

	t.Run("a stale entry fails", func(t *testing.T) {
		reg := registry()
		reg["internal/p.Gone"] = exempt("was a lock")
		got := run(base, reg)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "stale registry entry internal/p.Gone")
	})

	t.Run("an exempt entry that no longer writes fails", func(t *testing.T) {
		src := strings.Replace(base, `_, err := os.OpenFile("l", 0, 0); return err`, `return nil`, 1)
		got := run(src, registry())
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "no longer writes")
	})

	t.Run("a reasonless exemption fails", func(t *testing.T) {
		reg := registry()
		reg["internal/p.Lock"] = exempt("")
		got := run(base, reg)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "has no reason")
	})

	t.Run("downstream needs its single registered caller", func(t *testing.T) {
		reg := registry()
		reg["internal/p.Inner"] = downstream("WriteRecord", "only WriteRecord calls it")
		reg["internal/p.WriteRecord"] = checkpoint("RedactRecord")
		assert.Empty(t, run(base, reg))
		src := base + "\nfunc Other(b []byte) { _ = Inner(b) }\n"
		got := strings.Join(run(src, reg), "\n")
		assert.Contains(t, got, "must have internal/p.WriteRecord as its only caller")
	})

	t.Run("a body-only writer passes only through Body or Content", func(t *testing.T) {
		src := base + "\nfunc Prompt(b []byte) error { return Inner(Body(b)) }\nfunc Body(b []byte) []byte { return b }\n"
		got := checkWriters(scanFuncs(t, map[string]string{"internal/p/p.go": src}), registry(), nil,
			map[string]bool{"RedactRecord": true, "Body": true})
		assert.Empty(t, got)
		got = checkWriters(scanFuncs(t, map[string]string{"internal/p/p.go": src}), registry(), nil, choke)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Prompt")
	})

	t.Run("one redacted write does not excuse an unredacted one", func(t *testing.T) {
		const skeleton = `
func Body(b []byte) []byte { return b }
func Skeleton(body []byte, y string) error {
	if err := os.WriteFile("prompt.md", PROMPT, 0o600); err != nil {
		return err
	}
	data := RedactRecord(y)
	return Inner([]byte(data))
}
`
		chokeBody := map[string]bool{"RedactRecord": true, "Body": true}
		runBody := func(src string) []string {
			return checkWriters(scanFuncs(t, map[string]string{"internal/p/p.go": src}), registry(), nil, chokeBody)
		}
		got := runBody(base + strings.Replace(skeleton, "PROMPT", "body", 1))
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Skeleton: the WriteFile call's data argument is not fed by a redaction choke point")
		assert.Empty(t, runBody(base+strings.Replace(skeleton, "PROMPT", "Body(body)", 1)))
		viaVar := strings.Replace(skeleton, "if err := os.WriteFile", "pb := Body(body)\n\tif err := os.WriteFile", 1)
		assert.Empty(t, runBody(base+strings.Replace(viaVar, "PROMPT", "pb", 1)))
	})

	t.Run("a redacted path does not excuse raw bytes", func(t *testing.T) {
		stray := func(body string) []string {
			return run(base+"\nfunc Stray(name string, dir string, b []byte) error {\n"+body+"\n}\n", registry())
		}
		got := stray(`return os.WriteFile(RedactRecord(name), b, 0o600)`)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Stray: the WriteFile call's data argument is not fed")
		got = stray(`return Inner(append([]byte(RedactRecord(dir)), b...))`)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Stray: the Inner call's data argument is not fed")
		assert.Empty(t, stray(`return os.WriteFile(name, []byte(RedactRecord(dir)), 0o600)`))
		assert.Empty(t, stray(`return Inner(append([]byte(RedactRecord(dir)), RedactRecord(name)...))`))
	})

	t.Run("an accumulator is as redacted as what is appended to it", func(t *testing.T) {
		acc := func(elem string) []string {
			return run(base+"\nfunc Stray(name string, b []byte) error {\n\tvar out []byte\n\tout = append(out, "+elem+"...)\n\treturn Inner(out)\n}\n", registry())
		}
		assert.Empty(t, acc(`[]byte(RedactRecord(name))`))
		got := acc(`b`)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "internal/p.Stray calls no redaction choke point")
	})

	t.Run("append does not skip a name that is not its own accumulator", func(t *testing.T) {
		const defs = "\nfunc Body(b []byte) []byte { return b }\nfunc fmtLine(a, b []byte) []byte { return a }\n"
		chokeBody := map[string]bool{"RedactRecord": true, "Body": true}
		stray := func(body string) []string {
			src := base + defs + "\nfunc Stray(body []byte) error {\n" + body + "\n}\n"
			return checkWriters(scanFuncs(t, map[string]string{"internal/p/p.go": src}), registry(), nil, chokeBody)
		}
		for name, body := range map[string]string{
			"var name reassigned to raw": `_ = Body(body)
	var raw []byte
	raw = body
	return os.WriteFile("p", append([]byte(RedactRecord("x")), raw...), 0o600)`,
			"name visited earlier in the expression": `return os.WriteFile("p", append(fmtLine(body, Body(body)), body...), 0o600)`,
			"name visited earlier through a variable": `h := fmtLine(body, Body(body))
	return os.WriteFile("p", append(h, body...), 0o600)`,
		} {
			got := stray(body)
			require.Len(t, got, 1, name)
			assert.Contains(t, got[0], "the WriteFile call's data argument is not fed", name)
		}
		assert.Empty(t, stray(`var out []byte
	out = append(out, Body(body)...)
	return os.WriteFile("p", out, 0o600)`))
		assert.Empty(t, stray(`var out []byte
	out = Body(body)
	return os.WriteFile("p", out, 0o600)`))
	})

	t.Run("a sink without a data-argument index fails", func(t *testing.T) {
		reg := registry()
		reg["internal/p.Inner"] = writerEntry{class: classSink, reason: "test sink"}
		got := run(base, reg)
		require.Len(t, got, 1)
		assert.Contains(t, got[0], "sink internal/p.Inner has no data-argument index")
	})

	t.Run("an out-of-scope package is skipped", func(t *testing.T) {
		got := checkWriters(
			scanFuncs(t, map[string]string{"internal/q/q.go": "package q\nimport \"os\"\nfunc F() { os.WriteFile(\"x\", nil, 0) }\n"}),
			nil, map[string]string{"internal/q": "identity graph"}, choke)
		assert.Empty(t, got)
	})
}

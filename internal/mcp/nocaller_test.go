package mcp

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/doctor"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// underClaudeCode makes the test an MCP server Claude Code started: Claude Code
// is the caller's harness, and the server has CLAUDECODE but no CLAUDE_PID.
func underClaudeCode(t *testing.T) {
	t.Helper()
	old := resolve.UnderClaudeCode
	resolve.UnderClaudeCode = func() bool { return true }
	t.Cleanup(func() { resolve.UnderClaudeCode = old })
	t.Setenv("CLAUDE_PID", "")
}

// notUnderClaudeCode is the control: the same environment from a client that is
// not Claude Code, where the harness-neutral chain still answers.
func notUnderClaudeCode(t *testing.T) {
	t.Helper()
	old := resolve.UnderClaudeCode
	resolve.UnderClaudeCode = func() bool { return false }
	t.Cleanup(func() { resolve.UnderClaudeCode = old })
}

// poisonEnv is the environment a stale or shared process hands the server: it
// names an existing session and participant, so the env chain would answer
// with a plausible identity (bob) for a call whose caller nobody declared.
func poisonEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ETHOS_SESSION", "sess-outer")
	t.Setenv("ETHOS_AGENT_ID", "999")
	t.Setenv("USER", "bob")
}

func requireNamedRefusal(t *testing.T, text string) {
	t.Helper()
	assert.Contains(t, text, "Claude Code call arrived without _caller")
	assert.Contains(t, text, "PreToolUse hook did not declare the caller")
	assert.Contains(t, text, "is it installed, enabled, and not killed?")
}

// With the hook killed or bypassed, a Claude Code call carries no _caller. The
// server refuses it with the named error. The same call from a client that is
// not Claude Code is answered from the env chain, which is the control that
// shows the refusal is what stands between the call and a wrong answer.
func TestNoCaller_WhoamiRefusedUnderClaudeCode(t *testing.T) {
	h := hookHandler(t)
	poisonEnv(t)

	notUnderClaudeCode(t)
	result, text := whoami(t, h, map[string]any{})
	require.False(t, result.IsError, text)
	assert.Equal(t, "bob", handleOf(t, text), "control: the env chain answers a non-Claude-Code client")

	underClaudeCode(t)
	result, text = whoami(t, h, map[string]any{})
	require.True(t, result.IsError, "a Claude Code call with no _caller must not be answered: %s", text)
	requireNamedRefusal(t, text)
	assert.NotContains(t, text, "bob")
}

func TestNoCaller_SessionToolRefusedUnderClaudeCode(t *testing.T) {
	h := hookHandler(t)
	poisonEnv(t)
	call := func(args map[string]any) (bool, string) {
		result, err := h.handleSession(context.Background(), callTool(args))
		require.NoError(t, err)
		return result.IsError, resultText(t, result)
	}

	notUnderClaudeCode(t)
	isErr, text := call(map[string]any{"method": "roster"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "sess-outer", "control: the env chain answers a non-Claude-Code client")

	rostersBefore := map[string]*session.Roster{}
	for _, sid := range []string{"sess-outer", "sess-lead"} {
		r, err := h.sessionStore.Load(sid)
		require.NoError(t, err)
		rostersBefore[sid] = r
	}

	underClaudeCode(t)
	for name, args := range map[string]map[string]any{
		"roster":                       {"method": "roster"},
		"join":                         {"method": "join", "agent_id": "newcomer", "persona": "carol"},
		"leave":                        {"method": "leave", "agent_id": "4242"},
		"iam":                          {"method": "iam", "persona": "carol"},
		"iam with an explicit session": {"method": "iam", "persona": "carol", "session_id": "sess-lead"},
	} {
		t.Run(name, func(t *testing.T) {
			isErr, text := call(args)
			require.True(t, isErr, text)
			requireNamedRefusal(t, text)
		})
	}
	for sid, before := range rostersBefore {
		after, err := h.sessionStore.Load(sid)
		require.NoError(t, err)
		assert.Equal(t, before.Participants, after.Participants, "a refused call changes nothing in %s", sid)
	}
}

// An explicit session_id is an administrative target, not a declaration about
// the caller, and keeps working with no caller declared.
func TestNoCaller_ExplicitSessionTargetsStillWork(t *testing.T) {
	h := hookHandler(t)
	poisonEnv(t)
	underClaudeCode(t)
	for _, args := range []map[string]any{
		{"method": "roster", "session_id": "sess-lead"},
		{"method": "join", "session_id": "sess-lead", "agent_id": "newcomer", "persona": "carol"},
		{"method": "leave", "session_id": "sess-lead", "agent_id": "newcomer"},
	} {
		result, err := h.handleSession(context.Background(), callTool(args))
		require.NoError(t, err)
		assert.False(t, result.IsError, "%v: %s", args["method"], resultText(t, result))
	}
}

// The server cannot tell a model-written _caller from the hook's, so one that
// is well formed and names a real participant is routed (DES-074: routing, not
// authentication). One that names nobody is refused, and never replaced by the
// env chain: with the hook killed, a model-supplied _caller gets no further
// than the roster check.
func TestNoCaller_ForgedCallerWithoutHookIsStillChecked(t *testing.T) {
	h := hookHandler(t)
	poisonEnv(t)
	underClaudeCode(t)
	for name, c := range map[string]any{
		"unknown participant": declared("sess-lead", "forged"),
		"unknown session":     declared("sess-forged", "4242"),
		"malformed":           "sess-lead/4242",
	} {
		t.Run(name, func(t *testing.T) {
			result, text := whoami(t, h, map[string]any{callerArg: c})
			require.True(t, result.IsError, text)
			assert.NotContains(t, text, "bob")
			assert.NotContains(t, text, "without _caller", "a declared caller is judged on its own, not refused as absent")
		})
	}
}

func TestNoCaller_DoctorHumanIdentityRow(t *testing.T) {
	h := hookHandler(t)
	poisonEnv(t)
	humanRow := func(t *testing.T, args map[string]any) string {
		t.Helper()
		result, err := h.handleDoctor(context.Background(), callTool(args))
		require.NoError(t, err)
		for _, line := range strings.Split(resultText(t, result), "\n") {
			if strings.Contains(line, doctor.HumanIdentityRow) {
				return line
			}
		}
		t.Fatalf("no %q row in %s", doctor.HumanIdentityRow, resultText(t, result))
		return ""
	}

	underClaudeCode(t)
	row := humanRow(t, map[string]any{})
	assert.Contains(t, row, "FAIL")
	assert.Contains(t, row, "without _caller")
	assert.NotContains(t, row, "bob", "no row derived from the environment stands")

	// The row's name is one constant, so the replacement cannot miss it.
	assert.Equal(t, "Human identity", doctor.HumanIdentityRow)
	assert.Equal(t, doctor.HumanIdentityRow, doctor.CheckHumanIdentity(h.store, h.sessionStore, "").Name)

	// A declared caller is answered from its participant.
	row = humanRow(t, map[string]any{callerArg: declared("sess-lead", "agent-sub1")})
	assert.Contains(t, row, "PASS")
	assert.Contains(t, row, "carol")

	notUnderClaudeCode(t)
	row = humanRow(t, map[string]any{})
	assert.NotContains(t, row, "without _caller", "control: a non-Claude-Code client keeps the env chain")
}

// Mission binding needs the caller's session. Under Claude Code with no caller
// it cannot be known: create says so in its warnings and writes nothing;
// close and abandon fail, because their binding clear did not happen.
func TestNoCaller_MissionBindingRefusedUnderClaudeCode(t *testing.T) {
	const sess = "sess-nocaller-mission"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_SESSION", sess)
	t.Setenv("ETHOS_AGENT_ID", "")
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")

	h := testHandlerWithSessions(t)
	seedSessionRoster(t, h.sessionStore, sess)
	h.missionStore = h.missionStore.WithRepoRoot(t.TempDir())
	t.Cleanup(func() { h.missionStore = h.missionStore.WithRepoRoot("") })
	create := func(yaml string) mission.Contract {
		result, err := h.handleMission(context.Background(), callTool(map[string]any{"method": "create", "contract": yaml}))
		require.NoError(t, err)
		require.False(t, result.IsError, resultText(t, result))
		var c mission.Contract
		require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &c))
		return c
	}

	t.Run("create", func(t *testing.T) {
		underClaudeCode(t)
		result, err := h.handleMission(context.Background(), callTool(map[string]any{"method": "create", "contract": validContractYAML}))
		require.NoError(t, err)
		require.False(t, result.IsError, "the mission is created")
		var payload struct {
			MissionID string   `json:"mission_id"`
			Warnings  []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &payload))
		require.Len(t, payload.Warnings, 1)
		requireNamedRefusal(t, payload.Warnings[0])
		pending, _, err := mission.ReadDispatchPending(globalRoot, sess)
		require.NoError(t, err)
		assert.Empty(t, pending, "no dispatch is bound to a session nobody declared")
		active, err := mission.ReadActiveMission(globalRoot, sess)
		require.NoError(t, err)
		assert.Empty(t, active)
	})

	t.Run("close", func(t *testing.T) {
		notUnderClaudeCode(t)
		c := create(contractYAMLWithWriteSet("internal/nocaller-close/"))
		submitResultForMCP(t, h, c.MissionID)
		underClaudeCode(t)
		result, err := h.handleMission(context.Background(), callTool(map[string]any{"method": "close", "mission_id": c.MissionID}))
		require.NoError(t, err)
		require.True(t, result.IsError, resultText(t, result))
		text := resultText(t, result)
		requireNamedRefusal(t, text)
		assert.Contains(t, text, c.MissionID)
		assert.Contains(t, text, "is "+mission.StatusClosed)
		loaded, err := h.missionStore.Load(c.MissionID)
		require.NoError(t, err)
		assert.Equal(t, mission.StatusClosed, loaded.Status)
	})

	t.Run("abandon", func(t *testing.T) {
		notUnderClaudeCode(t)
		c := create(contractYAMLWithWriteSet("internal/nocaller-abandon/"))
		underClaudeCode(t)
		result, err := h.handleMission(context.Background(), callTool(map[string]any{
			"method": "abandon", "mission_id": c.MissionID, "reason": "never dispatched"}))
		require.NoError(t, err)
		require.True(t, result.IsError, resultText(t, result))
		text := resultText(t, result)
		requireNamedRefusal(t, text)
		assert.Contains(t, text, "is "+mission.StatusAbandoned)
	})
}

// The only code in this package that may answer "who is calling?" without a
// declared caller is nocaller.go, which refuses first under Claude Code. These
// are the calls that answer it from somewhere else: the process tree, the
// environment, the pointer file. A new one anywhere else is a path around the
// refusal.
func TestNoCallerResolutionLivesInOneFile(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var found, offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		hits := noCallerCalls(t, f, string(src))
		if f == "nocaller.go" {
			found = hits
			continue
		}
		for _, h := range hits {
			offenders = append(offenders, f+": "+h)
		}
	}
	assert.Empty(t, offenders, "caller resolution outside nocaller.go; route it through a noCaller* method")
	// The scan is not vacuous: the approved file is where each of them lives.
	for _, want := range []string{"resolve.SessionID", "resolve.Resolve", "process.FindClaudePID", "process.LegacyClaudePID", `"ETHOS_AGENT_ID"`} {
		assert.Contains(t, found, want, "nocaller.go is the approved home of %s", want)
	}
}

// noCallerCalls lists the banned references in one source file: selectors on
// the resolve and process packages (by import path, so an alias cannot hide
// one) and string literals naming the variables the env chain reads.
func noCallerCalls(t *testing.T, name, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	require.NoError(t, err)

	const modPath = "github.com/punt-labs/ethos/v4/internal/"
	banned := map[string]map[string]bool{
		"resolve": {"SessionID": true, "Resolve": true},
		"process": {"FindClaudePID": true, "LegacyClaudePID": true, "ClaudePIDFromEnvCorroborated": true},
	}
	local := map[string]string{} // local import name -> package
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		require.NoError(t, err)
		pkg := strings.TrimPrefix(path, modPath)
		if _, ok := banned[pkg]; !ok || !strings.HasPrefix(path, modPath) {
			continue
		}
		n := pkg
		if imp.Name != nil {
			n = imp.Name.Name
		}
		local[n] = pkg
	}

	var hits []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if pkg, ok := local[id.Name]; ok && banned[pkg][x.Sel.Name] {
					hits = append(hits, pkg+"."+x.Sel.Name)
				}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil {
					for _, env := range []string{"ETHOS_SESSION", "ETHOS_AGENT_ID"} {
						if strings.Contains(v, env) {
							hits = append(hits, strconv.Quote(env))
						}
					}
				}
			}
		}
		return true
	})
	sort.Strings(hits)
	return hits
}

// The scan finds what it is meant to, including through an import alias.
func TestNoCallerCallsScanner(t *testing.T) {
	src := `package mcp

import (
	"os"

	res "github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/process"
)

func f() {
	res.SessionID(nil)
	res.Resolve(nil, nil, "")
	_ = process.FindClaudePID()
	_ = process.LegacyClaudePID()
	_ = os.Getenv("ETHOS_SESSION")
	_ = os.Getenv("ETHOS_AGENT_ID")
	_ = res.Roots()
	_ = process.UnderClaudeCode
}
`
	assert.Equal(t, []string{
		`"ETHOS_AGENT_ID"`, `"ETHOS_SESSION"`,
		"process.FindClaudePID", "process.LegacyClaudePID",
		"resolve.Resolve", "resolve.SessionID",
	}, noCallerCalls(t, "x.go", src))
}

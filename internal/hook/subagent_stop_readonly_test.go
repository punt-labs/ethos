//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/require"
)

// TestHandoffReadOnlyStop proves a subagent stop skips handoff checks only
// when the agent made no Write/Edit call and the checkout is unchanged since
// SubagentStart; any Bash-style change still runs the `files: all` gate.
func TestHandoffReadOnlyStop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		act     func(t *testing.T, root string)
		audit   []auditEntry
		noStart bool
		runs    bool
	}{
		{name: "read-only", runs: false},
		{name: "read-only beside another agent's write", audit: []auditEntry{auditWrite("other", "dirty.go")}, runs: false},
		{name: "write with unchanged checkout", audit: []auditEntry{auditWrite("sub", "dirty.go")}, runs: true},
		{name: "edit with unchanged checkout", audit: []auditEntry{{Tool: "Edit", AgentID: "sub", ToolInput: map[string]any{"file_path": "<repo>/dirty.go"}, PathFormat: auditPathFormat}}, runs: true},
		{name: "bash edits a clean file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "clean.go"), "package changed\n") }, runs: true},
		{name: "bash edits a dirty file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "dirty.go"), "package b\n") }, runs: true},
		{name: "bash edits a renamed file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "renamed file.go"), "package c\n") }, runs: true},
		{name: "bash edits an untracked file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "untracked.go"), "package d\n") }, runs: true},
		{name: "bash creates a file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "new.go"), "") }, runs: true},
		{name: "bash removes a file", act: func(t *testing.T, root string) { require.NoError(t, os.Remove(filepath.Join(root, "clean.go"))) }, runs: true},
		{name: "bash stages a file", act: func(t *testing.T, root string) { stageGit(t, root, "add", "dirty.go") }, runs: true},
		{name: "bash commits", act: func(t *testing.T, root string) {
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qam", "bash")
		}, runs: true},
		{name: "no start fingerprint", noStart: true, runs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			checkPolicy(t, root, "project", "  - name: full-gate\n    stage: handoff\n    files: all\n    run: [probe]\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			stageGit(t, root, "add", ".punt-labs")
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "policy")
			marker := filepath.Join(t.TempDir(), "ran")
			t.Setenv("HANDOFF_MARKER", marker)
			postEditTool(t, bin, "probe", "printf ran > \"$HANDOFF_MARKER\"; exit 1\n")
			writeFile(t, filepath.Join(root, "dirty.go"), "package a\n")
			writeFile(t, filepath.Join(root, "clean.go"), "package clean\n")
			stageGit(t, root, "add", "dirty.go", "clean.go")
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "dirty")
			writeFile(t, filepath.Join(root, "dirty.go"), "package a // dirty\n")
			stageGit(t, root, "mv", "README", "renamed file.go")
			writeFile(t, filepath.Join(root, "untracked.go"), "package u\n")

			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("ro", session.Participant{AgentID: "human"}, session.Participant{AgentID: "main"}, "", ""))
			start, err := json.Marshal(map[string]any{"session_id": "ro", "agent_id": "sub", "agent_type": "worker"})
			require.NoError(t, err)
			if tc.noStart {
				require.NoError(t, ss.Join("ro", session.Participant{AgentID: "sub", AgentType: "worker"}))
			} else {
				_, err = captureStdout(t, func() error {
					return HandleSubagentStartWithDeps(bytes.NewReader(start), SubagentStartDeps{Identities: identity.NewStore(t.TempDir()), Sessions: ss})
				})
				require.NoError(t, err)
			}
			roster, err := ss.Load("ro")
			require.NoError(t, err)
			require.Equal(t, tc.noStart, roster.FindParticipant("sub").CheckoutState == "")
			if tc.act != nil {
				tc.act(t, root)
			}
			for i := range tc.audit {
				tc.audit[i].Ts, tc.audit[i].Session = "2026-11-22T10:00:00Z", "ro"
			}
			if len(tc.audit) > 0 {
				writeRepoTreeSession(t, root, "2026-11-22", "ro", tc.audit)
			}

			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), bytes.NewReader(start), ss, &out, &stderr))
			roster, err = ss.Load("ro")
			require.NoError(t, err)
			require.Nil(t, roster.FindParticipant("sub"))
			if tc.runs {
				require.FileExists(t, marker)
				require.Contains(t, out.String(), `"decision":"block"`)
				return
			}
			require.NoFileExists(t, marker)
			require.Empty(t, out.String())
			require.Empty(t, stderr.String())
		})
	}
}

func auditWrite(agent, file string) auditEntry {
	return auditEntry{Tool: "Write", AgentID: agent, ToolInput: map[string]any{"file_path": "<repo>/" + file}, PathFormat: auditPathFormat}
}

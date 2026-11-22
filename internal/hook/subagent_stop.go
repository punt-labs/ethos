package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// HandleSubagentStop reads the SubagentStop hook payload from stdin
// and removes the subagent from the session roster.
func HandleSubagentStop(r io.Reader, ss *session.Store) error {
	return HandleSubagentStopWithIO(context.Background(), r, ss, os.Stdout, os.Stderr)
}

// HandleSubagentStopWithIO removes the roster participant before running handoff checks.
func HandleSubagentStopWithIO(ctx context.Context, r io.Reader, ss *session.Store, out, errOut io.Writer) error {
	input, err := ReadInput(r, hookInputTimeout)
	if err != nil {
		stopFeedback(out, errOut, StageFeedback{Blocking: "subagent-stop: " + err.Error()}, false)
		return nil
	}

	agentID, _ := input["agent_id"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	if leaveErr := ss.Leave(sessionID, agentID); leaveErr != nil {
		fmt.Fprintf(errOut, "ethos: failed to leave session %s: %v\n", sessionID, leaveErr)
	}
	if postEditKilled() {
		return nil
	}
	active, _ := input["stop_hook_active"].(bool)
	checkout, err := postEditCheckout()
	if err != nil {
		stopFeedback(out, errOut, StageFeedback{Blocking: "handoff scope: " + err.Error()}, active)
		return nil
	}
	agent, _ := input["agent_type"].(string)
	if handle, _ := input["agent_handle"].(string); handle != "" {
		agent = handle
	}
	feedback := runStageChecks(ctx, checkout, agent, "handoff", func() ([]string, error) {
		return handoffFiles(checkout, sessionID, agentID)
	})
	stopFeedback(out, errOut, feedback, active)
	return nil
}

func stopFeedback(out, errOut io.Writer, feedback StageFeedback, active bool) {
	if feedback.Advisory != "" {
		fmt.Fprintln(errOut, editRedact(feedback.Advisory))
	}
	if feedback.Blocking == "" {
		return
	}
	if active {
		fmt.Fprintln(errOut, editRedact(feedback.Blocking))
		return
	}
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": editRedact(feedback.Blocking)})
}

func handoffFiles(checkout, sessionID, agentID string) ([]string, error) {
	store, err := resolve.StoreOf(checkout)
	if err != nil {
		return nil, err
	}
	cfg, err := resolve.ConfigOf(store)
	if err != nil {
		return nil, err
	}
	policy, err := (resolve.Located{Store: store, Checkout: checkout, Config: cfg}).Layout()
	if err != nil {
		return nil, err
	}
	entries, err := readSessionAuditWithPolicy(policy, sessionID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, entry := range entries {
		if entry.AgentID != agentID || entry.Tool != "Write" && entry.Tool != "Edit" {
			continue
		}
		file, _ := entry.ToolInput["file_path"].(string)
		if file == "" || file == redactedPath || entry.RawPlaceholder || entry.PathFormat < auditPathFormat && mission.HasHomePlaceholder(file) {
			continue
		}
		file, err = mission.ExpandPath(file, checkout)
		if err != nil {
			return nil, err
		}
		root, physical, err := postEditScope(checkout, file)
		if err != nil {
			return nil, err
		}
		if root != checkout {
			continue
		}
		rel, err := filepath.Rel(checkout, physical)
		if err != nil {
			return nil, err
		}
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	if len(files) == 0 {
		return stageFiles(checkout, "handoff")
	}
	sort.Strings(files)
	return files, nil
}

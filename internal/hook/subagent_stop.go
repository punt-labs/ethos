package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
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
	if Killed() {
		return nil
	}
	input, err := readHookInput(ctx, r)
	if err != nil {
		stopFeedback(out, errOut, StageFeedback{Blocking: "subagent-stop: " + err.Error()}, false)
		return nil
	}

	agentID, _ := input["agent_id"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	start := startedParticipant(ss, sessionID, agentID)
	agent := stopAgent(input)
	if agent == "" {
		diagnostic.SetHookIdentity(ctx, "", "", start.Persona)
	}
	if leaveErr := ss.Leave(sessionID, agentID); leaveErr != nil {
		fmt.Fprintf(errOut, "ethos: failed to leave session %s: %v\n", sessionID, leaveErr)
	}
	active, _ := input["stop_hook_active"].(bool)
	checkout, err := hookCheckout(ctx)
	if err != nil {
		stopFeedback(out, errOut, StageFeedback{Blocking: "handoff scope: " + err.Error()}, active)
		return nil
	}
	if readOnlyStop(ctx, checkout, agent, sessionID, start) {
		return nil
	}
	var notes []string
	feedback := runStageRootChecks(ctx, checkout, agent, "handoff", func() (stageScope, error) {
		files, err := handoffFilesWithAdvice(checkout, sessionID, agentID, func(note string) { notes = append(notes, note) })
		if err == nil && len(notes) != 0 {
			if _, ok := files[checkout]; !ok {
				files[checkout] = nil
			}
		}
		return stageScope{filesByRoot: files, auditGap: len(notes) != 0}, err
	})
	if len(notes) != 0 && handoffPolicySelected(checkout, agent) {
		feedback.Notice = strings.Join(notes, "\n")
	}
	stopFeedback(out, errOut, feedback, active)
	return nil
}

func stopFeedback(out, errOut io.Writer, feedback StageFeedback, active bool) {
	response := map[string]string{}
	if feedback.Notice != "" {
		response["systemMessage"] = editRedact(feedback.Notice)
	}
	if feedback.Advisory != "" {
		fmt.Fprintln(errOut, editRedact(feedback.Advisory))
	}
	if feedback.Blocking != "" {
		if active {
			fmt.Fprintln(errOut, editRedact(feedback.Blocking))
		} else {
			response["decision"] = "block"
			response["reason"] = editRedact(feedback.Blocking)
		}
	}
	if len(response) > 0 {
		_ = json.NewEncoder(out).Encode(response)
	}
}

func handoffFiles(checkout, sessionID, agentID string) (map[string][]string, error) {
	return handoffFilesWithAdvice(checkout, sessionID, agentID, nil)
}

func handoffFilesWithAdvice(checkout, sessionID, agentID string, advice func(string)) (map[string][]string, error) {
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
	stream := audit.Stream{Namespace: audit.SessionNS, Session: sessionID}
	data, err := audit.ReadStreamWithSalvage(policy, stream, LiveLineGuard(policy, stream.Namespace))
	if err != nil {
		return nil, err
	}
	entries, err := decodeSessionStream(data)
	if err != nil {
		return nil, err
	}
	if advice != nil {
		for _, gap := range data.Gaps {
			advice(fmt.Sprintf("ethos: handoff audit gap in %s (%s through %s)", gap.Chunk, audit.FormatLineTS(gap.First), audit.FormatLineTS(gap.Last)))
		}
	}
	type auditPath struct {
		file string
		own  bool
	}
	seenAuditPaths := map[auditPath]bool{}
	seen := map[string]bool{}
	files := map[string][]string{}
	otherWriters := map[string]bool{}
	otherPaths := map[string]bool{}
	vanished := map[string][]string{}
	fallbackUncertain := false
	for _, entry := range entries {
		if entry.Tool != "Write" && entry.Tool != "Edit" {
			continue
		}
		file, _ := entry.ToolInput["file_path"].(string)
		if file == "" || file == redactedPath || entry.RawPlaceholder || entry.PathFormat < auditPathFormat && mission.HasHomePlaceholder(file) {
			fallbackUncertain = true
			continue
		}
		own := entry.AgentID == agentID
		key := auditPath{file: file, own: own}
		if seenAuditPaths[key] {
			continue
		}
		seenAuditPaths[key] = true
		file, err = mission.ExpandPath(file, checkout)
		if err != nil {
			fallbackUncertain = true
			continue
		}
		scope := postEditScope
		if !own {
			scope = postEditRoot
		}
		root, physical, err := scope(checkout, file)
		if err != nil {
			fallbackUncertain = true
			continue
		}
		if root == "" {
			continue
		}
		if !own {
			otherWriters[root] = true
			otherPaths[physical] = true
			continue
		}
		if _, ok := files[root]; !ok {
			files[root] = nil
		}
		rel, err := filepath.Rel(root, physical)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(physical); os.IsNotExist(err) {
			vanished[root] = append(vanished[root], filepath.ToSlash(rel))
			continue
		} else if err != nil {
			return nil, err
		}
		if !seen[physical] {
			seen[physical] = true
			files[root] = append(files[root], rel)
		}
	}
	noFallback := map[string]bool{}
	for root, oldPaths := range vanished {
		renamed, err := handoffRenames(root)
		if err != nil {
			if advice != nil {
				advice("ethos: handoff rename lookup: " + err.Error())
			}
			if renamed == nil {
				noFallback[root] = true
				continue
			}
		}
		for _, old := range oldPaths {
			target, known := renamed[old]
			if known {
				noFallback[root] = true
			}
			if target == "" {
				continue
			}
			accepted, physical, err := postEditScope(root, filepath.Join(root, target))
			if err != nil || accepted != root || seen[physical] || otherPaths[physical] {
				continue
			}
			info, err := os.Stat(physical)
			if err != nil || info.IsDir() {
				continue
			}
			rel, err := filepath.Rel(root, physical)
			if err != nil {
				continue
			}
			seen[physical] = true
			files[root] = append(files[root], rel)
		}
	}
	if len(files) == 0 {
		files[checkout] = nil
	}
	for root, paths := range files {
		if len(paths) == 0 && !noFallback[root] && root != store && !otherWriters[root] && !fallbackUncertain {
			paths, err = stageFiles(root, "handoff")
			if err != nil {
				return nil, err
			}
		}
		sort.Strings(paths)
		files[root] = paths
	}
	return files, nil
}

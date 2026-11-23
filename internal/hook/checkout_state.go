package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// mutatingTools are the file-writing tools whose audited use by an agent
// rules out a read-only handoff, even when the checkout looks unchanged.
var mutatingTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// stopAgent is the agent name that selects check policy for a subagent.
func stopAgent(input map[string]any) string {
	agent, _ := input["agent_type"].(string)
	if handle, _ := input["agent_handle"].(string); handle != "" {
		agent = handle
	}
	return agent
}

// subagentStartState fingerprints the session checkout and handoff policy
// for a starting subagent whose policy has handoff checks. Any failure
// returns "", which makes the matching stop run its handoff checks.
func subagentStartState(input map[string]any) string {
	checkout, err := hookCheckout()
	if err != nil {
		return ""
	}
	state, err := handoffState(checkout, stopAgent(input))
	if err != nil {
		return ""
	}
	return state
}

// handoffState hashes the agent's resolved handoff checks with the checkout
// state, so a stop skips only if neither changed. With no handoff checks it
// returns "": there is nothing to skip.
func handoffState(checkout, agent string) (string, error) {
	checks, _, err := configuredStageChecks(checkout, agent, "handoff")
	if err != nil || len(checks) == 0 {
		return "", err
	}
	policy, err := json.Marshal(checks)
	if err != nil {
		return "", err
	}
	state, err := checkoutState(checkout)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append(policy, 0), state...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// startedParticipant returns the stopping agent's roster entry, or an
// empty one when it cannot be read.
func startedParticipant(ss *session.Store, sessionID, agentID string) session.Participant {
	roster, err := ss.Load(sessionID)
	if err != nil {
		return session.Participant{}
	}
	if p := roster.FindParticipant(agentID); p != nil {
		return *p
	}
	return session.Participant{}
}

// readOnlyStop reports whether a stopping subagent provably changed nothing:
// it has a start fingerprint, no audited Write/Edit call is its own or
// unattributed since it joined, and neither the checkout nor its handoff
// policy changed. Bash edits change the fingerprint.
func readOnlyStop(ctx context.Context, checkout, agent, sessionID string, start session.Participant) bool {
	if start.CheckoutState == "" {
		return false
	}
	logger := diagnostic.FromContext(ctx)
	joined, err := time.Parse(time.RFC3339Nano, start.Joined)
	if err != nil {
		logger.Info("handoff read-only check unavailable", "reason", "join time", "error_type", diagnostic.ErrorType(err))
		return false
	}
	entries, err := sessionAuditEntries(checkout, sessionID)
	if err != nil {
		logger.Info("handoff read-only check unavailable", "reason", "audit", "error_type", diagnostic.ErrorType(err))
		return false
	}
	for _, entry := range entries {
		if !mutatingTools[entry.Tool] {
			continue
		}
		if entry.AgentID == start.AgentID {
			return false
		}
		if entry.AgentID != "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, entry.Ts)
		if err != nil || !ts.Before(joined) {
			return false
		}
	}
	current, err := handoffState(checkout, agent)
	if err != nil {
		logger.Info("handoff read-only check unavailable", "reason", "fingerprint", "error_type", diagnostic.ErrorType(err))
		return false
	}
	return current == start.CheckoutState
}

func sessionAuditEntries(checkout, sessionID string) ([]auditEntry, error) {
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
	return readSessionAuditWithPolicy(policy, sessionID)
}

// checkoutState hashes HEAD, the index entries that differ from HEAD and
// the content of every changed or untracked, non-ignored worktree path.
// Ethos's own machine-local session and audit files are excluded: hooks
// write them on every tool call, read-only ones included.
func checkoutState(checkout string) (string, error) {
	out, err := gitenv.Output(checkout, "git", "--no-optional-locks", "status", "--porcelain=v2", "-z", "--branch", "--no-ahead-behind", "--untracked-files=all", "--ignore-submodules=none",
		"--", ":(top)", ":(top,exclude).punt-labs/local", ":(top,exclude).punt-labs/ethos/sessions")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", checkout)
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" || strings.HasPrefix(record, "# ") && !strings.HasPrefix(record, "# branch.oid ") {
			continue
		}
		fmt.Fprintf(h, "%s\x00", record)
		var path string
		switch record[0] {
		case '#':
			continue
		case '1':
			path = statusPath(record, 9)
		case '2':
			path = statusPath(record, 10)
			i++ // the original path of a rename or copy follows
			if i == len(records) || records[i] == "" {
				return "", fmt.Errorf("git status: rename without source %q", record)
			}
			fmt.Fprintf(h, "%s\x00", records[i])
		case 'u':
			path = statusPath(record, 11)
		case '?':
			path = statusPath(record, 2)
		default:
			return "", fmt.Errorf("git status: unexpected record %q", record)
		}
		if path == "" {
			return "", fmt.Errorf("git status: malformed record %q", record)
		}
		if err := hashWorktreePath(h, filepath.Join(checkout, filepath.FromSlash(path))); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func statusPath(record string, fields int) string {
	parts := strings.SplitN(record, " ", fields)
	if len(parts) != fields {
		return ""
	}
	return parts[fields-1]
}

func hashWorktreePath(h io.Writer, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		_, err = io.WriteString(h, "absent\x00")
		return err
	}
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "link %s\x00", target)
		return nil
	case info.Mode().IsRegular():
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "file %o %d\x00", info.Mode().Perm(), info.Size())
		_, err = io.Copy(h, f)
		return errors.Join(err, f.Close())
	case info.IsDir():
		// A nested repository or submodule: its own state covers edits
		// that leave the parent's status record unchanged.
		if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
			fmt.Fprintf(h, "dir\x00")
			return nil
		}
		state, err := checkoutState(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "repo %s\x00", state)
		return nil
	default:
		fmt.Fprintf(h, "other %s\x00", info.Mode().Type())
		return nil
	}
}

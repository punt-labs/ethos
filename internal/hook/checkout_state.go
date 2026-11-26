package hook

import (
	"bytes"
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
	checkout, err := hookCheckout(context.Background())
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
	var inputs []string
	for _, check := range checks {
		if check.Snapshot != nil {
			inputs = append(inputs, check.Snapshot.Inputs...)
		}
	}
	key := append(append(policy, 0), state...)
	if len(inputs) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), gateInputTime)
		defer cancel()
		proof, err := digestGateInputs(ctx, checkout, "", inputs)
		if err != nil {
			return "", err
		}
		key = append(append(key, 0), proof...)
	}
	sum := sha256.Sum256(key)
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
// the content of every changed, untracked or status-hidden, non-ignored
// worktree path.
// Ethos's own machine-local session and audit files are excluded: hooks
// write them on every tool call, read-only ones included.
func checkoutState(checkout string) (string, error) {
	return checkoutStateBudgeted(checkout, nil)
}

type gateHashBudget struct {
	ctx       context.Context
	dst       io.Writer
	remaining int64
}

func (b *gateHashBudget) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		b.remaining = -1
		return 0, errors.New("handoff fingerprint exceeds byte budget")
	}
	n, err := b.dst.Write(p)
	b.remaining -= int64(n)
	return n, err
}

func checkoutStateWithBudget(ctx context.Context, checkout string) (string, error) {
	return checkoutStateBudgeted(checkout, &gateHashBudget{ctx: ctx, remaining: gateBytes})
}

func fingerprintGit(checkout string, budget *gateHashBudget, args ...string) ([]byte, error) {
	if budget == nil {
		return gitenv.Output(checkout, "git", args...)
	}
	deadline, ok := budget.ctx.Deadline()
	if !ok || budget.ctx.Err() != nil || !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	var out bytes.Buffer
	bounded := &gateHashBudget{ctx: budget.ctx, dst: &out, remaining: min(budget.remaining, 8<<20)}
	_, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: checkout, Args: args, Env: gitenv.Parsed(), Stdout: bounded, Limit: time.Until(deadline)})
	budget.remaining -= int64(out.Len())
	if bounded.remaining < 0 {
		return nil, errors.New("git fingerprint output exceeds byte budget")
	}
	return out.Bytes(), err
}

func checkoutStateBudgeted(checkout string, budget *gateHashBudget) (string, error) {
	out, err := fingerprintGit(checkout, budget, "--no-optional-locks", "status", "--porcelain=v2", "-z", "--branch", "--no-ahead-behind", "--untracked-files=all", "--ignore-submodules=none",
		"--", ":(top)", ":(top,exclude).punt-labs/local", ":(top,exclude).punt-labs/ethos/sessions")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	h := sha256.New()
	var writer io.Writer = h
	if budget != nil {
		old := budget.dst
		budget.dst = h
		defer func() { budget.dst = old }()
		writer = budget
	}
	fmt.Fprintf(writer, "%s\x00", checkout)
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" || strings.HasPrefix(record, "# ") && !strings.HasPrefix(record, "# branch.oid ") {
			continue
		}
		fmt.Fprintf(writer, "%s\x00", record)
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
			fmt.Fprintf(writer, "%s\x00", records[i])
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
		if err := hashWorktreePath(writer, filepath.Join(checkout, filepath.FromSlash(path)), budget); err != nil {
			return "", err
		}
	}
	if err := hashHiddenPaths(writer, checkout, budget); err != nil {
		return "", err
	}
	if budget != nil && (budget.remaining < 0 || budget.ctx.Err() != nil) {
		return "", errors.New("handoff fingerprint exceeded its budget")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// hashHiddenPaths hashes tracked files marked assume-unchanged or
// skip-worktree, whose edits git status does not report.
func hashHiddenPaths(h io.Writer, checkout string, budget *gateHashBudget) error {
	out, err := fingerprintGit(checkout, budget, "--no-optional-locks", "ls-files", "-v", "-z",
		"--", ":(top)", ":(top,exclude).punt-labs/local", ":(top,exclude).punt-labs/ethos/sessions")
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for _, record := range strings.Split(string(out), "\x00") {
		if len(record) < 3 {
			continue
		}
		tag := record[0]
		if tag != 'S' && (tag < 'a' || tag > 'z') {
			continue
		}
		path := record[2:]
		fmt.Fprintf(h, "hidden %c %s\x00", tag, path)
		if err := hashWorktreePath(h, filepath.Join(checkout, filepath.FromSlash(path)), budget); err != nil {
			return err
		}
	}
	return nil
}

func statusPath(record string, fields int) string {
	parts := strings.SplitN(record, " ", fields)
	if len(parts) != fields {
		return ""
	}
	return parts[fields-1]
}

func hashWorktreePath(h io.Writer, path string, budget *gateHashBudget) error {
	if budget != nil && budget.ctx.Err() != nil {
		return budget.ctx.Err()
	}
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
		state, err := checkoutStateBudgeted(path, budget)
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

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:   "commit-msg <message-file>",
		Short: "git commit-msg policy: add Mission/Delegation trailers",
		Long: `Run the trailer policy the installed commit-msg shim delegates to.

MISSION_ID and DELEGATION_ID in the environment win. Otherwise the committing
session's active mission and delegation binding are used. With neither, the
message is left unchanged.

Exit codes:
  0  trailers added, or none to add
  1  the trailers could not be rendered or installed
  2  usage error`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHookCommitMsg(cmd.ErrOrStderr(), args[0])
		},
	})
}

// runHookCommitMsg adds the trailers for the committing session to the message
// file. A session that cannot be resolved or read adds no trailer and does not
// block the commit; a trailer that cannot be installed does.
func runHookCommitMsg(errOut io.Writer, msgFile string) error {
	missionID, delegationID := os.Getenv("MISSION_ID"), os.Getenv("DELEGATION_ID")
	if missionID == "" && delegationID == "" {
		var err error
		missionID, delegationID, err = sessionTrailerIDs()
		if err != nil {
			fmt.Fprintf(errOut, "ethos: commit-trailers: %v; no trailer added\n", err)
			return nil
		}
	}
	if missionID == "" && delegationID == "" {
		return nil
	}
	p, err := currentRoots().Located.Layout()
	if err != nil {
		return fmt.Errorf("commit-message: locating repository layout: %w", err)
	}
	if err := hook.ApplyCommitMessage(p, msgFile, missionID, delegationID); err != nil {
		return fmt.Errorf("commit-message: %w", err)
	}
	return nil
}

// sessionTrailerIDs returns the mission and delegation IDs bound to the
// committing session. Outside Claude Code no session is expected, so it
// returns none and no error.
func sessionTrailerIDs() (missionID, delegationID string, err error) {
	sessionID, _, err := resolve.SessionID(sessionStore())
	if err != nil {
		if errors.Is(err, resolve.ErrNotUnderClaudeCode) {
			return "", "", nil
		}
		return "", "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := hook.WriteCommitTrailers(&buf, home+"/.punt-labs/ethos", sessionID); err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "MISSION_ID="); ok {
			missionID = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "DELEGATION_ID="); ok {
			delegationID = strings.TrimSpace(v)
		}
	}
	return missionID, delegationID, nil
}

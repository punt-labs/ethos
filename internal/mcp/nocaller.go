package mcp

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/v4/internal/doctor"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// This file is the only place the MCP server answers "who is calling?" without
// a declared caller. Claude Code gives an MCP server no session or agent
// identity (DESIGN.md DES-074, amendment 2026-11-03), so under Claude Code the
// question has one answer: the ethos PreToolUse hook declared the caller in
// _caller, or the call is refused. Any other source (a process-tree walk, the
// environment, a pointer file) can name another session's caller with no error,
// which is the defect DES-074 exists to close. For a client that is not Claude
// Code the harness-neutral chain still applies. TestNoCallerResolutionLivesInOneFile
// keeps the calls it makes from appearing anywhere else in this package.

// errNoCaller is returned to a Claude Code call that declared no caller. It is
// a type, not errors.New, so the message can open with the proper noun the
// operator searches for.
var errNoCaller error = noCallerError{}

type noCallerError struct{}

func (noCallerError) Error() string {
	return "Claude Code call arrived without _caller: the ethos PreToolUse hook did not declare the caller (is it installed, enabled, and not killed?)"
}

// refuseUndeclared is errNoCaller under Claude Code, and nil for any other
// client.
func refuseUndeclared() error {
	if resolve.UnderClaudeCode() {
		return errNoCaller
	}
	return nil
}

// noCallerSessionID is the session of a call that declared no caller, for the
// operations that act on the caller's own session. The error is errNoCaller
// under Claude Code, and otherwise resolve.SessionID's, unchanged.
func (h *Handler) noCallerSessionID() (string, error) {
	if err := refuseUndeclared(); err != nil {
		return "", err
	}
	sid, _, err := resolve.SessionID(h.sessionStore)
	return sid, err
}

// noCallerHandle is the persona handle of a call that declared no caller:
// errNoCaller under Claude Code, otherwise resolve.Resolve's chain.
func (h *Handler) noCallerHandle(s identity.IdentityStore, checkout string) (string, error) {
	if err := refuseUndeclared(); err != nil {
		return "", err
	}
	return resolve.Resolve(s, h.sessionStore, checkout)
}

// noCallerIam declares persona for a call that declared no caller, keyed on
// ETHOS_AGENT_ID then the Claude PID, as the CLI does (DES-061 R4). It returns
// the agent key it recorded.
func (h *Handler) noCallerIam(sessionID, persona string) (string, error) {
	if err := refuseUndeclared(); err != nil {
		return "", err
	}
	agentID := os.Getenv("ETHOS_AGENT_ID")
	selfKeyed := agentID == ""
	if selfKeyed {
		agentID = process.FindClaudePID()
	}
	p := session.Participant{AgentID: agentID, Persona: persona}
	var err error
	if selfKeyed {
		// Tolerate a session that started before DES-074's CLAUDE_PID key and
		// keyed its primary participant on the walk-derived PID: a plain Join
		// would file a duplicate for the same process. An explicit
		// ETHOS_AGENT_ID is exact by the caller's own declaration and is never
		// subject to this fallback.
		err = h.sessionStore.JoinSelf(sessionID, agentID, process.LegacyClaudePID(), p)
	} else {
		err = h.sessionStore.Join(sessionID, p)
	}
	if err != nil {
		return "", fmt.Errorf("failed to set persona: %w", err)
	}
	return agentID, nil
}

// noCallerDoctor is doctor's report for a call that declared no caller. Under
// Claude Code the "Human identity" row says the caller is unknown, in place of
// the row doctor derives from the environment.
func (h *Handler) noCallerDoctor(loc resolve.Located) []doctor.Result {
	if err := refuseUndeclared(); err != nil {
		return doctor.RunAllForCaller(loc, h.store, h.sessionStore, h.teams,
			doctor.Result{Name: doctor.HumanIdentityRow, Status: "FAIL", Detail: err.Error()})
	}
	return doctor.RunAllFor(loc, h.store, h.sessionStore, h.teams)
}

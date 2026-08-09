// Package hooks embeds the git-hook scripts ethos chains into a repo, so a
// single authoritative copy — the shellcheck-linted scripts in this
// directory — is the one both the shell test suite and the Go chainer use.
// The embed lives here beside the scripts because an embed directive cannot
// reach files above its own package directory (no "..").
package hooks

import _ "embed"

// PreCommit is the DES-058 audit-seal pre-commit hook, gated on the §2.7
// enabled marker.
//
//go:embed pre-commit.sh
var PreCommit []byte

// CommitMsg is the DES-054 Mission/Delegation trailer commit-msg hook, gated
// on the §2.7 enabled marker.
//
//go:embed commit-msg.sh
var CommitMsg []byte

// Hook tags and idents: the marker tag internal/githook.Chain fences each
// script's section with, and the fingerprint it carries on the line after
// BEGIN. internal/enable uses these to chain and unchain the hooks;
// internal/doctor uses the same four to find and identify a hook's on-disk
// section for the presence, active, and currency checks. One copy here
// avoids the two-copies-drift pattern this codebase already burned itself on
// once (ethos-2ol1, docs/enable-disable.md "Why three packages, not one").
const (
	SealTag      = "ETHOS DES-058 SEAL"
	SealIdent    = "hooks/pre-commit.sh — Seal pending live audit lines"
	TrailerTag   = "ETHOS DES-054 TRAILER"
	TrailerIdent = "hooks/commit-msg.sh — Append Mission:/Delegation:"
)

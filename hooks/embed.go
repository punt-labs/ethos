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

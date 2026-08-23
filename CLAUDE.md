# Ethos (working on this repo)

@docs/development.md

Ethos, vox, and z-spec are enabled here with deposited guides and are
`@`-imported below (dogfooding). Biff is also enabled but ships no
repo-scoped guide today (§ 2.6 lets a global tool register user-scope
only) — there's nothing to import, so its biconditional is vacuously
satisfied; the gap is on biff's side, tracked as a follow-up on the
biff repo. Per the org's
[tool enable/disable standard](https://github.com/punt-labs/punt-kit/blob/main/standards/tool-enable-disable.md)
§ 2.11 (enabled ⟺ import line), every enabled tool with a deposited
guide is imported below — the guides are the same content consumers
receive, so dogfooding means loading them. The upstream ADR (DES-071) that optimized this repo's per-session
payload by *skipping* these imports was superseded by that standard;
the guides ride the wire per session, and any bloat is a signal to
tighten the guides themselves, not to skip them.

For one-time setup guidance (install, `ethos enable`, `ethos setup`,
troubleshooting), see [`docs/ETHOS-SETUP.md`](docs/ETHOS-SETUP.md) —
not `@`-imported (setup is a one-time task, not per-session context).

@.punt-labs/ethos/CLAUDE.md
@.punt-labs/vox/CLAUDE.md
@.punt-labs/z-spec/CLAUDE.md

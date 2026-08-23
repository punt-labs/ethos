# Ethos (working on this repo)

@docs/development.md

Ethos and vox are enabled in this repo (dogfooding). Per the org's
[tool enable/disable standard](https://github.com/punt-labs/punt-kit/blob/main/standards/tool-enable-disable.md)
§ 2.11 biconditional (enabled ⟺ import line), the two `@`-imports below
must be present here — the tool user guides they load are the same ones
consumers of these tools receive, so dogfooding means loading them
too. The upstream ADR (DES-071) that optimized this repo's per-session
payload by *skipping* these imports was superseded by that standard;
the guides ride the wire per session, and any bloat is a signal to
tighten the guides themselves, not to skip them.

For one-time setup guidance (install, `ethos enable`, `ethos setup`,
troubleshooting), see [`docs/ETHOS-SETUP.md`](docs/ETHOS-SETUP.md) —
not `@`-imported (setup is a one-time task, not per-session context).

@.punt-labs/ethos/CLAUDE.md
@.punt-labs/vox/CLAUDE.md

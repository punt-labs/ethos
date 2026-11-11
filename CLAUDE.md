# Ethos development compatibility

Shared project instructions are in `AGENTS.md`. OpenCode V2 loads that file and
the native workflow configured in `opencode.json`; it does not interpret this
file's imports. Development details remain in `docs/development.md`.

@AGENTS.md
@docs/development.md

The deposited guides below remain imported for enabled Claude-compatible tools,
as required by the tool-enable-disable standard. Read `docs/ETHOS-SETUP.md` only
for one-time product setup and `docs/opencode-setup.md` for the native harness.

@.punt-labs/ethos/CLAUDE.md
@.punt-labs/vox/CLAUDE.md
@.punt-labs/z-spec/CLAUDE.md

## Demo gate

- Native OpenCode changes require `make doctor-opencode` plus the affected real
  smoke stages from `make smoke-opencode`. Record the exact tested commit,
  commands, expected/actual outcomes, and receipt paths under `.tmp/opencode/`.
- Product changes require actual scenarios through the built or installed CLI,
  MCP, or hook entry point per `docs/development.md`, with the same evidence.
- Behavior requiring subjective user judgment needs operator confirmation of
  observed behavior; internal machine-verifiable changes need no human gate.
- Only prose-only documentation edits that change no executable instructions or
  configuration are `demo_exempt` and may retain prior demo evidence. Changes to
  `plugin/` or `.opencode/` prompts/configuration are behavior even in Markdown.
- After a passing demo, the leader posts `demo confirmed: <exact commit>` with
  receipts and any required operator confirmation, newer than any `demo failed:`
  comment. That commit must be the head or its ancestor, with only `demo_exempt`
  changes since it. A failed demo remains a finding until fixed and verified.
- Tests or documentation alone never establish that a demo actually ran.
@.punt-labs/punt/CLAUDE.md

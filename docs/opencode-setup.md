# OpenCode setup

This repository targets native OpenCode 2.2.5. The root `opencode.json` loads
`docs/opencode-workflow.md` through the active `instructions` array. Config,
agents, commands and skills hot-reload; `/reload` forces a reload. Reconnect an
MCP after upgrading its executable so the connection runs the new image.
The `/punt:auto` command is defined in JSON rather than a colon-bearing filename
so Git checkouts remain portable; the other commands use native Markdown files.

## Requirements

Use the already authenticated `bifrost` provider with its
`@opencode/ai/providers/openai-compatible/responses` package, endpoint and key
configured outside this repository. Project provider overrides contain model
configuration only. A Bifrost virtual key must route through Bifrost; a direct
`openai/` selector uses a different provider.

Root model selection loses its variant, so project Astra settings set
`reasoningEffort: high`; build, commands and specialists additionally pin variants.
An existing session's selection wins: choose `bifrost/openai/gpt-6-astra#high`
for the leader. Review, formal and product roles use the same Astra/high selector;
implementation and QA use `bifrost/openai/gpt-6.1-sol#high`; bounded research uses
the available `bifrost/openai/gpt-5.6-terra#medium`. The project adds Sol's catalog
entry and high variant. Its 200,000-context/32,000-output limits are conservative
project budgets, not claimed gateway maxima. Astra and Terra retain their existing
global catalog entries. A catalog entry still needs a real model turn to prove
routing and authorization.

Install prerequisites separately, with operator authorization: Python 3.10+,
Git, Go (`gofmt`), `gopls`, OpenCode, direnv, Vox, `beadle-email`, Quarry,
`z-spec`, and the maintained sibling `prfaq`, `z-spec`, `punt-kit` checkouts.
Qodo's installed skills require their declared CLI minimum (currently 2.x) and
the intended authenticated workspace. `/review` loads `ethos-qodo-review`:
CLI >=2 delegates to the installed skill unchanged. The operator authorized
restored, official checksum-verified CLI 1.6.0 legacy review after 2.1 A2A failures
for both the ethos-t1uy bootstrap and the approved DES-079 storage-boundary repair,
including after bootstrap closes, until a separately verified, user-directed
upgrade. That route uses help-verified `summary`/`decisions` context,
the pushed target's actual merge-base SHA and `qodo review --base SHA
--context-file FILE`; it does not claim the 2.x skill passed. Final coverage
includes the full intended diff (including new helpers); a path-limited
diagnostic is not final coverage. Unknown old versions block. A user-directed
2.x reinstall returns to the installed skill's gates. Neither route silently
upgrades, edits global skills, reconstructs credentials or requires re-login
when existing authentication works. This bootstrap installs nothing globally.

The resolver `python3 scripts/opencode.py sources` locates maintained procedures
from the canonical Git checkout's workspace, including from linked worktrees.
PR/FAQ may fall back to an installed skill. Release must use the canonical
workspace's `punt-kit/plugin/skills/auto/SKILL.md` and
`punt-kit/plugin/playbooks/release.yaml`, executing against the current target
repository, never against punt-kit. Missing paths are reported explicitly.

## Six selected MCP connections

| Server | Transport | Prerequisite / fallback |
| --- | --- | --- |
| vox | `direnv exec . vox mcp` | Approved repo environment; `vox --help` |
| beadle | `direnv exec . beadle-email serve` | Configured mail account; `beadle-email --help` |
| quarry | `direnv exec . quarry mcp` | Local index/store; `quarry --help` |
| zspec | `direnv exec . z-spec mcp` | `z-spec doctor` discovers fuzz/ProB; `z-spec --help` |
| lux | `http://127.0.0.1:8430/mcp` | Running Lux service; `lux --help` |
| context7 | `https://mcp.context7.com/mcp` | Service environment has `CONTEXT7_API_KEY`; native docs web fetch fallback |

Context7 uses `Bearer {env:CONTEXT7_API_KEY}`, matching the working Points
reference without copying a key. A shared OpenCode service must inherit that
environment; local `direnv exec` alone does not provide remote-server headers.
Do not display its resolved header. Approve direnv outside agent automation if
needed; never rewrite `.envrc` or `.envrc.local` to pass a check. Keep tool state
under `.punt-labs/local/ethos/` and scratch/evidence under `.tmp/opencode/`.

Startup/catalog budgets are 120/60 seconds; MCP execution allows an hour for
formal operations. Ethos MCP, Biff and Brave are deliberately not required.
Native MCP connectivity is not the unimplemented DES-078 adapter.

## Verification

```bash
make check-opencode     # Offline, deterministic; also part of make check
make doctor-opencode    # Live roster/models/config/MCP connection diagnostics
make smoke-opencode     # Paid real parent/child tool turns and health calls
make smoke-opencode-discovery  # Paid discovery only, not a full smoke
make smoke-opencode-health     # Paid root Code Mode health calls only
make smoke-opencode-formatter  # Paid formatter fixture only
```

`make smoke-opencode-children` covers all eleven child reads only. The default
smoke still runs children, discovery, health and formatter. Select multiple
remaining stages without repeating child turns using
`python3 -B scripts/opencode.py smoke --stage discovery --stage health --stage formatter`.
Each invocation retains an independent receipt with `scope` (full/targeted),
requested/completed stages and sessions; a targeted pass certifies only its
listed stages, never a full smoke or unrun model turn. Every selection first
runs the live doctor. No old failed receipt is rewritten into a pass.

The offline gate needs no provider, network, installed siblings, or OpenCode.
It checks native shapes, roster, protected paths, formatter policy, source
uniqueness, adapter/command paths and negative regression fixtures.
It does not assert the leader-owned root documentation relocation is complete.

Doctor inspects live discovery without printing raw config or credentials.
It verifies the full effective permission suffix for every role, including
reviewed exceptions, native read-action restrictions and denial of nested delegation.
After the reviewed policy, only no additional rules or OpenCode 2.2.5's exact
native browser-unavailable pair is accepted, in order: deny `browser` on `*`,
then deny `skill` on `browser`. No other trailing deny, ask or grant is accepted;
even a denial can disable required formal/research capabilities.
These rules are not a shell sandbox: inspected tests still run with host authority.
All six transports must connect; a connection is not a successful health call.
Smoke creates dedicated retained sessions and a scratch fixture, pins the actual
parent Astra/high, exercises every configured specialist and a completed
`go.mod` read, and verifies exact parent/child, agent, model, variant and result.
It also runs read-only PR/FAQ/release discovery, Z-spec availability, harmless
MCP calls through the root's default Code Mode.
Code Mode requires the exact response-returning code, six completed native
nested-call records and each tool's actual returned evidence. Readiness includes
Vox provider/program status, Z-spec's healthy doctor, Lux pong/latency, Quarry
store/index counts and Context7 Go library results. Beadle poll status must not
report failures; this checks the polling capability, not outbound mail delivery.
Missing or malformed evidence fails. Discovery shell tools must report actual
foreground zero exit, not background, timeout or missing exit evidence.
Discovery permits and requires the adapter's harmless `z-spec --help` as well
as doctor. Evidence is matched by target arguments before checking completion;
unrelated shell failures do not substitute for or poison another operation.
Every attempt at a required operation must succeed, including a denied/failed
attempt followed by a successful retry.
Formatter checks require an exact sentinel-only native patch input (the GPT
tool-selection hook exposes `patch`, not `edit`/`write`), preserving deliberately
malformed Go before automatic formatting, and then compare disk bytes against
gofmt and non-Go originals. It never releases, sends mail, or rewrites
source specifications. No existing session or worktree is deleted for cleanup.
The source guard snapshots Git-tracked and non-ignored untracked paths, with
file type, mode, content hash and symlink target. Existing Git exclusions keep
generated coverage/cache churn out of the source set; tracked ignored files
remain protected. Before/after snapshots and changed relative paths are retained
with stage/session IDs and safe exit/timeout diagnostics. This is change
detection, not a lock against concurrent source writers. Failure evidence is
retained, not reported as readiness.
The result extractor accepts any native author ID, not a particular team prefix;
provide `--model provider/model#variant` for an author outside the project roster.
For a configured author, `--model` must equal its pin; it cannot override it.

### Practical role access and adapters

Formal roles `jms` and `jra` can use Code Mode `execute` with `zspec.check`,
`zspec.model_check` and `zspec.doctor`. The first two persist sidecar reports;
inputs must be under assigned `.tmp/opencode/` scratch, never source. Only jms
may edit assigned source specifications; jra uses leader-prepared scratch inputs.
Researcher can use `execute` with Quarry find/show/status and Context7
resolve-library-id/query-docs, plus its narrow recall skill, but not ingestion,
capture or database switching. Native MCP permission IDs preserve hyphens.
QA can run exact canonical `make build`, `make test`, `make check`, `make lint`
and selected `./ethos` read/schema commands for assigned real-entry acceptance;
installation, enablement, mission closure and publication remain ungranted.
These grants are useful host-authority operations, not containment. Inspect
targets, scope fixtures and record actual tool results. The eleven-child smoke
proves read/dispatch, not formal exploration or research retrieval. A meaningful
additional acceptance task should retrieve a cited Quarry result/Context7 page,
or check/model-check a tiny leader-prepared scratch model and inspect its report;
permissions alone do not certify those capabilities worked.

The offline gate expects these nine project adapters:

| Adapter | Maintained or bounded route |
| --- | --- |
| ethos-prfaq | Maintained PR/FAQ procedure |
| ethos-zspec | Maintained modeling/check procedures and live schemas |
| ethos-quarry-recall | Bounded read-only recall |
| ethos-quarry-capture | Explicitly authorized persistent capture |
| ethos-collaboration | Independent tool APIs, not backend relocation |
| ethos-punt-release | Authorized maintained punt-kit release executor |
| ethos-opencode-check | Offline/live bootstrap diagnostics |
| ethos-reference | Consumer CLI/MCP contracts |
| ethos-qodo-review | Installed >=2 skill or authorized 1.6.0 legacy route |

Only Go is automatically formatted. A formatter object enables *all* built-ins
in V2, so every documented non-Go built-in is explicitly disabled, including
LaTeX, Markdown, Python and shell formatters. New upstream built-ins or inherited
custom formatters require inspection; the fixture smoke detects non-Go changes
for representative extensions, not every possible future formatter.

Do not claim live readiness until doctor and smoke have actually passed in the
intended checkout/environment. Full `make check` follows root-guide integration.

## Native references

- [Config](https://opencode.ai/v2/docs/config/)
- [Agents](https://opencode.ai/v2/docs/agents/)
- [Models](https://opencode.ai/v2/docs/models/)
- [Skills](https://opencode.ai/v2/docs/skills/)
- [Commands](https://opencode.ai/v2/docs/commands/)
- [Permissions](https://opencode.ai/v2/docs/permissions/)
- [Formatters](https://opencode.ai/v2/docs/formatters/)

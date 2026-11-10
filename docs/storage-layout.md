# Storage and publication contract

**Status:** The DES-079 layout described here is the shipped layout. Ethos
converts any older layout itself, once per machine and once per repository, at
every entry point (DES-084); the user runs nothing. "Promise to implementation
to acceptance" names the test or acceptance case that proves each row.
`make check` includes `check-storage` (`scripts/storage_acceptance.py`), which
drives the real CLI, hook wrappers, MCP server and Git hooks through the cases
`default_seal`, `modes`, `audit_recovery`, `lifecycle`, `required_event_failure`,
`worktree_retention`, `full_export`, `publish_squash`, `mcp_modes` and `upgrade`.

## Roots and ownership

Let **S** be the canonical store root resolved through Git's common directory
and **C** the invoking or committing checkout. In an ordinary checkout S = C;
in a linked feature worktree they usually differ. Use the established resolver,
not a cwd walk or an independently parsed configuration file.

- **S owns shared authority:** `.punt-labs/ethos/` holds source and blame
  records. A file there is intended for Git, not automatically staged or committed.
- **S owns repository-local state:** every new live log, retained archive, lock,
  receipt, overlay, scratch file and temporary is under
  `S/.punt-labs/local/ethos/`. This includes staging for writes into C and
  symlink-resolved host files. Classification as local is not permission to
  create a temporary inside the record tree, even briefly.
- **C owns checkout effects:** generated agents, enablement deposits, host-file
  edits and publication copies target C; Git operations target C's actual index,
  including an inherited `GIT_INDEX_FILE`. Publication copies are not a second
  mutable mission authority.
- Global rosters, counters and legitimate global stores retain their existing
  `~/.punt-labs/ethos/` layout. DES-081 is not a prerequisite. The concrete
  global paths are listed under "Global paths" below.

**Observed in a linked worktree (v4.22.1).** With the invoking checkout C a
linked worktree, `ethos mission dispatch` wrote `contract.yaml`,
`corrections.yaml` and the live logs into S, the main checkout's working
tree, and nothing into C. The feature branch receives those records only
through `ethos mission publish`, which copies them into C and C's index. A
mission dispatched from a linked worktree therefore leaves S's working tree
dirty, not C's.

Repository staging uses the local zone, file sync, atomic rename/link semantics
and destination-directory sync as applicable. `EXDEV` refuses with the previous
target intact: no adjacent temporary or copy-over fallback. A post-rename sync
failure means applied with unconfirmed crash durability, not rolled back.
Lock relocation requires quiescing old writers; moving a pathname does not move
an already-held lock inode. Preserve lock domains and acquisition order.

## Paths

Paths in the first column are relative to S unless C is explicit.

| Data | Destination | Sharing |
|---|---|---|
| Identity graph and base extensions | `.punt-labs/ethos/{identities,talents,personalities,writing-styles,roles,teams,agents,...}/` | Source, ordinary Git staging |
| Configuration | `.punt-labs/ethos.yaml` | Source; documented exception to the record-tree location |
| Contract, including inline decisions | `.punt-labs/ethos/missions/<id>/contract.yaml` (with the `round_decisions` v1 block, written at dispatch) | Shared workflow authority |
| All accepted results, delegation records, corrections | `.punt-labs/ethos/missions/<id>/{results.yaml,delegations/<d>/record.yaml,corrections.yaml}`. in repository (two-tree) mode `corrections.yaml` is initialized at mission create as an empty file (`version`, `mission`, `entries: []`); `results.yaml` on result; `record.yaml` when the Agent spawn passes pre-tool-use | Blame records |
| Mission summaries; repo ADRs | `.punt-labs/ethos/{missions.jsonl,adrs/<id>.yaml}`; `missions.jsonl` is written on close; `ethos adr create` writes `adrs/<id>.yaml` in the repository store (`S/.punt-labs/ethos/adrs/`) | Blame records; ADRs read from the repository store |
| Immutable publication manifest | `C/.punt-labs/ethos/provenance/<sha256>.yaml` (written by `ethos mission publish`, which also stages it and the published records into C's index) | Committed snapshot inventory and attribution evidence |
| Plain export | `<dir>/<id>.contract.yaml` and `<dir>/<id>.result.yaml`. Default `<dir>` is `C/.punt-labs/ethos/missions` (`cmd/ethos/mission.go`, `missionExportDefaultDir`); `--dir` is cwd-relative | Unstaged |
| Full export additions | `<dir>/<id>/log-<sid>-<t0>-<t1>.jsonl`, alongside retained prompts and reflections, same `<dir>` rule | Unstaged |
| Published reflection aggregate (`record: full`) | `C/.punt-labs/ethos/missions/<id>/reflections.yaml` (`CheckoutRecordRoot`, `internal/mission/reflection_authority.go:569`); under `record: blame`, `ethos mission reflect` writes only the local envelope | Trace publication copy, eligible entries only |
| Published session audit chunk (`record: full`) | `C/.punt-labs/ethos/sessions/<date>-<sid>/audit-<t0>-<t1>.jsonl` (`internal/hook/audit_repository.go:250`); the pre-commit seal adds it to the commit automatically | Trace publication copy, eligible entries only |
| Published delegation prompt (`record: full`) | `C/.punt-labs/ethos/missions/<id>/delegations/<d>/prompt.md` (`internal/mission/prompt_record.go:231`) | Trace publication copy, eligible entries only |
| Published mission event chunk (`record: full`) | `C/.punt-labs/ethos/missions/<id>/log-<sid>-<t0>-<t1>.jsonl` | Trace publication copy, eligible entries only |
| Session audit live file | `.punt-labs/local/ethos/sessions/<sid>.audit.jsonl` | Local in both modes |
| Mission event live file | `.punt-labs/local/ethos/missions/<id>/<sid>.log.jsonl` | Local in both modes |
| Retained prompts and chunks | `.punt-labs/local/ethos/trace/` plus record-relative path | Local in both modes |
| Prompt eligibility receipt | `.punt-labs/local/ethos/trace/missions/<id>/delegations/<d>/prompt-consent.yaml` | Local only; never exported as trace |
| Accepted per-round reflection envelope | `.punt-labs/local/ethos/trace/missions/<id>/reflection-<round>.yaml` | Local in both modes; joined to shared decisions |
| Pending per-round reflection receipt | `.punt-labs/local/ethos/missions/<id>/reflection-intents/<round>.yaml` | Local preparation/recovery detail, not acceptance authority |
| Pending correction receipt | `.punt-labs/local/ethos/missions/<id>/correction-intent.yaml` (`internal/mission/correction_authority.go`) | Local preparation/recovery detail, not acceptance authority |
| Mission event stream with no resolvable session | `.punt-labs/local/ethos/missions/<id>/no-session.log.jsonl` | Local in both modes |
| Sealed mission event chunks | `.punt-labs/local/ethos/trace/missions/<id>/log-<sid>-<t0>-<t1>.jsonl` (sealed at close and at commit) | Local in both modes |
| Sealed session audit chunks | `.punt-labs/local/ethos/trace/sessions/<date>-<sid>/audit-<t0>-<t1>.jsonl` (sealed at commit) | Local in both modes |
| Locks | `.punt-labs/local/ethos/locks/<domain>/<sha256>.lock`. Domains come from the `LockPath` calls that take a lock: `adr`, `audit-stream`, `claudemd`, `commit-message`, `delegation`, `delegation-prompt`, `identity`, `mission-create`, `mission-delegations`, `mission-state`, `mission-summary`. Calls that only validate a domain (`audit-publication`, `repair`, and `"validate"` keys) are excluded | Never published |
| Staging and quarantine | `.punt-labs/local/ethos/tmp/` (`Policy.TempDir`) and `.punt-labs/local/ethos/quarantine/`. Staging files carry the `.ethos-tmp` marker in their names (`internal/layout/temp.go`: `TmpPattern` gives `<base>-*<ext>.ethos-tmp`, `DotTmp` gives `.<name>.ethos-tmp`) | Never published |
| Mission summary live logs | `.punt-labs/local/ethos/missions/*.jsonl` | Never published |
| Repository extension overlay | `.punt-labs/local/ethos/identities/<handle>.ext/<ns>.local.yaml` | Local; base remains in the identity's source layer |
| Other local state not listed above | `.punt-labs/local/ethos/` | Never published |
| Team bundles in the repository | `.punt-labs/ethos-bundles/**` | Source |
| Submodule registration | `.gitmodules` (written by `team add-bundle`) | Source |
| Vendor manifest | `.punt-labs/ethos/.vendor.yaml` | Source |
| Git hook files | hook files under an in-tree `core.hooksPath` | Source |
| Older layouts | record-tree locks (`missions/**/*.lock`, `missions.jsonl.lock`, `identities/*.yaml.lock`), `missions/*/log.jsonl`, `log-*.quarantine`, `*.corrupt*`, `sessions/*/audit.jsonl`, `.punt-labs/ethos/config.yaml`, singular `reflection-intent.yaml` | The automatic storage upgrade converts or deletes them (steps 1-15); nothing reads them afterward. `missions/*/artifacts/**` is agent-owned and never touched |
| New worker artifacts | `.punt-labs/local/ethos/artifacts/missions/<id>/delegations/<d>/`, the value pre-tool-use exports as `MISSION_ARTIFACTS_DIR` (`internal/mission/delegation.go`, `internal/hook/pretooluse_dispatch.go`) | Agent-owned, not automatically exported or redacted |
| Generated agents and enablement guides | C's `.claude/agents/*.md` (generated plus the three seeded review agents), the `CLAUDE.md` `@`-import, the `.gitignore` rule `.punt-labs/local/`, and `.punt-labs/ethos/{CLAUDE.md,ETHOS-SETUP.md,enabled,.vendored-manifest}` | Intended source; staging still local to S |

The delegation prompt and its consent receipt are
`.punt-labs/local/ethos/trace/missions/<id>/delegations/<d>/{prompt.md,prompt-consent.yaml}`.
`internal/layout/layout.go` also lists the pattern
`missions/*/reflection-intent.yaml` (singular, classified never-tracked) so the
upgrade can recognize it; writes use `reflection-intents/<round>.yaml`.

### Global paths

Under `~/.punt-labs/ethos/` (not part of S or C):

- Session roster: `sessions/<sid>.yaml` and `.lock`; session pointer
  `sessions/current/<claude-pid>` (staged as `sessions/current/current-*.tmp`,
  `internal/session/store.go:900`); per-session mission state
  `sessions/<sid>/{active-mission,active-mission-origin,delegation-binding,dispatch-pending/}`
  with `dispatch-pending/.lock` (`internal/session/store.go`,
  `internal/mission/active.go`).
- Delegation locks: `delegations/<d>.lock` (`internal/mission/delegation.go:206,256`).
- ID allocation: `counters/missions-<date>` and `counters/delegations-<date>`,
  each with a `.lock` (`internal/mission/id.go`).
- Identities written by `ethos setup --file`: the human and agent identity
  files go to the **global** `identities/<handle>.yaml`, not the repository
  (`cmd/ethos/setup.go:193-194`). The repository receives
  `.punt-labs/ethos.yaml`, the `.gitignore` local-zone rule (`setup.go:276`),
  `C/.claude/agents/*.md` (`setup.go:401-406`), and
  `.punt-labs/ethos/teams/<bundle>.yaml` only when leadership seats are rebound
  (`setup.go:459-477`).
- Seed content from `ethos seed`: `roles/`, `talents/`, `personalities/`,
  `writing-styles/`, `archetypes/`, `pipelines/`, `bundles/`, skills, and
  `.seed-manifest.json`; skills are also placed at `~/.claude/skills/<name>`.
- Global ADRs `adrs/<id>.yaml`, the store outside a repository. A global ADR
  linked from a repository's missions is copied into the repository store by
  the automatic upgrade (step 12). In repository mode the ADR mutation takes the
  global store's lock `adrs/.adr.lock` in this directory first, then the
  repository lock `S/.punt-labs/local/ethos/locks/adr/<sha256>.lock`
  (`internal/adr/persistence.go`); no global ADR is written.

Prompt and chunk archives mirror paths relative to `.punt-labs/ethos/`, for
example `trace/missions/<id>/delegations/<d>/prompt.md` and
`trace/sessions/<date>-<sid>/audit-<first>-<last>.jsonl`. Reflections instead use
per-round envelopes, not a local aggregate `trace/missions/<id>/reflections.yaml`.
Raw quarantined bytes stay local even if their redacted diagnostic is shareable.
Agent artifacts written to an older location are agent-owned: ethos does not
move, delete or redact them, and adds no ignore exception under
`.punt-labs/ethos/`.

S retains prompts, reflections, sealed chunks **and unsealed tails** in both
modes. Removing a feature worktree C distinct from S cannot remove their only
copy, even without SessionEnd. This is not protection against deleting S, cleaning
its local tree, disk loss, or removing a checkout that itself owns S (including
the supported bare-common-directory case where S = C).

## Recording policy

`record` in S's `.punt-labs/ethos.yaml` is `blame` (default when omitted) or
`full`. Validate once at request entry and pass an immutable roots/policy snapshot
through every subordinate operation, including each call in a long-lived MCP
server. Invalid values fail visibly on CLI, hooks and MCP; never choose a fallback
destination. Do not mutate shared stores' policy between concurrent requests.

- **blame:** retain trace locally; archival sealing neither queries nor changes
  the Git index and stages no trace.
- **full:** retain the same local trace and add eligible published copies of
  prompts, reflections, mission chunks and sealed **session audit** chunks into C.
  Full recording does not relocate the sole archive into C.
- Eligibility belongs to each immutable entry: prompt content identity,
  mission/round/reflection digest, or stream/entry/payload digest. Persist consent
  with the local payload atomically or recoverably. Missing or legacy consent is
  private. A whole-file rewrite or a seal under full cannot promote private older
  entries. Published collections union eligible entries with already published
  entries. Pending eligible copies remain retryable from their captured consent,
  even after switching back to blame; the switch alone publishes nothing.

Mode changes affect future eligibility only: no deletion of published history,
retroactive consent, changed round authority, budget or evaluator pin. Archive
completion and publication completion are separate facts. The local-zone ignore
rule is shared across modes; no mode-specific trace ignores or force-add workflow.

Repository enable/setup/vendor ignore maintenance requires a working Git
executable to verify effective repository-owned directory exclusion at S and C.
A literal rule or coverage of selected filenames is not proof of protection.
Missing Git, unreadable ignore sources, or defeated protection cause a visible
failure; conflicting user rules are preserved for operator repair. This does not
change the separate global setup path outside a repository.

Prompt eligibility receipts bind version, mission, delegation and the SHA-256 of
the actual guarded prompt bytes to captured publication consent. A delegation's
`PromptHash` identifies an expected prompt for readers; it is not publication
consent. Neither an existing delegation record nor a prompt without a valid
receipt gains consent when recording mode changes.

## Round authority and interrupted persistence

New contracts initialize this non-null block; callers cannot seed decisions:

```yaml
round_decisions:
  version: 1
  entries: []
```

Each entry has exactly `id` (32 lowercase hexadecimal digits), `round`,
`created_at` (RFC3339), `author` (validated/redacted), and `recommendation`.
At most ten entries, unique IDs, strictly increasing unique rounds, and
`1 <= entry.round <= current_round <= budget.rounds <= 10`. Every crossed round
has exactly one nonterminal decision; the current round has zero or one.
Recommendations preserve `continue`, `advance`, `pivot`, `stop`, `escalate`.
No reason, signals, digest, budget copy or evaluator copy belongs in this block.

A complete structured Reflection is still mandatory on submission. Validate
supplied timestamps; fill an omitted one once and retain it on retry. Supplied
authors are validated, not authenticated as the leader. Missing decision blocks
mean a mission whose round history is incomplete, not initialized empty
authority; the automatic upgrade (step 3) writes decisions only from complete
validated evidence and otherwise leaves the mission fenced from lifecycle
writes. Explicit null, malformed/unknown versions, duplicate YAML keys,
missing/null entries and multiple documents refuse; defaults cannot repair
malformed v1.

Under the canonical mission lock, reconcile pending receipts before later mission
events can overtake them. Persist a durable local redacted intent containing the
normalized reflection, compact decision, exact event, original stream ID,
operation ID, allocated timestamp and captured consent. Derive paths internally.
Acquire stream locks in mission-to-stream order. Append and durably confirm the
required event (including required directory entries) **before** installing the
shared contract decision. The decision rename is the sole acceptance point.
Never auto-commit a decision during recovery.

- Before acceptance, failure means not applied; retain/classify preparation.
  Exact retry reuses ID, timestamp, original stream and consent, checking original
  live/archive/published sources to avoid a duplicate logical event.
- A matching accepted decision and receipt permits archive/publication/cleanup
  completion only. No matching full payload means already decided, not permission
  to replace unavailable prose. Conflicting receipt/event/authority refuses.
- After rename, directory-sync failure is applied/durability unconfirmed; later
  archive/publication failure is applied/trace pending. Retain recovery evidence
  and expose diagnostics in machine output too. Unreadable authority means unknown
  outcome and permits no recovery or lifecycle writes.

For operation-ID-bearing reflect events, readers join with shared decisions:
matching means accepted, no decision means preparation/attempt, unreadable
authority means acceptance unknown. A sealed raw event grants no permission.
Generic Update must reject stale or changed decisions, current round, lifecycle
status/closed-at, budget and complete evaluator tuple under the mission lock.
Ordinary result, advance, close, abandon and Update event-failure rollback remains;
this is not a generic post-commit required-event replay system.

Corrections use strict shared `corrections.yaml` authority:
`version: 1`, `mission`, and `entries` of `{id, created_at, correction}` with the
existing redacted Correction value. Empty initialized is not missing/unmigrated.
Use durable intent/event-before-install ordering, preserving all correction guards
and author resolution. Correct changes no lifecycle field. Do not deduplicate
legitimate identical annotations by prose. Recovery of a known receipt is
idempotent; arbitrary repeated requests are not promised exactly once.
Readers derive correction acceptance from the shared entry, not the existence
of a prepared correction event; unreadable authority leaves acceptance unknown.

## Publication, squash and exports

These commands are proved by the `publish_squash`, `full_export` and `modes` cases:

| Operation | Contract |
|---|---|
| `ethos mission publish <ids>...` | Locked per-mission immutable snapshots from S into C and C's actual index; no implicit trace under blame |
| `ethos provenance message <ref>` | Generate the squash body carrying the immutable provenance reference |
| `ethos provenance verify <ref>` | Verify inventory, references and source associations against the actual commit |
| `ethos mission export <id> [--dir]` | Existing portability operation: redacted contract and final result; does not stage |
| `ethos mission export <id> --full` | Explicit promotion of retained trace for this mission regardless of original mode; does not stage |

Complete publication includes contract/inline decisions, **all accepted results**,
delegation records, corrections, selected summary rows and applicable ADRs
(mission-linked plus explicitly selected additional IDs). A versioned inventory
marks required/optional records; unsupported kinds refuse rather than vanish.
Snapshot cuts are per mission, not an implied repository-wide instant. Repair or
refuse missing required summaries. Preserve unrelated rows; conflicting identities
refuse. Historical reads use the manifest-bearing commit, not today's S files.

Prepare a private candidate index from the actual starting index, validate the
inventory, then install under Git's index lock only if the starting index still
matches. Preserve unrelated staged entries and reject divergent selected edits.
Never restore an old whole index over another writer's work. Report partial
checkout installation separately from index success. The supported commit boundary
must revalidate the inventory against the actual committing index, including an
alternate index and partial-commit forms.

Immutable manifests contain explicit mission/delegation association tuples,
paths, source-snapshot blob identities and ranges, and the branch-derived evidence
needed after branch objects disappear. Trailers record associations, not
authenticated authorship. Multiple candidates stay ambiguous; never pick the
first delegation, ticket match or write-set overlap. Blame lookup uses the blamed
commit's original filename/line, not today's coordinates. Source blobs must match
the candidate squash blobs; otherwise refuse and regenerate.

The supported remote route generates a body, pins the reviewed head with
`gh pr merge --squash --match-head-commit SHA --body-file GENERATED`, then fetches
and verifies the actual merge SHA. Local hooks do **not** run on GitHub's squash
server. Verification failure is failed provenance, not success with a hidden
warning. See the [merge example](example/07-pr-and-merge.md).

Full export snapshots/seals the selected mission's **unsealed tail** locally and
exports retained prompts, reflections and mission event chunks through redaction.
It excludes session audits, unrelated missions, live files as files, scratch,
raw quarantine, overlays, agent artifacts and conversation transcripts. Missing
expected history reports incompleteness, not empty success. Existing immutable
chunks are not rewritten. Plain export's default remains relative to C's root
regardless of cwd; explicit `--dir` remains cwd-relative. Published/exported files
use ordinary `git add`, never `git add -f`.

## Filesystem readers and failure domains

### Reflection files and the authority join

For new repository writes, let **L** = `S/.punt-labs/local/ethos/` and read:

- Shared authority: `S/.punt-labs/ethos/missions/<id>/contract.yaml`, specifically
  its strict v1 `round_decisions` block above.
- Accepted archive: `L/trace/missions/<id>/reflection-<round>.yaml`.
- Pending receipt: `L/missions/<id>/reflection-intents/<round>.yaml`. This can
  precede acceptance or remain after acceptance while archive/publication/cleanup
  is pending. Successful completion removes the receipt, not the archive.
- Published aggregate: `C/.punt-labs/ethos/missions/<id>/reflections.yaml` (also
  inspect S's record copy when S differs from C). Full export uses this aggregate
  representation at the chosen export destination. It is a YAML mapping with
  `reflections: [<Reflection>, ...]`, not the local envelope format.

Both the per-round archive and receipt are single YAML documents with these
top-level fields:

| Field | Meaning |
|---|---|
| `version`, `kind`, `mission` | `1`, `reflect`, and the containing mission ID |
| `decision` | Exact compact entry: `id`, `round`, `created_at`, `author`, `recommendation` |
| `reflection` | Full typed value: `mission`, `round`, `created_at`, `author`, `converging`, `signals`, `recommendation`, `reason` |
| `event` | Exact required reflect event: `ts`, `event`, `actor`, `details`, `record`, `operationid`, `acceptance` |
| `event_session` | Original mission-event stream ID; it need not be the reader's session |
| `publish` | Captured boolean full-mode consent; missing/legacy consent is private, not permission inferred from today's mode |

In the envelope's **YAML** event, the key is `operationid`; the corresponding
live/sealed **JSONL** event uses `operation_id`. `event.details` contains `round`,
`recommendation`, `converging` and `signal_count`. `event.record` is `full` when
`publish: true`, otherwise `blame`. The serialized `event.acceptance` is not
authority (new envelopes leave it empty). `event_session` identifies the original
`L/missions/<id>/<event_session>.log.jsonl` stream even after its tail is sealed.
Event `ts` and reflection `created_at` are separately allocated timestamps;
exact retry retains both.

Validate mission/round identity and agreement between the reflection, decision
and event. Join an envelope to the shared entry by round and **all five decision
fields**, including `decision.id = event.operationid`; the reflection's round,
timestamp, author and recommendation must match the decision. Equal complete
copies collapse; unequal detail or a mismatched decision is a conflict, not a
replacement candidate. Published aggregates have no operation ID: match their
reflection mission, round, timestamp, author and recommendation against authority
and compare full payloads across admissible copies. A receipt or raw reflect
event without a shared decision is preparation only, never permission to advance.
A matching receipt may supply accepted detail even before archival completion.
An accepted decision with no admissible local/published detail means submitted,
detail unavailable here; it is not unsubmitted and cannot authorize replacement.
Do not mutate or replay receipts from a filesystem reader. Unlocked observations
can span writer phases: reread authority/detail before declaring a transient
absence final, and retain unknown/conflict outcomes rather than invent acceptance.

### Reader outcomes

Filesystem integration remains independent of the binary. Readers resolve S/C and
union local live/archive and published sources independently of current mode.
Compare canonical payloads **before** watermark filtering: equal identities
collapse, unequal content is a named conflict. Timestamp collisions retain
their separate identity rules, not lossy first-wins dedup.

| Observation | Required meaning |
|---|---|
| Mission absent | Mission-not-found error |
| Valid initialized authority, no decision for round | Not submitted; preparation may additionally be pending |
| Missing decision block (round history incomplete) | Authority unavailable; not certified as non-submission, and lifecycle writes refuse |
| Accepted decision, no admissible detail in this clone | Submitted, detail unavailable here; not an empty reflection log |
| Valid authority, corrupt optional detail | Decision present, detail unreadable; permission unchanged |
| Malformed/unreadable authority or conflicting copies | Error/unknown, never an empty-success or lifecycle bypass |
| Missing expected export/prompt/history artifact | Incomplete; not proof it was never recorded |

Quarry must distinguish these outcomes and explicitly repair incomplete indexed
memory after later authorized sharing. Desk must discover local versus published
prompts and the local artifact destination. Ethos keeps no fallback reader for
them. Mission transcript tagging in
Quarry remains proposed; no implemented hook parity is claimed.

Audit recording failures remain visible without blocking the tool invocation.
Manual archive commands report errors. Under default blame, commit-context
archival failure reports retained data and permits unrelated source commits;
invalid configuration and required blame/full publication failure remain blocking.
Mission-event failure is a separate transactional domain, never best-effort
successful lifecycle acceptance.

## Automatic storage upgrade

Ethos upgrades its own storage; no command exists for it. Every CLI command,
hook and MCP call passes the upgrade gate (`autorepair.Ensure`) before it reads
storage. `version`, `completion`, `help` and `uninstall` are exempt.

**Stamps.** A stamp file records that a store is at layout 5, the DES-079
layout. There are two: `S/.punt-labs/local/ethos/layout.yaml` for the
repository and `~/.punt-labs/ethos/layout.yaml` for the machine. Each holds the
layout number, the ethos binary version, the time of the upgrade and any
residual items. With both stamps current the gate costs two `stat` calls and
one small read. A missing stamp means unknown, so every step runs. A stamp with
a higher number is treated as current: an older binary never converts backward.

**Steps.** Seventeen steps run in a fixed order (`autorepair.StepOrder`). Each
is idempotent by observed state, not by release number. They import this
repository's missions, session audit and ADRs from the global store; write
round decisions and corrections from complete validated evidence; scrub
tracked records and rewrite live tails that predate redaction; archive
record-tree trace into the local zone, redacted, then remove it from the tree;
salvage a corrupt sealed chunk (decodable lines are redacted into a replacement
chunk, raw bytes deleted, digests logged); move repository overlays; untrack
local files from the index (they stay on disk); replace the old managed
`.gitignore` block with the single `.punt-labs/local/` rule; rename
`.punt-labs/ethos/config.yaml` to `.punt-labs/ethos.yaml`; move a `voice:` field
into the `vox` extension; rewrite `inputs.bead` to `inputs.ticket`; delete old
locks and staging leftovers; refresh stale hook sections; retire dead session
rosters. Pre-commit runs the untrack, live-tail and hook steps on every commit,
before the seal; session start runs the roster step.

**Log and notice.** Each step appends JSON lines to
`~/.punt-labs/ethos/logs/repair.log`: step, action, redacted paths and any
error, never file contents. The log is capped at 1 MB with one rotation
(`repair.log.1`). The only user-visible output is one line, printed only when
something was repaired:

```text
ethos: upgraded storage (N items; see ~/.punt-labs/ethos/logs/repair.log)
```

Session start puts it in the hook's system message; pre-commit prints it to
stderr only; the CLI prints it to stderr; an MCP call adds a second content
block. `ethos doctor` shows a `Storage` row with the layout and upgrade time.

**Busy locks and losses.** The upgrade takes a global lock, then a repository
lock, and waits at most 2 s. A busy lock, an index lock or an unreadable file
defers the step: it logs, the stamp is not written, and the next run resumes. A
lock is never stolen. A step that finds a permanent condition (for example a
mission whose round history is incomplete) records it as a residual in the
stamp and counts as finished. Nothing an upgrade step does fails the command,
hook or MCP call. Losses are logged, never fatal.

**Never touched.** Shared identities, contract lifecycle fields, accepted
results, corrections, ADR text, provenance manifests, private credentials,
configuration, the outside-a-repository global stores, current publications,
explicit exports (`missions/<id>.contract.yaml`, `missions/<id>.result.yaml`
with their associated trace), full-mode published copies, agent-written
artifacts, user `.gitignore` lines outside the former managed block, foreign
git hooks and git history. The upgrade does not stage or commit anything, and
there is no backup. Reconnect long-lived MCP servers after installing a new
binary. Re-running `enable` maintains the shared local-zone exclusion and
current hooks, not record-tree ignore exceptions.

**Private overlays.** A repository sibling
`S/.punt-labs/ethos/identities/<handle>.ext/<ns>.local.yaml` is moved under
`S/.punt-labs/local/ethos/identities/<handle>.ext/<ns>.local.yaml`, with all
credentials preserved. When both exist the new one is kept and the old one is
deleted. A merged identity is never serialized into the base, and credentials
are never printed in diagnostics. Global overlay locations are unchanged.

Repository overlay handling preserves base-only writes, per-key local precedence,
namespace limits, source-layer selection and read-only bundles. Direct base-file
readers still see base only unless they explicitly implement the overlay contract;
never serialize a merged identity into tracked space. A local overlay is not a
vault and may reach runtime model context. Global overlay behavior is unchanged.

## Promise to implementation to acceptance

Each row names the acceptance case in `scripts/storage_acceptance.py` (run by
`make check-storage` against the built CLI, plugin hook wrappers, MCP server and
installed Git hooks in scratch repositories) and the Go tests that prove it.
The script does not inject crashes, fsync failures or `EXDEV`, and has no
competing-index writer; the Go tests named here cover those boundaries.

| Promise / authority | Implementation surface | Proof |
|---|---|---|
| Physical boundary, including transient writes (DES-079/075) | `internal/layout`, all record/identity/enable/seed/hook writers | Final-path walk (`check_boundary`) at the end of `default_seal`, `modes`, `audit_recovery`, `lifecycle`, `required_event_failure`, `worktree_retention`, `full_export`, `publish_squash`, `mcp_modes`, `upgrade`; `TestGeneratedGateStorageBoundary` |
| Canonical local owner (DES-058/062) | `internal/resolve`, audit/mission paths, close/seal | `worktree_retention`: linked C removed without SessionEnd; sealed audit, unsealed tail and reflection stay readable from S |
| Strict request policy and prospective consent | Config resolver, CLI/hooks, MCP request guard, trace writers | `modes` (unset, blame, full, seven invalid values; private history not promoted), `mcp_modes` (one server switched blame/full/blame, then invalid), `TestDoctorConfigSnapshotConcurrentRequests`, `TestCorrectionAuthorityInterruptedRetryPreservesConsent` |
| Mandatory reflection, shared decisions (DES-034) | Mission strict decoder, reflect/advance/Update, CLI/MCP/UI | `lifecycle`: five recommendations, null/unknown-version/null-entries decision blocks, unknown field, multi-document, wrong mission and round, exact versus changed retry, fresh-clone handoff |
| Event-before-decision acceptance | Mission intent/event persistence and recovery | `required_event_failure`: a failed mission event stream leaves shared authority unchanged for `reflect` and `result`, and retry does not duplicate; `TestCorrectionIntentAncestryAcceptedRetry`, `TestCorrectionPriorAcceptedRecoveryWarning` |
| Shared corrections (DES-072) | Correct writer/readers, export/publication | `lifecycle` (a correction after close leaves lifecycle state unchanged; `corrections.yaml` present), `publish_squash` (corrections survive squash and fresh clone), `TestCorrectionAuthorityMissingV1RefusesBeforePreparation` |
| Complete selected blame publication | Mission snapshots, provenance, Git actual-index transaction | `publish_squash`: two missions, three delegations, ADR, alternate `GIT_INDEX_FILE`, held index lock, missing inventory caught by `provenance verify-index`, unrelated staged bytes unchanged |
| Squash-safe line attribution | Manifest/message/verify, UI original blame coordinates | `publish_squash`: actual `merge --squash`, source branch deleted, fresh clone verifies, known and ambiguous sources |
| Local audit usability without source churn | Audit append/seal/query and hooks | `default_seal` (dry-run and repeated seal, index bytes unchanged, held index lock, foreign pre-commit hook runs once, `enable` idempotent), `audit_recovery` (corrupt chunk salvaged automatically, unrelated commit not blocked), `required_event_failure` (audit write failure is an advisory message), `TestPreCommitUnstagesLocalAndRunsNamedSteps` |
| Plain versus full export | Mission CLI/export readers/redactor | `full_export`: unsealed tail exported; session audit, scratch, quarantine and other missions excluded; live files not copied; no staging; ordinary `git add` works; synthetic credential not exported |
| Extensions and repo ADRs (DES-057/079) | Identity layering/persistence, ADR CLI/MCP store | `publish_squash` (`adr create`, then `adr show` in a fresh clone), `TestOverlaysStepMovesLegacyOverlay`, `TestOverlaysStepBothLocationsKeepsNew`, `TestCLI_RepositoryIdentityOverlayDivergence` |
| Automatic storage upgrade; generated content | `internal/autorepair`, enable, doctor, generator | `upgrade`: every released layout in the committed corpus upgraded with no user action, at most one notice line, no tracked machine-local files afterward, no credential-shaped token in files or new history; `TestEnsureWritesStampsAndIsIdempotent`, `TestEnsureBusyLockContinuesWithoutFailing`, `TestEnsureRecordsResidualsInStamp`, `TestEnsureConcurrentCallsConvertOnce`; `TestAgentCurrencySourceEditsCauseDrift` (generated drift) |
| Independent integration surfaces | Filesystem contract, Quarry/Desk consumers | `lifecycle` and `worktree_retention` read S's files with no Quarry or Desk present |
| Redaction (DES-080) | DES-080 writers | `full_export` (synthetic credential absent from the export), `upgrade` (redaction of pre-redaction tails and no token in files or new history) |

Release proof is `make check` (race tests and applicable formal checks),
including `check-storage`, plus independent product, code, security and formal
review and compilation of changed product and formal PDFs.

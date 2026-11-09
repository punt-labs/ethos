# Storage and publication contract

**Status:** The DES-079 features listed under 4.21.0 in `CHANGELOG.md` shipped
in v4.21.0; release acceptance is pending. The placements in the Paths table
were observed on v4.22.1 through real entry points on 2026-11-09. Every row in
"Promise to implementation to acceptance" stays **Pending**. "Shipped versus
proven" lists the partial `check-storage` evidence per row.
`make check` includes `check-storage` (`scripts/storage_acceptance.py`), which
drives the real CLI, hook wrappers and Git hooks. DES-080 redaction shipped in
v4.20.0; that does not establish DES-079 acceptance.

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
| Mission summaries; repo ADRs | `.punt-labs/ethos/{missions.jsonl,adrs/<id>.yaml}`; `missions.jsonl` is written on close; `ethos adr create` writes `adrs/<id>.yaml` in the repository store (`S/.punt-labs/ethos/adrs/`) | Blame records; ADR reads repo-first, legacy global fallback |
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
| Legacy, read-only | record-tree locks (`missions/**/*.lock`, `missions.jsonl.lock`, `identities/*.yaml.lock`), `missions/*/log.jsonl`, `log-*.quarantine`, `*.corrupt*`, `missions/*/artifacts/**`, `sessions/*/audit.jsonl`, `.punt-labs/ethos/config.yaml`, singular `reflection-intent.yaml` | Compatibility inputs; new writes go elsewhere |
| New worker artifacts | `.punt-labs/local/ethos/artifacts/missions/<id>/delegations/<d>/`, the value pre-tool-use exports as `MISSION_ARTIFACTS_DIR` (`internal/mission/delegation.go`, `internal/hook/pretooluse_dispatch.go`) | Agent-owned, not automatically exported or redacted |
| Generated agents and enablement guides | C's `.claude/agents/*.md` (generated plus the three seeded review agents), the `CLAUDE.md` `@`-import, the `.gitignore` rule `.punt-labs/local/`, and `.punt-labs/ethos/{CLAUDE.md,ETHOS-SETUP.md,enabled,.vendored-manifest}` | Intended source; staging still local to S |

The delegation prompt and its consent receipt are
`.punt-labs/local/ethos/trace/missions/<id>/delegations/<d>/{prompt.md,prompt-consent.yaml}`.
`internal/layout/layout.go` also still lists a legacy pattern
`missions/*/reflection-intent.yaml` (singular, classified never-tracked); new
writes use `reflection-intents/<round>.yaml`.

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
- Legacy ADRs `adrs/<id>.yaml` (read-only fallback). In repository mode the
  ADR mutation takes the legacy store's lock `adrs/.adr.lock` in this directory
  first, then the repository lock
  `S/.punt-labs/local/ethos/locks/adr/<sha256>.lock`
  (`internal/adr/persistence.go`); no global ADR is written.

Prompt and chunk archives mirror paths relative to `.punt-labs/ethos/`, for
example `trace/missions/<id>/delegations/<d>/prompt.md` and
`trace/sessions/<date>-<sid>/audit-<first>-<last>.jsonl`. Reflections instead use
per-round envelopes, not a local aggregate `trace/missions/<id>/reflections.yaml`.
Raw quarantined bytes stay local even if their redacted diagnostic is shareable.
Legacy misplaced agent artifacts require operator-directed cleanup, not a new
ignore exception under `.punt-labs/ethos/`. Ethos does not silently move or redact
agent-owned files.

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
mean legacy authority requiring inspection, not initialized empty authority.
Explicit null, malformed/unknown versions, duplicate YAML keys, missing/null
entries and multiple documents refuse; legacy defaults cannot repair malformed v1.

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

These commands shipped in v4.21.0; entry-point acceptance is pending:

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
  `reflections: [<Reflection>, ...]`, not the local envelope format. Supported
  legacy aggregates are compatibility inputs, not new local write targets.

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
union local live/archive, published and supported legacy sources independently of
current mode. Compare canonical payloads **before** watermark filtering: equal
identities collapse, unequal content is a named conflict. Legacy timestamp
collisions retain their separate identity rules, not lossy first-wins dedup.

| Observation | Required meaning |
|---|---|
| Mission absent | Mission-not-found error |
| Valid initialized authority, no decision for round | Not submitted; preparation may additionally be pending |
| Missing legacy decision block | Legacy unknown; inspect/migrate before certifying non-submission |
| Accepted decision, no admissible detail in this clone | Submitted, detail unavailable here; not an empty reflection log |
| Valid authority, corrupt optional detail | Decision present, detail unreadable; permission unchanged |
| Malformed/unreadable authority or conflicting copies | Error/unknown, never an empty-success or lifecycle bypass |
| Missing expected export/prompt/history artifact | Incomplete; not proof it was never recorded |

Quarry must distinguish these outcomes and explicitly repair incomplete indexed
memory after later authorized sharing. Desk must discover local versus published
prompts and the local artifact destination. Their existing sibling-file readers
are not fixed merely by an Ethos legacy fallback. Mission transcript tagging in
Quarry remains proposed; no implemented hook parity is claimed.

Audit recording failures remain visible without blocking the tool invocation.
Manual archive commands report errors. Under default blame, commit-context
archival failure reports retained data and permits unrelated source commits;
invalid configuration and required blame/full publication failure remain blocking.
Mission-event failure is a separate transactional domain, never best-effort
successful lifecycle acceptance.

## Bounded legacy repair

There is no new storage migration framework or lossless legacy-trace release
guarantee. The bounded `ethos repair` contract is to show known obsolete files,
warn that deleting them permanently loses legacy detail, and ask
`Are you sure? [y/N]`. `--yes` is explicit consent for automation, not implicit
consent from running another command. `ethos repair --dry-run` only inventories;
without `--yes`, destructive confirmation requires a terminal. Empty input, no
and EOF delete nothing. Destructive repair currently refuses on Windows; inventory
is available, but deletion requires a Unix host with all writers stopped. This
repair command remains subject to final entry-point acceptance; historical shipped
`ethos mission migrate` / `ethos audit migrate` are not expanded into a DES-079
migration workflow.

**Deletion warning:** the operator may explicitly remove identified obsolete
trace/runtime files; that can permanently lose old
prompts, reflections, events and audit history. Name the affected paths and warn
before deletion. Shared identities, contracts, results, corrections, provenance
and ADRs, private credentials, configuration, global data and arbitrary files are
excluded from blanket cleanup; do not silently erase them.
Canonical local history, current publications and flat current exports
(`missions/<id>.contract.yaml`, `missions/<id>.result.yaml` with their associated
trace) are retained, including incomplete/ambiguous exports. Other intentional
publications and exports also require explicit operator selection outside blanket
cleanup, not a tree-wide delete. No backup, migration, authority backfill, lifecycle
transition, Git staging/commit or configuration rewrite is performed. Deletion is
incremental, not transactional: after interruption, inspect a fresh inventory and
confirm again; no rollback or lossless legacy recovery is promised. This
operator-relaxed legacy requirement does not weaken new
shared authority, correct placement, ordinary commits or retention of new local
history when a feature worktree is removed. Stop all old/new writers, hooks and
MCP servers before confirming; a busy lock refuses and is never stolen. Then run
`ethos enable` and reconnect long-lived MCP servers. Re-running enable
maintains the shared local-zone exclusion and current hooks, not record-tree
ignore exceptions.

**Private legacy overlays are operator-directed, not repair candidates.** Stop
writers before handling a repository sibling
`S/.punt-labs/ethos/identities/<handle>.ext/<ns>.local.yaml`. Reconcile it into
`S/.punt-labs/local/ethos/identities/<handle>.ext/<ns>.local.yaml`, preserving all
credentials and resolving any existing destination before removing the old copy.
Do not overwrite divergent values, serialize a merged identity into the base, or
print credentials in diagnostics. Legacy siblings are read-only compatibility
inputs; local writes refuse while one remains, and unequal local/legacy copies
refuse reads. `ethos repair` neither moves nor deletes overlays; there is no
storage-upgrade command to do it. Global overlay locations are unchanged.

Repository overlay handling preserves base-only writes, per-key local precedence,
namespace limits, source-layer selection and read-only bundles. Direct base-file
readers still see base only unless they explicitly implement the overlay contract;
never serialize a merged identity into tracked space. A local overlay is not a
vault and may reach runtime model context. Global overlay behavior is unchanged.

## Promise to implementation to acceptance

All DES-079 rows are **pending** until the leader attaches actual command results
at the tested revision. Package/file pointers are implementation targets, not
claims of completeness. Focused checks alone do not establish release acceptance.

| Promise / authority | Implementation surface | Required acceptance / current evidence |
|---|---|---|
| Physical boundary, including transient writes (DES-079/075) | `internal/layout`, all record/identity/enable/seed/hook writers | **Pending:** real CLI/MCP/hooks; observe transient and final paths, failures and `EXDEV`; old targets intact |
| Canonical local owner (DES-058/062) | `internal/resolve`, audit/mission paths, close/seal/purge | **Pending:** linked C removal without SessionEnd retains prompts, reflections, chunks and tails; interrupted seal/retry |
| Strict request policy and prospective consent | Config resolver, CLI/hooks, MCP request guard, trace writers | **Pending:** unset/blame/full/invalid, both switches in one server, concurrent requests and mid-call config edit, mixed private/full entries |
| Mandatory reflection, shared decisions (DES-034) | Mission strict decoder, reflect/advance/Update, CLI/MCP/UI | **Pending:** malformed/null/duplicate/multidoc inputs, all five recommendations, rounds 1–10, stale Update, isolated-HOME handoff |
| Event-before-decision acceptance | Mission intent/event persistence and recovery | **Pending:** every interruption/write/fsync/rename boundary, exact cross-session retry, prepared-event rendering, conflict refusal, unchanged ordinary rollback |
| Shared corrections (DES-072) | Correct writer/readers, export/publication | **Pending:** interruption/retry, fresh clone, repeated identical legitimate annotations, unchanged lifecycle |
| Complete selected blame publication | Mission snapshots, provenance, Git actual-index transaction | **Pending:** two missions/multiple delegations, all results/ADRs/corrections, unknown/missing inventory, alternate/partial index, competing writer, lock/fault cases; unrelated staged bytes unchanged |
| Squash-safe line attribution | Manifest/message/verify, UI original blame coordinates | **Pending:** actual squash, branch removal, isolated-HOME fresh clone, known and ambiguous ranges, later insertion/rename, tampered source/reference refusal |
| Local audit usability without source churn | Audit append/seal/query/quarantine and hooks | **Pending:** real entry points, seal twice, visible corruption/gaps, equal/conflicting overlaps, default seal no index access, nonblocking audit versus required-event failure |
| Plain versus full export | Mission CLI/export readers/redactor | **Pending:** plain C-root/cwd `--dir` regression; full blame-history export with unsealed tail, missing history, redaction, exclusions and ordinary add |
| Extensions and repo ADRs (DES-057/079) | Identity layering/persistence, ADR CLI/MCP store | **Pending:** base-only writes preserve overlay secrecy/limits/bundles; repo-first/global fallback; redacted ADR publication |
| Bounded legacy repair; generated content | Repair, enable, doctor, generator | No lossless migration gate. **Pending:** known-obsolete inventory, permanent-loss warning, default-no confirmation, explicit `--yes`, cleanup exclusions; new lock coordination, byte-identical agents; generated/installed drift fails, seeded edits warn |
| Independent integration surfaces | Filesystem contract, Quarry/Desk consumers | **Pending:** identity without missions, missions without teams, tracing without Quarry, blame without origin-local state, later explicit memory repair |
| Redaction shipped; repair still required | DES-080 writers; `CHANGELOG.md` v4.20.0 | Release evidence exists for DES-080. **Pending:** new destinations, publication/export/ADR sinks and legacy-tail handling at final DES-079 revision |

### Shipped versus proven

The table above keeps its rows **Pending**. The table below names, per row,
what ships and which `check-storage` scenario in `scripts/storage_acceptance.py`
exercises it. The script runs the built CLI, the plugin hook wrappers and the
installed Git hooks in scratch repositories. By its own `coverage_limits` it
does not prove transient placement, and it has no crash/fsync/`EXDEV`
injection, competing-index writer, live UI or external-consumer checks. A
scenario named here is evidence for the listed behavior only, not for the whole
row.

| Row | Shipped | Covered by `check-storage` scenario | Proof still missing |
|---|---|---|---|
| Physical boundary | `internal/layout` | Final-path walk (`check_boundary`) at the end of `default_seal`, `modes`, `audit_recovery`, `lifecycle`, `required_event_failure`, `worktree_retention`, `full_export`, `publish_squash`, `mcp_modes` | Transient writes, failures, `EXDEV`; MCP/hook paths beyond those scenarios |
| Canonical local owner | `internal/resolve`, seal, purge | `worktree_retention`: linked C removed without SessionEnd; sealed audit, unsealed tail and reflection remain readable from S | Interrupted seal/retry |
| Strict request policy | Config resolver, request guard | `modes` (unset/blame/full and seven invalid values; private history not promoted), `mcp_modes` (one long-lived server switched blame/full/blame, then invalid) | Concurrent requests, mid-call config edit, mixed private/full entries beyond these |
| Mandatory reflection, shared decisions | `internal/mission` strict decoder, reflect/advance | `lifecycle`: all five recommendations, null/unknown-version/null-entries decision blocks, unknown field, multi-document, wrong mission and round, exact retry versus changed retry, fresh-clone handoff | Rounds 1-10 (the scenario uses budgets of 2), stale Update, UI |
| Event-before-decision acceptance | Intent/event persistence | `required_event_failure`: failed mission event stream leaves shared authority unchanged for `reflect` and `result`, and retry does not duplicate | Interruption at each write/fsync/rename boundary, cross-session retry, conflict refusal |
| Shared corrections | `corrections.yaml` authority | `lifecycle` (correction after close leaves lifecycle state unchanged; `corrections.yaml` present), `publish_squash` (corrections survive squash and fresh clone) | Interruption/retry, repeated identical annotations |
| Complete selected blame publication | `ethos mission publish`, `internal/provenance` | `publish_squash`: two missions, three delegations, ADR, alternate `GIT_INDEX_FILE`, held index lock, missing inventory caught by `provenance verify-index`, unrelated staged bytes unchanged | Competing writer, partial-commit forms, lock/fault cases |
| Squash-safe line attribution | `provenance message`, `provenance verify` | `publish_squash`: actual `merge --squash`, source branch deleted, fresh clone verifies, known and ambiguous sources | Later insertion/rename, tampered source/reference refusal, UI blame coordinates |
| Local audit usability | `ethos audit seal`, `show`, `quarantine`, hooks | `default_seal` (dry-run and repeated seal, index bytes unchanged, held index lock, foreign pre-commit hook still runs once, `enable` idempotent), `audit_recovery` (corrupt chunk, quarantine twice, unrelated commit not blocked), `required_event_failure` (audit write failure is an advisory message) | Equal/conflicting overlaps |
| Plain versus full export | `ethos mission export [--full]` | `full_export`: unsealed tail exported, session audit/scratch/quarantine/other mission excluded, live files not copied, no staging, ordinary `git add` works, synthetic credential not exported | Plain export C-root versus `--dir` regression, missing-history reporting |
| Extensions and repo ADRs | `internal/adr` repository store | `publish_squash`: `adr create` then `adr show` in a fresh clone | Overlay secrecy/limits/bundles, global fallback, redacted ADR publication |
| Bounded legacy repair; generated content | `ethos repair`, `internal/repair`, enable, doctor | None for `repair` or `doctor`; `default_seal` covers `enable` idempotence only | Everything listed in the row |
| Independent integration surfaces | Filesystem contract | None | Everything listed in the row |
| Redaction shipped; repair still required | DES-080 writers, v4.20.0 | `full_export` checks one synthetic credential is absent from the export | New destinations, ADR sinks, legacy-tail handling |

Required release proof includes `make check` (race tests and applicable formal
checks), real entry-point acceptance, independent product/code/security/formal
review, and compilation/inspection of changed product/formal PDFs. None is
replaced by path classification, model intent, an unrun test, or this table.

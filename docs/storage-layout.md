# Storage and publication contract

**Status:** DES-079 requirements; implementation and release acceptance pending.
This is not evidence that an installed binary implements the repair. DES-080
redaction shipped in v4.20.0; that does not establish DES-079 acceptance.
The table below tracks the remaining proof obligations, not test passes.

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
  `~/.punt-labs/ethos/` layout. DES-081 is not a prerequisite.

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
| Contract, including inline decisions | `.punt-labs/ethos/missions/<id>/contract.yaml` | Shared workflow authority |
| All accepted results, delegation records, corrections | `.punt-labs/ethos/missions/<id>/{results.yaml,delegations/<d>/record.yaml,corrections.yaml}` | Blame records |
| Mission summaries; repo ADRs | `.punt-labs/ethos/{missions.jsonl,adrs/<id>.yaml}` | Blame records; ADR reads repo-first, legacy global fallback |
| Immutable publication manifest | `C/.punt-labs/ethos/provenance/` | Committed snapshot inventory and attribution evidence |
| Session audit live file | `.punt-labs/local/ethos/sessions/<sid>.audit.jsonl` | Local in both modes |
| Mission event live file | `.punt-labs/local/ethos/missions/<id>/<sid>.log.jsonl` | Local in both modes |
| Retained prompts and chunks | `.punt-labs/local/ethos/trace/` plus record-relative path | Local in both modes |
| Prompt eligibility receipt | `.punt-labs/local/ethos/trace/missions/<id>/delegations/<d>/prompt-consent.yaml` | Local only; never exported as trace |
| Accepted per-round reflection envelope | `.punt-labs/local/ethos/trace/missions/<id>/reflection-<round>.yaml` | Local in both modes; joined to shared decisions |
| Pending per-round reflection receipt | `.punt-labs/local/ethos/missions/<id>/reflection-intents/<round>.yaml` | Local preparation/recovery detail, not acceptance authority |
| Repository extension overlay | `.punt-labs/local/ethos/identities/<handle>.ext/<ns>.local.yaml` | Local; base remains in the identity's source layer |
| Locks, preparation receipts, eligibility metadata, scratch and temporary staging | `.punt-labs/local/ethos/` | Never published |
| New worker artifacts | Local scratch under `.punt-labs/local/ethos/`; `MISSION_ARTIFACTS_DIR` must name a local destination | Agent-owned, not automatically exported or redacted |
| Generated agents and enablement guides | C's `.claude/agents/` and documented guide/import/marker paths | Intended source; staging still local to S |

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

These are DES-079 command contracts pending entry-point acceptance:

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

Required release proof includes `make check` (race tests and applicable formal
checks), real entry-point acceptance, independent product/code/security/formal
review, and compilation/inspection of changed product/formal PDFs. None is
replaced by path classification, model intent, an unrun test, or this table.

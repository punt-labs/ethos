# Design: Conversation and Mission Capture

How ethos turns Claude Code session JSONLs and ethos mission state
into committed markdown via a pre-commit hook. This doc covers
architecture, file layouts, schemas, hook behavior, library shape,
the library-vs-subprocess decision, performance, mission markdown
representation, backfill semantics, and failure modes.

This is the technical companion to
`docs/use-case-conversation-mission-capture.md`. The use-case doc is
the user-facing why; this doc is the implementation what and how.

## Inputs settled before this design

Three decisions are settled and not relitigated here:

1. **Pre-commit, not post-commit.** Settled by the hook timing
   spike. The data and reasoning are in
   `research/research-2026-04-25-hook-timing-spike.md`. Pre-commit
   gives atomic guarantees and visible failures; both post-commit
   variants degrade silently on three of three failure modes. The
   1.24 s per-commit cost is a future optimization candidate, not
   a reason to abandon atomicity.
2. **Markdown in `.ethos/`.** Files live at
   `.ethos/sessions/<session-id>.md` and
   `.ethos/missions/<mission-id>.md`. Replaced on every commit.
   Tracked in git.
3. **Capture is an ethos feature.** The harness owns the hook. The
   reference Python primitives in `punt-labs/.bin/` prove the shape;
   they are not the final home.

## Architecture

```text
git commit (pre-commit hook fires)
    │
    ├─► ethos capture run
    │       │
    │       ├─► resolve repo root, session id, active mission set
    │       │
    │       ├─► for each active session:
    │       │       distill JSONL → markdown
    │       │       scrub markdown → markdown
    │       │       write .ethos/sessions/<id>.md
    │       │       git add .ethos/sessions/<id>.md
    │       │
    │       └─► for each open mission whose worker is in this repo:
    │               assemble (contract YAML + event log + worker JSONLs
    │               + results + reflections)
    │               distill each JSONL → per-round markdown
    │               scrub the assembled doc → markdown
    │               write .ethos/missions/<id>.md
    │               git add .ethos/missions/<id>.md
    │
    └─► git creates the commit object (capture files included)
```

The hook does no git plumbing beyond `git add`. It does not write
commits, change branches, or push. It writes one or more files,
stages them, and exits.

## File Layout

### Repo-tracked outputs

```text
<repo>/
├── .ethos/
│   ├── sessions/
│   │   └── <session-id>.md          (per-session, replaced on commit)
│   └── missions/
│       └── <mission-id>.md          (per-mission, replaced on commit)
└── ...
```

Each file is replaced wholesale on every commit during which the
session or mission is active. HEAD always holds the full
session-so-far or mission-so-far. There is no append, no merge, no
incremental write at the file level — the markdown is regenerated
from the source JSONLs and YAMLs on each invocation.

The replace-each-commit choice traces to a single property: the
file at HEAD must be readable as the *complete* record of the
session/mission up to that commit. A reader who runs
`git show HEAD:.ethos/missions/m-2026-04-25-001.md` gets everything
known about that mission at that commit, not a fragment that
requires reassembly across history.

The two artifact kinds — session and mission — share this layout
property but differ in source shape and trigger. The user-facing
table that sets that out (sources, triggers, lifecycle, growth) is
in the use-case doc under "Relationship Between Conversation
Capture and Mission Capture". This doc does not restate it.

### Source-of-truth inputs (not in repo)

```text
~/.claude/projects/<encoded-cwd>/<session-id>.jsonl
    (Claude Code's per-session event log; we read, never write)

~/.punt-labs/ethos/missions/<mission-id>.yaml
~/.punt-labs/ethos/missions/<mission-id>.jsonl           (event log)
~/.punt-labs/ethos/missions/<mission-id>.reflections.yaml
    (ethos's per-mission state; we read, never write)
```

The capture pipeline is a one-way function from these sources to
markdown. The sources are authoritative; the markdown is derived.

## Schema for capture markdown

Every capture file starts with a YAML front matter block, then a body.

### Front matter (both kinds)

```yaml
---
schema_version: 1
kind: session | mission
id: <session-id-or-mission-id>
generated_at: 2026-04-25T15:42:00-07:00
generator: ethos@<version>
distill_version: 1
scrub_version: 1
source_records: <int>          # JSONL record count for sessions; total worker
                               # records across all rounds for missions
source_bytes: <int>            # raw input bytes summed across all sources
output_bytes: <int>
redactions:                    # categories the scrubber actually fired
  gh-pat: 0
  env-secret: 2
  profanity: 0
session_id: <session-id>       # session capture only
mission_id: <mission-id>       # mission capture only
mission_status: open | closed  # mission capture only
last_commit: <commit-sha>      # the commit at which this file was generated
---
```

Front matter is the load-bearing audit substrate. `schema_version`,
`distill_version`, and `scrub_version` are how backfill detects
"changed since." `source_bytes` and `output_bytes` give a
reduction ratio at a glance. `redactions` lets a reviewer see what
the scrubber caught without inspecting the body.

`generated_at` and `last_commit` together are the freshness
fingerprint. A capture file whose `last_commit` predates HEAD is a
sign that capture failed silently or was bypassed — useful for a
`doctor` check.

### Body (session)

The body is the output of the distiller (rendered transcript) with
the scrubber applied. The header that the reference distiller emits
(`# Session: <id>` plus `**Project:**` etc.) is suppressed in favor
of the front matter. Replaced by a single `# Session: <id>` heading
followed by the rendered records.

Section structure (verbatim from the distiller):

```text
# Session: <session-id>

---
## User · <ts>
...
## Assistant · <ts>
...
**Tool call · <name>**
...
```

No structural changes from the reference distiller's output beyond
header replacement. The signal preservation, read-class
compression, and Bash head/tail compression are inherited.

### Body (mission)

See "Mission markdown representation" below — three layouts
sketched, one recommended.

## Hook implementation

### Hook file shape

`hooks/capture-precommit.sh` — a thin shell wrapper that invokes
the `ethos capture run` Go command. The wrapper exists for
two reasons: pre-commit hooks must be executable scripts (not Go
binaries linked into a magic path), and the wrapper handles the
"hook script not executable" git inherent quirk by using a
recognizable shebang and known on-disk location.

Hook script body (representative):

```bash
#!/usr/bin/env bash
set -euo pipefail

if [[ "${ETHOS_CAPTURE:-}" == "skip" ]]; then
    exit 0
fi

if ! command -v ethos >/dev/null 2>&1; then
    echo "ethos capture: ethos binary not on PATH; skipping" >&2
    exit 0
fi

exec ethos capture run --hook pre-commit
```

The skip check is the privacy escape hatch (UC-7) and an emergency
release valve. The PATH check degrades gracefully when ethos is
not installed (e.g. on a CI runner that does not have the binary).

### Installation

Three modes, explicitly selected by `--mode`:

| Mode | Behavior | Use case |
|---|---|---|
| `chain` | Backs up existing `pre-commit` to `pre-commit.local`. Installs an ethos-managed `pre-commit` that runs ethos capture, then `pre-commit.local`. | Repo with hand-rolled pre-commit hooks. |
| `framework` | Installs a `.pre-commit-config.yaml` entry under `repo: local` that calls `ethos capture run`. | Repo using the `pre-commit` framework. |
| `replace` | Backs up existing `pre-commit` to `pre-commit.bak.<ts>` and installs ethos as the only hook. | Greenfield repo or user explicitly wants ethos to own the gate. |

`--mode` is required when an existing pre-commit hook or framework
config is detected. If no conflict exists, `--mode` defaults to
`replace` and the install proceeds.

`--yes` suppresses the interactive prompt for the no-conflict case.
It does **not** auto-pick a mode on conflict. If a conflict is
detected and no `--mode` is given, install exits non-zero with an
actionable message:

```text
ethos capture: existing pre-commit hook detected at .git/hooks/pre-commit
fix: re-run with one of:
  --mode chain        run ethos first, then existing hook
  --mode framework    add ethos to .pre-commit-config.yaml
  --mode replace      back up and replace the existing hook
```

Auto-detection of the right mode is a v1.1 candidate once we have
data on what users actually pick. v1 stays explicit.

`ethos capture install --check` reports the current install state
without modifying anything. `ethos capture uninstall` removes the
ethos hook and restores the most recent backup if one exists.

### Failure modes and exit codes

The hook follows POSIX conventions and the project's existing
shell-hook patterns (DES-016, DES-029, DES-030).

| Exit | Meaning | User-visible message (stderr) |
|---|---|---|
| `0` | Success — capture wrote the file(s) and staged them, OR was skipped intentionally (env var, no active session, etc.) | (silent on success; one-line "ethos capture: skipped (no active session)" on intentional skip) |
| `1` | Capture failed — bad input (malformed JSONL), write failure, scrubber error. Commit aborted. | `ethos capture: OP failed: REASON` plus a one-line `fix: HINT` |
| `2` | Misconfiguration — ethos binary present but capture not installed correctly, missing repo config | `ethos capture: misconfigured: REASON` plus `fix: run ethos capture install` |
| `3` | Internal error not attributable to user input | `ethos capture: internal error: REASON` plus `report: ISSUE-LINK` |

Exit codes 1, 2, 3 abort the commit. Exit code 0 lets the commit
proceed. The chained next hook only runs on a 0 exit.

Per the spike's failure-mode table, the inherent git "hook script
not executable" case is silent at the git layer and outside ethos's
control. Mitigation: the install command sets `+x` and `doctor`
flags any hook lacking `+x` as a warning.

### Discovery: session id and worker JSONLs

The hook receives no arguments from git; it must discover its
context. The discovery chain:

1. **Repo root** — walk up from cwd looking for `.git`. Already
   exists in `internal/resolve.FindRepoRoot`.
2. **Active session id** — Claude Code does not expose a
   "current session id" env var. The hook reads
   `~/.claude/projects/<encoded-cwd>/` and selects the most
   recently modified `.jsonl`. The encoded cwd is the project
   path with `/` replaced by `-`, matching the existing
   convention. If multiple JSONLs are recent (within a 5-minute
   window), all are captured (rare; covers the case of two
   parallel sessions in the same repo).
3. **Open missions in this repo** — read the global mission store
   (`~/.punt-labs/ethos/missions/`), filter to missions whose
   top-level `created_in_repo` field equals this repo root
   (set by the store at create time — see DES-053). For older
   contracts that pre-date the field, fall back to walking the
   mission event log and using the cwd of the first event entry.
   Each open mission produces one mission capture file.
4. **Worker JSONLs for a mission** — the mission event log
   records `agent_started` events with a session id (this is
   added in this design; see DES-053). Failing that, the hook
   walks `~/.claude/projects/<encoded-cwd>/` looking for JSONLs
   whose first record's `parent_uuid` is the leader's session id
   AND whose timestamp window overlaps the mission's
   `created_at..now`.

Discovery failure on (2) — no JSONL found — is a 0-exit skip. A
commit can legitimately happen outside an active Claude Code
session (a developer running `git commit` in a regular shell).
Discovery failure on (4) — a mission with no traceable worker
JSONLs — is a warning, not a hard failure: the mission capture
still emits with the contract, event log, and result, just without
worker transcripts. The front matter records this with an
`incomplete: true` field and a reason.

## CLI surface

```text
ethos capture install --mode chain|framework|replace [--yes] [--check]
ethos capture uninstall
ethos capture run [--hook pre-commit] [--dry-run] [--session <id>]
ethos capture private [--unmark]
ethos capture backfill [--since <date>] [--dry-run] [--commit]
ethos capture distill <input> [-o <output>] [--stats]
ethos capture scrub <input> [-o <output>] [--report]
```

`run` is what the hook invokes. It also runs from a shell, useful
for debugging (`ethos capture run --dry-run` prints what would be
written without writing).

`distill` and `scrub` are individual primitive surfaces, available
to humans, ad-hoc pipelines, and tests. Both accept either a file
path or `-` for `<input>` (stdin) and either `-o <file>` or
`-o -` for output (stdout). This matches the reference Python
scripts and the Unix convention; the pre-commit path always uses
files at known paths, but there is no reason to force that shape
on every consumer.

```text
# File in, file out (the hook's path):
ethos capture distill ~/.claude/projects/.../session.jsonl \
    -o .ethos/sessions/abc123.md

# Pipe composition (humans, ad-hoc indexing, tests):
cat session.jsonl | ethos capture distill - | ethos capture scrub - --report
```

`backfill` is the idempotent re-runner. See "Backfill semantics"
below.

`private` toggles a marker file at
`~/.punt-labs/ethos/sessions/<session-id>.private` (machine-local,
gitignored by virtue of being outside the repo). When the marker
is present, the hook skips that session's capture entirely.

## Library structure

The capture logic lives in three new internal packages, each small
and testable.

```text
internal/capture/
├── capture.go         # public API: Run(repo, opts) error
├── discover.go        # session/mission discovery
├── session.go         # session capture path
├── mission.go         # mission capture path
└── install.go         # hook installation logic

internal/distill/
├── distill.go         # JSONL → markdown
├── distill_test.go
└── testdata/          # fixture JSONLs

internal/scrub/
├── scrub.go           # markdown → scrubbed markdown
├── rules.go           # secret/profanity rule registry
├── scrub_test.go
└── testdata/
```

Reasons for this split:

- `capture` is the orchestrator. It owns I/O, paths, and git
  staging. It depends on `distill` and `scrub`.
- `distill` is pure — input bytes (JSONL) to output bytes
  (markdown). No filesystem, no git. It is the part that is
  worth porting from Python because it is the slow part.
- `scrub` is pure — input bytes (markdown) to output bytes
  (scrubbed markdown) plus a redaction count. No filesystem.

The `capture` package alone does I/O. This isolates the slow,
testable, deterministic core (distill+scrub) from the messy
runtime concerns (paths, git, hooks).

## Library vs. subprocess decision for distill and scrub

Three approaches considered. This is one of the load-bearing
architectural decisions.

### Option A: Subprocess (call the existing Python scripts)

The Go `capture` package shells out to `python3 jsonl-to-quarry.py`
and pipes through `python3 scrub-pre-ingest.py`.

**Pros.**

- Zero re-implementation cost — the proven scripts are reused
  verbatim.
- The Python tests (`test_jsonl_to_quarry.py`,
  `test_scrub_pre_ingest.py`) cover the behavior; we do not
  duplicate.
- Bug fixes to the scripts immediately reach ethos.

**Cons.**

- Adds a Python runtime dependency to ethos. Ethos chose Go for
  its 10 ms cold start (DES-003); shelling out to Python adds
  ~200 ms × 2 of Python startup per commit, before any work.
- Cross-machine portability suffers — every developer needs
  Python 3 on PATH and the scripts at a known location.
  Installer must place the scripts under
  `~/.punt-labs/ethos/scripts/` or similar, with version pinning.
- The scripts live in `punt-labs/.bin/`, a different repo from
  ethos. The ownership boundary is awkward — who owns bug fixes,
  versioning, releases?
- Pipe-based composition makes error handling fragile; a
  scrub failure is a `SIGPIPE` upstream that the Go orchestrator
  must observe correctly.

### Option B: Native Go port

The Go `internal/distill` and `internal/scrub` packages
re-implement the logic. The Python scripts continue to exist for
any non-ethos consumer but ethos does not invoke them.

**Pros.**

- One language, one runtime, one binary. No external dep.
- Cold start is single-digit milliseconds. The 1.24 s
  per-commit budget collapses toward 0.05 s + the cost of regex
  scanning. The spike's per-commit cost stops being a future
  optimization concern.
- The code lives in ethos. Versioning, testing, bug fixes are
  trivial; the same `make check` gate covers it.
- Go's `regexp` is RE2-based: linear time on input, no
  catastrophic backtracking risk on adversarial input.
  (Python's `re` is a backtracking engine; the existing
  scrubber's patterns are safe against the inputs we see, but
  the property is incidental, not guaranteed.)

**Cons.**

- The Python tests do not transfer. We must port test cases
  alongside the logic. Approximately 1500 lines of test code
  rewritten as `internal/distill/distill_test.go` and
  `internal/scrub/scrub_test.go`. Substantial up-front cost.
- Two implementations diverge over time unless one is
  deprecated. If the org keeps both, drift is the eventual
  outcome.
- `regexp/syntax` differences: Python's `re.MULTILINE` and the
  scrubber's careful `[ \t]` discipline must port carefully.
  Some Python patterns use features (e.g. lookaheads) that
  RE2 supports; some use features (negative-look-behind of
  variable width, conditional groups) that RE2 does not. The
  existing patterns appear to use only RE2-safe features but
  this needs an explicit audit.

### Option C: Hybrid — subprocess in v1, port in v2

Ship v1 with subprocess. Build out the operational surface
(installer, mission representation, backfill). When stable, port
to native Go in v2 and deprecate the subprocess path.

**Pros.**

- Fastest path to a shipping artifact.
- Real-world feedback shapes the port — we learn what edges
  matter before re-implementing.
- The subprocess scripts continue to serve their non-ethos
  audience.

**Cons.**

- Two distinct deliverables. Migration is its own project, with
  its own test plan and its own risk window.
- Users running v1 see the worst-of-both-worlds: Python runtime
  required, plus the long capture wall.
- "We will port it later" is a deferral; the org's Fix-It-Now
  principle says do it now or do not do it.

### Recommendation: Option B (Native Go port)

Port the distillation and scrubbing logic into ethos, in
`internal/distill` and `internal/scrub`. Reasons in order:

1. **Latency is product UX.** The 1.24 s per-commit cost is
   visible. Two of three options accept it; the port is the
   only one that makes it disappear. Spike data shows ~1.15 s
   is in the Python pipeline; a Go re-implementation cuts that
   to single-digit ms, an order-of-magnitude improvement.
2. **Single-binary distribution.** Ethos is a Go tool that
   prides itself on no runtime dependencies. Adding "you also
   need Python 3 + these scripts at this path" undoes that.
3. **Boundary clarity.** Capture is an ethos feature. Its code
   lives in ethos. The Python scripts continue to serve their
   original purpose (ad-hoc indexing, debugging) without being
   tangled into ethos's release lifecycle.
4. **Test discipline.** Porting tests is a forcing function for
   case coverage. The Python suite is a starting point, not a
   ceiling; we can write `_race_test.go` cases that the Python
   suite does not address.
5. **Determinism.** RE2's linear-time guarantee makes the
   scrubber safe under adversarial input. The Python `re`
   engine does not give us that.

The cost — ~1500 LoC of Go to write, ~1500 LoC of Go test code
to write — is real but bounded. Estimate: 2-3 mission rounds
for `internal/distill` and 1-2 mission rounds for
`internal/scrub`.

The Python scripts continue to live in `punt-labs/.bin/` for
non-ethos use. They are not deleted, not hard-deprecated; they
are simply no longer in the ethos commit-time path.

## Performance analysis

Grounded in the spike data, projected to the recommended
implementation.

### Current (Python-piped) numbers

From the spike on Apple M2, warm cache, 38 MB input JSONL → 2.9 MB
markdown:

| Component | Wall time |
|---|---|
| Python startup × 2 | ~200 ms |
| JSONL parse | ~unmeasured (subset of distill) |
| Distill (`jsonl-to-quarry.py`) | ~700 ms |
| Scrub (`scrub-pre-ingest.py`) | ~250 ms |
| Bare `git commit` overhead | ~50 ms |
| **Total per commit** | **~1.24 s** |

### Projected (native Go) numbers

A Go re-implementation hits two wins:

1. Eliminates Python startup (200 ms savings).
2. Linear-time regex on the 2.9 MB output markdown rather than
   line-by-line plus block re-scanning. Go `regexp` on 3 MB
   should be ≤50 ms.

Distill is dominated by JSON decoding and string assembly. Go's
`encoding/json` on 38 MB is ≈300-500 ms in the steady state. The
distill output assembly is mostly `strings.Builder` writes — fast.

Estimate: Go pre-commit cost on the spike's representative
session = 0.4-0.6 s wall. **2-3x faster** than Python-piped.

**v1 always rewrites the full capture file on every commit.** No
incremental distill, no append-mode, no caching of intermediate
state. The markdown is fully derivable from the source JSONLs and
the front-matter version fields alone. Reproducibility is the
v1 priority; latency tuning is deferred.

If the per-commit cost is felt as a problem after the Go port, the
optimization paths from the prep doc are available in this order:

1. **Incremental distill (v2 candidate).** Track the last-distilled
   record index per session. On the next commit, parse only new
   records and append to the markdown. This is the highest-ceiling
   optimization because most commits add a small number of records
   to a long-running session — re-parsing the full 38 MB on every
   commit is wasteful. Cost: a session-level state file; a markdown
   that is no longer derivable from the JSONL alone (front-matter
   would record the last record index for reproducibility); harder
   to keep idempotent across schema bumps. The user has confirmed
   this is v2 work, not v1.
2. **In-process daemon.** Long-lived ethos process holds hot
   state (compiled regexes, parsed JSONL). Hook becomes an RPC
   call. Cost: process lifecycle, IPC, the daemon problem.
   Defer.
3. **Streaming JSON parser.** `encoding/json/v2` or a hand-
   rolled scanner. Modest improvement over `encoding/json`.
   Defer until measured.
4. **Compile-once regex cache.** Trivial. Already implied by
   `regexp.MustCompile` at package init. Free.

Order rationale: incremental distill attacks the linear cost; the
daemon collapses the constant cost; the parser is a small
constant; regex caching is free and built in. None of these ship
in v1.

### Failure-mode performance

A failed capture must abort fast. Worst case is malformed JSONL
on line 200,000: the parser fails on that line and exits. Bound:
< 0.5 s. Worst-case write failure: O(disk-write) which on APFS is
~10 ms for 3 MB.

## Mission markdown representation

The mission has multiple inputs (contract, event log, results,
reflections, N worker JSONLs) that must combine into one cohesive
readable document. Below are three sketched layouts, applied to a
hypothetical 3-round mission that ran one round of investigation,
one round of implementation, and one round of fix.

### Layout A: Strict timeline (one stream, chronological)

Everything flattens into a single chronological narrative. Round
boundaries are horizontal rules with a heading. Worker transcript
sections are quoted with `>` prefixes; leader event-log entries
are inline.

```markdown
---
schema_version: 1
kind: mission
id: m-2026-04-25-001
mission_status: closed
generated_at: ...
...
---

# Mission: m-2026-04-25-001

## Contract

leader: claude
worker: bwk
...

## Timeline

### 2026-04-25T15:00 · created

Leader created mission. Worker spawned.

### 2026-04-25T15:02 · round 1 started · investigate

> ## User · 15:02
> Read the prep doc and report the hook timing decision.
>
> ## Assistant · 15:02
> ...

### 2026-04-25T15:18 · round 1 result

verdict: pass
...

### 2026-04-25T15:19 · round 1 reflection

continue: true
...

### 2026-04-25T15:21 · round 2 started · implement

...

### 2026-04-25T16:55 · closed · pass

```

**Strengths.** True chronological order. Single read path.
Reader walks the document top to bottom and gets the story.

**Weaknesses.** Worker transcripts are long (multiple thousand
lines per round). Quoting them with `>` makes them dense and
hard to read. Round 2's transcript visually drowns out the
leader's reflection, which is the load-bearing decision.
Renders poorly past 5-10 rounds.

### Layout B: Layered (leader narrative outer, worker transcripts inner)

The outer narrative is the leader's framing — events, decisions,
reflections, the contract. The worker's transcripts are
collapsible (`<details>`) sections, present but folded.

```markdown
---
...
---

# Mission: m-2026-04-25-001

## Contract

(contract YAML inline)

## Round 1 — investigate

**Worker:** bwk · **Started:** 2026-04-25T15:02

<details>
<summary>Worker transcript (24 KB · click to expand)</summary>

(full distilled+scrubbed worker transcript)

</details>

### Result

verdict: pass · confidence: 0.9
...

### Reflection

continue: true
...

## Round 2 — implement

...

## Closed: 2026-04-25T16:55 · pass
```

**Strengths.** Leader's narrative is foreground; worker
transcripts are present but do not crowd. `<details>` is
allowed by the markdownlint config (`MD033`). Renders well at
high round counts because the document length is bounded by the
leader's framing, not the worker's transcript volume. Easy to
scan.

**Weaknesses.** GitHub renders `<details>` collapsed by default
which is what we want, but raw text consumers (the unrendered
markdown, grep, quarry's chunker) see all the transcript content
inline. That is also what we want — the transcript is the recall
substrate. So this is not a real weakness, just a property to
note.

### Layout C: Round-major sections, transcripts inline, summary at top

Each round is a top-level `## Round N` section with the worker
transcript inline (not collapsed). A summary table at the top
gives a 30-second read of the whole mission.

```markdown
---
...
---

# Mission: m-2026-04-25-001

## Summary

| Round | Type | Worker | Verdict | Outcome |
|---|---|---|---|---|
| 1 | investigate | bwk | pass | continued |
| 2 | implement | bwk | pass | continued |
| 3 | fix | bwk | pass | closed |

Closed at 2026-04-25T16:55 with verdict `pass`.

## Contract

...

## Round 1: investigate

**Worker:** bwk · **Started:** 2026-04-25T15:02

(full transcript inline)

### Result

...

### Reflection

...

## Round 2: implement

(full transcript inline)

...

## Conclusion

(close event details)
```

**Strengths.** No HTML. Pure markdown that renders identically
across consumers. Summary table at top is the 30-second read.
Round-major structure is the intuitive mental model.

**Weaknesses.** Long missions produce long documents — a 30-round
mission with average 24 KB transcripts is ~720 KB of inline
markdown. Scrolling becomes the only navigation. No way to skim
"just the leader's reasoning."

### Recommendation: Layout B (layered, with `<details>` for transcripts)

Layout B is the right shape for the load-bearing reader: someone
asking "why this mission, and why this verdict?" The leader's
narrative is foreground. The worker's transcript is fully
present (so quarry indexes it, so grep finds it, so a deep
auditor can read it) but does not crowd the visual hierarchy.

Three reasons the choice tips to B:

1. **Most readers want the framing first.** "What was the
   contract, what did the worker conclude, what did the leader
   decide?" That is the 30-second read. Layout B gives it
   directly. Layouts A and C bury it under transcript volume.
2. **Quarry indexes content regardless of `<details>`.** The
   chunker reads raw markdown; it does not parse HTML. The
   transcript content is fully indexed and searchable in
   quarry. The `<details>` only affects rendered-by-GitHub
   display.
3. **The markdownlint config already permits `<details>` and
   `<summary>` (MD033 allowed-elements list).** No new
   tolerance is needed.

The trade — that one reader profile (the deep-auditor reading
top-to-bottom) sees a fold instead of inline content — is
acceptable: the deep auditor is reading the rendered HTML of a
GitHub blob and clicks the disclosure triangle, or is reading
the raw markdown text and gets the content inline anyway.

A worked mission file template using Layout B follows in
"Mission file template (recommended)" below.

### Mission file template (recommended)

The outer fence below uses four backticks so the embedded YAML
fences inside the template render correctly.

````markdown
---
schema_version: 1
kind: mission
id: m-2026-04-25-001
mission_id: m-2026-04-25-001
mission_status: closed
generated_at: 2026-04-25T16:55:00-07:00
generator: ethos@v3.6.0
distill_version: 1
scrub_version: 1
source_records: 1247
source_bytes: 38421120
output_bytes: 4127321
redactions:
  env-secret: 1
last_commit: SHA
---

# Mission: m-2026-04-25-001 — Conversation and Mission Capture Design

**Leader:** claude · **Worker:** bwk · **Evaluator:** claude

**Created:** 2026-04-25T15:00 · **Closed:** 2026-04-25T16:55 ·
**Verdict:** pass

## Contract

```yaml
leader: claude
worker: bwk
evaluator:
  handle: claude
write_set:
  - docs/use-case-conversation-mission-capture.md
  - docs/design-conversation-mission-capture.md
  - DESIGN.md
success_criteria:
  - ...
budget:
  rounds: 3
  reflection_after_each: true
```

## Round 1 — Design draft

**Started:** 2026-04-25T15:02 · **Completed:** 2026-04-25T16:00

<details>
<summary>Worker transcript (1247 records · 4.1 MB scrubbed)</summary>

(distilled and scrubbed worker session JSONL)

</details>

### Result

```yaml
verdict: pass
confidence: 0.85
files_changed:
  - path: docs/use-case-conversation-mission-capture.md
    added: 312
    removed: 0
```

### Reflection

```yaml
continue: true
signals:
  - draft complete; no review yet
recommendation: continue to round 2 for review
```

## Round 2 — Review fix

(same shape)

## Round 3 — Polish

(same shape)

## Close

```yaml
verdict: pass
final_files_changed: ...
```

````

The template is rendered from a Go template at capture time, not
hand-written.

## Backfill semantics and idempotence

`ethos capture backfill` exists to handle two real scenarios:

1. **Initial install.** A user installs ethos capture for the
   first time. They want existing JSONLs (sessions in
   `~/.claude/projects/` whose first record's cwd resolves to
   this repo, missions in the global store whose contract was
   created in this repo) captured into the repo retroactively.
2. **Capture-pipeline upgrade.** A new ethos version ships with
   a smarter scrubber category, a refined distiller, or a
   schema-version bump. Existing capture files are now stale.
   The user wants them regenerated.

### What "changed" means precisely

A capture file is "changed" — and must be regenerated — if any
of the following differ between the file at HEAD and what the
current `ethos capture` would produce:

1. `schema_version` in the file front matter differs from the
   current `ethos capture` schema version.
2. `distill_version` differs.
3. `scrub_version` differs.
4. The source JSONL (or YAMLs for missions) has a different
   content hash than the one the file was generated from. To
   support this, the front matter records `source_hash`
   (sha256 of the concatenated source bytes).
5. The front matter is missing entirely (legacy file).

Idempotence falls out of this definition: re-running
`ethos capture backfill` immediately after a successful run
produces zero changes because every dimension of "changed" is
identical.

A file at HEAD that *would not change* under regeneration is
left alone — `git status` after `ethos capture backfill` shows
no diff. This is the operational test of idempotence.

In v1, regeneration is always a full re-distill from the source
JSONLs — same path the pre-commit hook takes. There is no
incremental backfill. The "would change" check is the only
short-circuit; once a file is in scope to regenerate, it is
written from scratch.

### Backfill walk

```text
ethos capture backfill [--since DATE] [--dry-run] [--commit]

1. Discover candidates:
   - Sessions: every JSONL under ~/.claude/projects/<encoded-cwd>/
     whose first record's cwd is this repo.
   - Missions: every mission YAML under ~/.punt-labs/ethos/missions/
     whose top-level `created_in_repo` equals this repo (with
     event-log cwd fallback for older contracts) OR whose worker
     JSONL resolves to this repo.

2. For each candidate, compute "would change" per the rules above.

3. Print the list (--dry-run stops here).

4. Without --commit:
   - Write the changed files to the working tree.
   - User reviews `git diff`, stages, commits when ready.

5. With --commit:
   - Write the changed files.
   - Stage them.
   - Create a single commit `chore(capture): backfill <N> files`.
```

`--since DATE` filters to JSONLs whose first record is on or
after DATE. Useful for partial backfills.

### Backfill of branches that were never merged

Out of scope. Capture files belong on the same branches as the
code they document. Backfill operates on the current branch only.
A user wanting capture on a deleted branch must restore the
branch first.

### Backfill of pre-capture-system commits

The capture file is added to a single, current backfill commit
on a feature branch. It is *not* squashed back into the
historical commits that produced the source JSONLs. Squashing
historical commits would rewrite history, conflict with the
"never modify shared history" principle, and is too invasive
for a maintenance command.

The trade: a `git log -p test_file.go` from before the backfill
commit does not show the matching capture; it shows up only on
or after the backfill commit. This is acceptable because:

- The matching session JSONL was never in git in the first
  place.
- The audit chain can still resolve "session for code at commit
  C" by reading the capture file at the most recent commit
  containing the corresponding session id and walking back.
- Rewriting history in v1 is a much bigger commitment than
  shipping a one-shot backfill.

### Re-scrubber upgrade

When the scrubber adds a new category — say, a tighter pattern
catches a previously-missed token — `scrub_version` increments.
On the next backfill, every existing capture file is regenerated
with the new scrubber. Concretely: an old file with a
`scrub_version: 1` front matter is regenerated under
`scrub_version: 2`; the resulting markdown may have new
`[REDACTED:...]` markers in places the old version did not.

The implication: a previously-leaked secret in an old capture
file might still be visible in `git log` even after the new
scrubber lands, because git history is immutable. A future
companion command — `ethos capture rewrite-history` — could
attempt history rewrites for that case. Not in v1. The
mitigation in v1 is to surface this loud and clear in the
backfill report so the user knows the on-disk file is now
clean but the historical commits are not.

### Re-distiller upgrade

When the distiller changes — a new tool stub, a different
compression boundary — `distill_version` increments. Backfill
regenerates every capture file. Same mechanics as the scrubber
case.

### Mission documents and their constituent worker JSONLs

A mission capture's `source_hash` is computed over the
concatenation of: contract YAML, event log JSONL, each worker
JSONL in round order, each result YAML, the reflections YAML.
Any change to any of those (e.g. a late `mission close` event)
flips the hash and forces regeneration.

A worker session that participated in a mission is *also*
captured as a session in the worker's repo (when the worker is
the primary session) OR is captured only as part of the mission
(when the worker is a sub-agent in the leader's session). The
authoritative home is the mission file when the JSONL is a
sub-agent JSONL; the standalone session file is suppressed in
that case. The hook records this decision in the front matter:
`primary_record_in: mission|session`.

## Open questions: addressed or deferred

The mission contract enumerated seven open questions. Each is
addressed below or explicitly deferred with reasoning.

### 1. Mission markdown representation

**Recommendation:** Layout B (layered, `<details>` for worker
transcripts). See above for three sketched layouts and rationale.

### 2. Backfill semantics

**Recommendation:** Idempotence defined by 5 explicit
"would change" rules: schema_version, distill_version,
scrub_version, source_hash, missing front matter. Re-running
backfill with no upstream change writes zero files. See above.

### 3. Multi-repo missions

**Recommendation:** Mission capture lives in the *leader's repo*.
The leader's repo is the one that ran `ethos mission create`,
recorded in the contract's top-level server-controlled
`created_in_repo` field (added in this design — see DES-053).
The leader does not set the field; the store stamps it from cwd
at create time, the same shape as `mission_id` and `created_at`.
Worker commits in other repos get only conversation capture
(UC-1), not mission capture. Cross-references between repos use
the mission ID in commit trailers (`Mission: m-2026-04-25-001`).

Rejected alternative: capture file in every repo a mission
touches. Reason: duplication. Mission state is one canonical
artifact; copies are drift sources.

Rejected alternative: capture file in the team submodule
(`.punt-labs/ethos/missions/`). Reason: the submodule is for
team registry, not work artifacts. Conflating the two would
require contributors to push to the team repo every time they
work, which is the wrong access pattern.

### 4. Privacy

**Recommendation:** Session-level `private` mark via
`ethos capture private`. When marked, the session produces no
capture file at all. Mark file lives at
`~/.punt-labs/ethos/sessions/<session-id>.private` (machine-
local). UC-7 covers the user flow.

**Deferred:** Per-tool-result redaction. The current scrubber
operates on the rendered markdown via regex. A "do not capture
this Read of /etc/foo" mechanism would require Claude Code-side
metadata that does not exist today. Defer to v2.

**Deferred:** Encryption at rest. Current artifacts are
plaintext. Repos using ethos capture inherit the same secrecy
envelope as the rest of the repo. If an encrypted capture is
needed (a regulated environment), `git-crypt`-style tools
already handle it without any ethos involvement.

### 5. Session-id and worker-JSONL discovery

**Recommendation:** Discovery chain documented in "Discovery"
above:

- Session id: most recently modified `.jsonl` in
  `~/.claude/projects/<encoded-cwd>/`.
- Worker JSONL: from `agent_started` events in the mission
  event log (new — DES-053), with fallback to a parent_uuid
  walk.

Risk: Claude Code may someday change the JSONL location or
naming convention. Mitigation: the discovery logic lives in
`internal/capture/discover.go` and is unit-tested with fixture
JSONLs. A change in Claude Code's layout updates one file.

### 6. v1 scope: conversations vs. missions

**Recommendation:** Ship both, in one v1, behind a feature flag
that is off by default for the first release.

```text
ethos capture install --enable-sessions   # session capture only
ethos capture install --enable-missions   # mission capture only
ethos capture install                      # both (default)
```

Rationale: the two flows share so much code (distill, scrub,
front-matter writing, hook installation) that splitting them
across two releases is more work than shipping both. The flag
exists because users upgrading from no-capture to full-capture
benefit from a staged rollout — try sessions for a week, then
add missions.

Sub-recommendation: the *initial public release* defaults to
sessions-only. Missions go to opt-in for one minor version (e.g.
v3.6 sessions-default, v3.7 missions-default). Rationale:
session capture is the simpler shape; if there is a
hook-discovery bug we want it to bite the smaller surface
first.

### 7. Performance optimization paths

**Recommendation:** No optimizations in v1 beyond the native Go
port itself. Measure post-port. If 0.5 s per commit is felt as
a problem, the optimization order is: incremental distill →
in-process daemon → finer JSON parser → regex tuning.

Rationale: the prep doc and spike both make the same point —
the right time to optimize is when the cost is felt, not before.
The Go port already takes the cost from 1.24 s to ~0.5 s.
Further gains have diminishing user-perceived value relative to
the engineering cost.

## Risks

### Risk: Discovery fragility

The session JSONL discovery depends on Claude Code's filesystem
convention. If that convention changes, capture breaks silently
on the next Claude Code release. Mitigation: a discovery
self-test in `ethos doctor` that verifies a JSONL is discoverable
for the current session. Failure is a warning, not a hard
failure.

### Risk: Pre-commit hook drag becomes the reason ethos is uninstalled

Even at 0.5 s per commit, a developer who commits 50 times in a
day spends 25 s waiting on capture. If the pain exceeds the
benefit, they will set `ETHOS_CAPTURE=skip` permanently. Then
capture is performative.

Mitigation: be honest in docs about the cost. Provide
`ETHOS_CAPTURE=skip` as a first-class escape hatch, not a hidden
flag. Track post-install retention via doctor (count of capture
files in the last 30 days vs. count of commits) and surface to
the user if retention is dropping.

### Risk: Scrubber misses a real secret

The scrubber's regex categories are a known finite set. A novel
token shape passes through. Once it is in git, it is in
`git log` forever (unless the user does a history rewrite).

Mitigation: keep the scrubber rules close to the latest secret
patterns (rule registry in `internal/scrub/rules.go` is an
explicit allowlist; we add categories as they are discovered).
The privacy escape hatch (UC-7) is the user's emergency stop.
A `git pre-commit` warning when a capture file is large or
contains shapes the scrubber suspects but does not fully match
might be worth shipping in v2. Defer.

### Risk: The Go regex port silently changes scrubber behavior

A Python `re` pattern that worked correctly on real input might
behave subtly differently when ported to Go's `regexp`.
Specifically, RE2 does not support: variable-width
look-behind, conditional groups, possessive quantifiers,
backreferences. The existing scrubber's patterns appear not to
use any of those, but the port must include a category-by-
category fixture test that proves bit-identical output on a
representative corpus.

Mitigation: an `internal/scrub/_compat_test.go` that runs the
Python and Go scrubbers on a fixed corpus and asserts identical
output. Lives in v1.

### Risk: Capture file size in a long-lived mono-session

A single Claude Code session that runs all day produces a 100+
MB JSONL. The distilled markdown is still ~10 MB. Replacing a
10 MB file on every commit is ~50 commits × 10 MB = 500 MB of
git object churn per day per developer. Pack files keep this
manageable but it is a real concern.

Mitigation: incremental distill is the deferred v2 optimization
that collapses this — only new records are processed and the
file diff is small. v1 ships the full-rewrite path; we accept
the churn for now and revisit if real-world use shows it as a
problem. Plus: most real sessions are minutes to hours,
not all day.

## Open questions deferred to a future round

- Cross-repo synchronous worker commit behavior. The mission
  trailer (`Mission: m-<id>`) is sufficient for cross-
  referencing; the leader's repo holds the canonical mission
  file and is the only repo that stages it. An integration
  test exercising two workers committing simultaneously in
  different repos belongs in implementation, not design.
  Deferred to the implementation phase.
- How to surface a capture file's existence in the Claude Code
  UI. Out of scope for ethos; a Claude Code feature request
  if needed.

## Dependencies

- `internal/mission` for mission discovery, contract loading,
  event log reading. Already exists.
- `internal/resolve.FindRepoRoot` for repo discovery. Already
  exists.
- A new top-level server-controlled `created_in_repo` field on
  the mission contract (DES-053). Set by `Store.ApplyServerFields`
  from cwd at create time. Source-compatible with the current
  contract; strict-decoder safe (additive); optional on the wire,
  defaulted at create time when empty.
- A new `agent_started` event type in the mission event log
  (DES-053). Additive; existing logs without it are valid.
- No external runtime dependencies; the Go port is self-
  contained.
- `markdownlint` config already permits `<details>` and
  `<summary>` via `MD033 allowed_elements`.

## References

- Use case: `docs/use-case-conversation-mission-capture.md`
- Hook timing spike:
  `research/research-2026-04-25-hook-timing-spike.md`
- Persistence prep:
  `research/research-2026-04-25-persistence-and-audit-prep.md`
- Reference primitives:
  `punt-labs/.bin/jsonl-to-quarry.py`,
  `punt-labs/.bin/scrub-pre-ingest.py`
- DES-031 (mission contract), DES-037 (event log reader),
  DES-050 (mission traceability JSONL)
- New ADRs added in DESIGN.md by this design:
  DES-052 (capture overview), DES-053 (mission file location +
  source discovery), DES-054 (native Go port choice),
  DES-055 (mission markdown layout)

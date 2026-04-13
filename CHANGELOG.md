# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Built-in pipelines are directly runnable** -- the 8 built-in pipeline
  templates (quick, standard, full, product, formal, docs, coe, coverage)
  now ship with `{feature}` and `{target}` variable conventions and
  sensible `write_set` / `success_criteria` / `context` defaults. Running
  `ethos mission pipeline instantiate <name> --var feature=X --var target=Y`
  produces valid mission contracts without needing to copy and customize
  the template.
- **`ethos mission pipeline instantiate`** -- generate N mission contracts
  from a pipeline template. `--var key=value` for template expansion,
  `--leader`/`--evaluator`/`--worker` flags, `--id` to override the
  auto-generated pipeline ID, `--dry-run` to preview without creating.
  Closes the gap between pipeline templates (v3.3.0) and executable
  pipeline-driven sprints.
- **Contract `pipeline` and `depends_on` fields** -- missions that
  belong to a pipeline carry its ID and reference upstream stage
  mission IDs. Enables filtered listing and dependency ordering.
- **`ethos mission list --pipeline <id>`** -- filter missions by
  pipeline membership; results returned in topological order (stages
  after their `depends_on` dependencies).

### Changed

- **README** -- rewritten for approachability. Three-gap intro (identity,
  delegation, integration), shorter quick start, linked guides for team
  setup and archetypes/pipelines.
- **New guide**: `docs/archetypes-and-pipelines.md` -- user-facing how-to
  covering built-in archetypes and pipelines, customization, and the
  pipeline selection heuristic.

## [3.3.0] - 2026-04-13

### Added

- **5 new pipeline templates** -- product (PR/FAQ before engineering), formal
  (Z-Spec before implementation), docs (documentation-only), coe (cause of
  error investigation), coverage (targeted test improvement).
- **Pipeline decision tree** -- H10 lint heuristic rewritten from size-based to
  nature-based selection. Checks context keywords and write_set patterns before
  falling back to size.

### Changed

- **standard pipeline** gains `document` stage (5 stages, was 4)
- **full pipeline** gains `prfaq`, `spec`, `coverage`, `document` stages (9
  stages, was 6)

## [3.2.0] - 2026-04-12

### Added

- **Mission archetypes** — typed subtypes for missions. 7 archetypes shipped
  as seed content (design, implement, test, review + inbox, task, report). Each
  has budget defaults, write-set constraints, and required fields. Extensible
  via YAML files.
- **Mission pipelines** — chained typed stages as workflow. 3 sprint templates
  (quick, standard, full). `ethos mission pipeline list/show` with `--json`.
- **Design mission archetype** in mission `SKILL.md` with cross-repo
  collaboration via biff.
- **3 design mission lint heuristics** (H7-H9): cross-repo context,
  before/after criteria, evaluator domain.
- **Pipeline selector lint** (H10): suggests quick/standard/full based on
  contract characteristics.
- **Contract `Type` field** — missions declare their archetype, defaults to
  "implement".

### Docs

- Ethos-biff integration design (`docs/ethos-biff-integration.md`)
- Mission archetypes design (`docs/mission-archetypes.md`)
- Mission pipelines design (`docs/mission-pipelines.md`)

## [3.1.0] - 2026-04-12

### Added

- **`ethos mission lint`** — advisory pre-delegation linter with 6
  heuristic checks (adjacent test file, CHANGELOG gap, README in
  criteria, inverted test gap, inputs.files not in write_set, placeholder
  evaluator). Exits 0 with or without warnings. Supports `--json`.
- **Walked-diff for verifier isolation** — `WalkWriteSet` resolves
  static write_set paths to concrete files on disk. Verifier isolation
  block now includes a "Concrete files on disk" section.
- **PreToolUse file allowlist enforcement** — hook handler blocks
  verifier Read/Write/Edit/Glob/Grep calls against paths outside the
  mission write_set. Communicated via `ETHOS_VERIFIER_ALLOWLIST` env var.
- **L4 behavioral test infrastructure** — three-layer test harness
  (deterministic, LLM-judged, adversarial) with 8 scenarios. Daily CI
  via `.github/workflows/behavioral.yml`.
- **L1-L3 test infrastructure** — content validation CI binary, CLI
  subprocess tests, MCP integration tests via stdio client.
- **`-coverprofile` in `make test`** and CI coverage summary step.

### Changed

- **All CLI handlers use RunE** (DES-042) — handlers return errors
  instead of calling `os.Exit`. `silentError` type for already-reported
  failures. `writeJSON` alongside `printJSON`.
- **`validate-content` added to CI test workflow** — was in `make check`
  locally but missing from GitHub Actions.

### Fixed

- **ext commands resolve repo-local identities** (DES-044) — `ext
  set/get/del/list` now use layered identity store. Extensions still
  write to global `.ext/` directory.

## [3.0.0] - 2026-04-10

### Docs

- **AGENTS.md audit fixes (`ethos-gpc`)** — corrected wrong `whoami`
  CLI example (should be `iam`), added missing `mission` tool to MCP
  table (9 tools -> 10), added hidden-command shortcut note, fixed
  MCP server naming in AGENTS.md (`self` vs `ethos`).
- **Phase 4 status block drift fixed (`ethos-zve`)** — Three stale
  "Designed, not yet implemented" / "not yet active" references in
  `docs/agent-identity-spec.tex` and `docs/agent-definitions.md`
  are updated to reflect that Phase 4 role-based hooks shipped in
  `ethos-9ai.2` (PR #190) and were updated by `ethos-b5g` (PR #195).
  The spec's §Phase 4 now reads "Shipped --- Unreleased, PR #190"
  with a sentence describing the `PostToolUse` hook for
  write-enabled roles; §Phase 5 no longer claims role-based hooks
  are inactive. The `agent-definitions.md` frontmatter table row
  for `hooks` changes from "Manual / future: Role-based" to
  "Generated from `role.tools`". Same-class fix: the stale `head -60`
  reference in the historical `ethos-9ai.2` changelog entry qualifier
  is updated to `head -n 60` to match the current command.
  Identified by silent-failure hunter M2 during `ethos-b5g` round 1
  and deferred out of that scope to keep b5g focused.
- **`DES-038`: worktree isolation investigation (`ethos-56a`)** —
  Documents the investigation that closed `ethos-56a` after 8+
  failed worker rounds misused `isolation: worktree`. The flag
  creates a worktree on a new `worktree-agent-<id>` branch
  (scratch-and-merge isolation), NOT on the leader's current
  feature branch (same-branch isolation). The COO's protocol now
  defaults OFF for single-worker feature delivery and reserves
  the flag for exploratory/parallel-fan-out/snapshot work only.
  No code changes — ethos has no worktree-creation logic to
  modify; the Claude Code behavior is working as designed.
- **Phase 2.6 completion status corrected (`ethos-gpc`)** — README,
  roadmap, and architecture.tex claimed Phase 2 fully shipped or
  omitted the Phase B–C remainder. Phase 2.5 (`/mission` skill
  Phase A, PR #186) shipped; Phase 2.6 (`/mission` Phase B–C:
  conflict detection and dry-run) remains planned. All three
  documents now reflect this.

### Changed

- **Mission escalate workflow documented in CLI help (`ethos-cqt`,
  `ethos-2qz`)** — `ethos mission result --help` now shows an escalate
  example with empty `files_changed` and populated `open_questions` for
  mid-round scope issues. `ethos mission create --help` documents the
  write_set-as-envelope convention: `inputs.files` is the read-context
  list, `write_set` is the permission envelope. `ethos mission --help`
  links to the escalate path. README mission tutorial includes a note.
- **Mission write commands echo on success (`ethos-30c`)** — The
  five mission write subcommands (`create`, `result`, `reflect`,
  `advance`, `close`) previously exited with status 0 and zero
  stdout, forcing every scripting caller to chase a follow-up
  `ethos mission show` or `list` to verify the operation landed.
  Each command now emits a one-line `verb: <id> k=v...` summary
  on stdout in text mode (JSON mode is unchanged): `created: <id>
  worker=... evaluator=...`, `result: <id> round=N verdict=...`,
  `reflected: <id> round=N rec=...`, `advanced: <id> round N -> M`,
  `closed: <id> round=N verdict=... status=...`. The `verb: <id>` prefix is grep-able
  and the `k=v` tail mirrors the `summarizeEventDetails` audit-log
  shape so the CLI echo and the event log read alike. Surfaced
  during the Phase 3 mission-primitive dogfood on PR #199 (`ethos-vjp`).
- **`FormatLocalTime` renders year and timezone (`ethos-vjp`)** —
  `hook.FormatLocalTime` now formats timestamps as
  `2006-01-02 15:04 MST` (year, month, day, 24h time, zone
  abbreviation when available; numeric offset such as `+0530`
  otherwise) instead of `Mon Jan _2 15:04`
  (weekday, month, day, 24h time, no year, no zone). Mission logs
  are a post-mortem tool;
  two operators in different timezones now identify the same event
  without ambiguity. Every call site picks up the new shape:
  `mission show`, `mission list`, `mission log`, and `session`
  output. The invalid-timestamp fallback (return raw string) and
  empty-string behavior are preserved.
- **Writer-role `output_format` templates aligned with
  `mission.Result` strict schema (`ethos-r6o`)** — the
  `implementer.yaml` and `test-engineer.yaml` sidecar role templates
  previously described an informal `RESULT: <task-id>` block with a
  `worker:` field, inline `+N -M` files_changed strings, and inline
  `pass | fail` evidence strings. Phase 3.1 landed the mission
  contract primitive (7274f0b) and Phase 3.x will wire the mission
  result submit path through `mission.DecodeResultStrict` +
  `Result.Validate()`; a worker who copy-pasted the old template into
  a `.results.yaml` file would have hit `KnownFields(true)` errors
  for `worker`, type errors for inline-string `files_changed`, and
  missing-required errors for `mission`, `round`, and `author`
  (`created_at` is optional — `Result.Validate()` parses it when
  set and `Store.AppendResult` default-fills it when empty). Both
  templates are now raw YAML bodies whose keys and
  value types match `mission.Result` exactly — `mission`, `round`,
  `created_at`, `author`, `verdict`, `confidence`, `files_changed`
  (list of `{path, added, removed}` maps), `evidence` (list of
  `{name, status}` maps), optional `open_questions`. Placeholder
  values are concrete enough to parse: a real mission ID
  (`m-2026-04-09-001`), a real RFC3339 timestamp, a valid verdict
  enum, a numeric confidence, and relative file paths that pass
  `validateWriteSetEntry`. A two-line instructional comment at the
  top of each body (`# Fill in the placeholder values below, then
  submit as your mission result.` plus a strict-decoder caveat)
  preserves the "fill me in" cue without breaking YAML parsing —
  comments are accepted (and silently dropped) by
  `DecodeResultStrict`. A new
  `internal/seed/output_format_schema_test.go` round-trip test loads
  each template through the embedded `Roles` FS, runs the body
  through `DecodeResultStrict` + `Validate`, and asserts both pass;
  a negative case injects `worker: test` into the implementer body
  and asserts strict decode rejects it with a message naming the
  unknown field. The tripwire is CI-enforced: a future edit that
  reverts to `worker:` or any other unknown key fails here with a
  clear diagnostic instead of shipping to workers and failing at
  submit time. Reviewer-style roles (`reviewer`, `architect`,
  `security-reviewer`) and `researcher` are deliberately out of
  scope — their FINDINGS and RESEARCH output shapes describe
  different workflows with different verdict enums
  (`{approve, iterate, reject}`) and different sub-field layouts;
  aligning them with `Result` would semantically distort them. The
  generator (`internal/hook/generate_agents.go`) is untouched — the
  existing `TrimSpace` guard at `buildAgentFile:386-390` renders the
  new raw-YAML body verbatim under the generator-owned
  `## Output Format` heading. No agent in the engineering team
  currently uses either role, so the null-effect verification shows
  zero md5 changes across `.claude/agents/*.md` after SessionStart
  regeneration; the template change lands invisibly until a future
  team member adopts `implementer` or `test-engineer` as their role.
  Round 2 of `ethos-r6o` closes four same-class drift findings from
  the pre-alignment Result shape. (1) `docs/ETHOS-ROADMAP.md` §3.6
  shipped a canonical Result example using the old `lines_added` /
  `lines_removed` / `test:` / `duration_ms:` field names under a
  top-level `result:` wrapper; the example is rewritten to match the
  real `mission.Result` shape (`added`/`removed`, `name`/`status`,
  flat top-level keys) with distinct placeholder values so a reader
  copying from the roadmap produces YAML that `DecodeResultStrict`
  accepts. (2) `internal/role/store_test.go`'s
  `TestStore_OutputFormatRoundTrip` carried a `RESULT: <task-id>`
  block with a `worker:` field as its body; replaced with opaque
  multi-line text that exercises YAML encoding fidelity (colons,
  indentation, code fences, pipes) without implying any canonical
  schema, since the store does not parse OutputFormat. (3) The
  negative-case assertion in
  `internal/seed/output_format_schema_test.go` was
  `strings.Contains(msg, "worker") || strings.Contains(msg, "field")`
  — the `"field"` disjunct was wide enough that any unrelated
  missing-required error would satisfy it; tightened to
  `strings.Contains(msg, "field worker not found")`, the exact
  yaml.v3 strict-decode format already asserted by three other tests
  in `internal/mission/`. (4) Both `implementer.yaml` and
  `test-engineer.yaml` shipped `open_questions: ["omit this key
  entirely if you have no unresolved questions"]` — instructional
  text that parses as a valid entry; a worker who submitted the
  template verbatim would ship the instruction as a real open
  question. The `open_questions` key is removed from both template
  bodies (`omitempty` on the struct makes its absence valid) and
  the guidance is folded into the leading YAML comment block.

- **Role-based `PostToolUse` hook command changed from `tail -20`
  to `head -n 60`** (`ethos-b5g`) — the 9ai.2 hook ran
  `(cd "$CLAUDE_PROJECT_DIR" && make check) 2>&1 | tail -20`
  (originally-shipped form, superseded by this entry) after every
  Write or Edit by a write-enabled role. `make check` is a sequence
  of quiet-on-success stages (go vet, staticcheck, shellcheck,
  markdownlint, then non-verbose `go test -race -count=1`), and
  every stage is silent until something fails. Go compile errors
  short-circuit the sequence at the top in 5-30 lines; a failing
  lint or test stage also surfaces at the top because every
  preceding stage was silent. `tail -20` caught the compile-error
  case (broken-build output is <20 lines) but missed the first
  failing test — non-verbose `go test` still prints one line per
  package on success before the failing package's `--- FAIL:`
  block, and with 13 packages the FAIL block can land past the
  20-line tail window. `head -n 60` catches both cases: compile
  errors at the top and the first-failing-stage output within 60
  lines. The command now reads
  `(cd "$CLAUDE_PROJECT_DIR" && make check) 2>&1 | head -n 60`
  — `-n 60` is the POSIX-canonical form; the BSD legacy shortcut
  `-60` is avoided to match the POSIX-sh pin on the rest of the
  command. Hook stays advisory, not blocking — the pipe to `head`
  still masks the exit code, so Claude Code does not gate the next
  Write on a broken build. This is the intentional shape: blocking
  the next Write would create user-hostile friction during
  refactors where intermediate states are knowingly broken, and
  Claude Code has no `--skip-hook` escape hatch. The canonical
  example and language in `docs/agent-identity-spec.tex`
  §Role-Based Hooks are updated in sync: "behavioral enforcement"
  softens to "visible enforcement" and a POSIX-sh pin note
  documents that the command runs under `/bin/sh` and must avoid
  bashisms like `set -o pipefail` and process substitution.
  Write-enabled agent files (bwk, mdm, adb, rmh) regenerate with
  the new command on the next SessionStart; review-only agent
  files (djb) are unchanged because they have no hooks block.
  **No user action required — regeneration is automatic on the
  next SessionStart.**
- **`ethos resolve-agent` now exits 1 on config read/parse errors**
  (`ethos-dc0`) — previously exited 0 with the error on stderr, which
  made the exit code meaningless for shell scripts gating on it.
  Post-fix, a malformed `.punt-labs/ethos.yaml` or a permission error
  produces a non-zero exit. Scripts that invoke `ethos resolve-agent`
  in a pipeline under `set -e` should either use `|| true` to preserve
  the old fail-open behavior (matching `hooks/session-start.sh`) or
  check the exit code explicitly.
- **`mission show --json` and MCP `mission show` payload shape**
  (Phase 3.6 round 3, `ethos-07m.10`) — both surfaces now serialize
  a new `mission.ShowPayload` struct that embeds `*Contract` and adds
  sibling `results` (always an array, never `null`) and optional
  `warnings` (omitempty) fields. This replaces the round-2 hand-rolled
  `map[string]any` that silently dropped the Contract's `session` and
  `repo` fields and unconditionally emitted every `omitempty`-tagged
  field on open missions. Any consumer decoding directly into
  `mission.Contract` still works — the embedded pointer serializes
  the Contract fields identically, and `omitempty` is honored. A
  consumer that was decoding the round-2 map into a custom struct
  with an enumerated field list will now see `session`, `repo`, and
  the omitted empty fields correctly. The new `warnings` field
  surfaces corrupt sibling-file failures that previously returned
  `"results": []` indistinguishable from "no result submitted", so
  scripted MCP callers gain a signal the CLI-only stderr channel did
  not carry. The `mission show` hook formatter
  (`internal/hook/format_output.go`) now renders a `Results:` section
  (and an empty-state `(none)` marker) and a `Warnings:` section when
  non-empty — previously the agent-facing MCP rendering dropped the
  results field entirely. Round 3 also makes the `mission show` CLI
  empty-state render `Results: (none)` so an operator running `show`
  on a fresh mission sees the section exists and is empty; adds a
  paragraph to `ethos mission close --help` documenting the result
  gate and the remediation path; and unifies the error-wrapper style
  in `validateFileChange` and Contract `validate` so both read
  `field: <cause>`. Round 4 (mdm finding N1) removes a stale early
  `return` in `runMissionShow` so a corrupt `.results.yaml` still
  renders the Results header and `(none)` marker on stdout — an
  operator piping `ethos mission show <id> 2>/dev/null | less` no
  longer loses the section entirely, and the stderr warning still
  carries the load failure. Round 5 (Copilot finding) closes the
  on-disk trust symmetry gap in `decodeResultsFile`: the write path
  (`AppendResult`) has always refused a result whose self-declared
  `mission` field disagrees with the target mission, but until now
  the read path did not. An attacker with local write access to
  `~/.punt-labs/ethos/missions/<id>.results.yaml` could hand-edit
  the file to contain a forged result claiming a different mission,
  and the close gate would accept it as long as the round number
  matched. The decoder now rejects any `results[i].mission` that
  does not equal the target, naming both IDs in the error —
  symmetric with the Phase 3.1 round-3 `KnownFields(true)` fix.
  Round 6 (Bugbot finding) closes the parallel miss of the round-4
  Results fix on the Reflections side of `runMissionShow`: a corrupt
  `.reflections.yaml` now still renders the `Reflections:` header
  and `(none)` marker on stdout, with the load failure on stderr,
  matching the symmetric Results behavior. `printReflections` on
  empty input now emits the section header and `(none)` marker
  instead of returning silently — parallel to the round-3 E1 fix
  for `printResults`.

### Added

- **SessionStart working context (`ethos-gcq.1`)** — `HandleSessionStart`
  now emits a `## Working Context` section with git branch, uncommitted
  change count and file paths (capped at 20), and unpushed commit count.
  Advisory only — returns empty on non-git directories or git failures.
- **Role-based safety constraints (`ethos-gcq.2`)** — Roles can declare
  `safety_constraints` (tool + message pairs) that appear as a
  `## Safety Constraints` section in generated agent files.
- **Session audit logging (`ethos-gcq.3`)** — PostToolUse hook appends
  one JSONL line per tool invocation to a per-session audit log at
  `~/.punt-labs/ethos/sessions/<id>.audit.jsonl`.
- **`ethos mission result --verify` cross-checks declared counts
  against `git diff --numstat` (`ethos-2e4`)** — adds an optional
  `--verify` flag (with a companion `--base` flag that defaults to
  `main`) to `ethos mission result` that runs
  `git diff --numstat <base>..HEAD` before the result lands and
  rejects the submission when any declared `files_changed` entry is
  absent from the diff or carries counts that disagree with git's
  numstat output; the error names the path and both count pairs.
  A path present in the diff but not declared in `files_changed`
  emits a warning to stderr and does not reject — workers may
  legitimately omit auto-generated files from their accounting. The
  flag is off by default; existing `mission result` behavior is
  byte-for-byte unchanged when `--verify` is not set. The verify
  code path lives in `cmd/ethos/mission.go` (CLI surface), not in
  `internal/mission/`, because the mission package is the trust
  boundary for persisted artifacts and git is a consumer-side
  convenience. Surfaced during Phase 3 dogfood on PR #199.
- **`Role.OutputFormat` field for structured agent handoff
  (`ethos-9ai.3`)** — `internal/role/role.go` gains an optional
  `OutputFormat string` field with YAML tag `output_format,omitempty`
  and matching JSON tag. When set, `internal/hook/generate_agents.go`
  emits a new `## Output Format` section as the LAST block of the
  generated `.claude/agents/<handle>.md` body — after the `Talents:`
  line, after `## What You Don't Do`, after every other section. The
  role provides only the body; the generator owns the heading. Empty
  field omits the section entirely (no empty heading, no trailing
  blank line). The field is free-form markdown with no validation —
  same trust boundary as `responsibilities` and the other string
  fields, since role YAML is git-tracked and human-reviewed. All six
  starter sidecar roles in `internal/seed/sidecar/roles/*.yaml` ship
  with templates tailored to their category: writer roles
  (`implementer`, `test-engineer`) get a `RESULT`-shaped template
  with `files_changed` and `evidence`; review roles (`reviewer`,
  `architect`, `security-reviewer`) get a `FINDINGS`-shaped template
  with `severity` and `location`; the `researcher` role gets a
  `RESEARCH`-shaped template with `sources` and `options`. The
  schema change is purely additive — existing role YAML without the
  field loads identically to before. To add `output_format` to a
  custom role, edit the role YAML directly and set the field using a
  YAML literal block scalar (`output_format: |`); see
  `internal/seed/sidecar/roles/implementer.yaml` for an example. The
  Punt Labs team roles in `.punt-labs/ethos/roles/*.yaml` deliberately
  do NOT set the field in this bead (a follow-up against
  `punt-labs/team` will populate them), so this PR is null-effect on
  the five Punt Labs team agent files regenerated by the session-start
  hook in this repo; users who run `ethos seed` and regenerate agents
  from the sidecar roles will see the new section.

- **Generated agent files include a `PostToolUse` hook that runs
  `make check` after every Write or Edit (`ethos-9ai.2`)** —
  `internal/hook/generate_agents.go` injects a `hooks:` block in the
  frontmatter of every generated `.claude/agents/<handle>.md` whose
  role `tools` list contains `Write` or `Edit`. The block is fixed
  shape with no per-role customization: a single `PostToolUse` entry
  matching `Write|Edit` and running the command
  `(cd "$CLAUDE_PROJECT_DIR" && make check) 2>&1 | tail -20` (the
  originally-shipped form; superseded by `ethos-b5g` which changed
  the window to `head -n 60` — see the `### Changed` section above). The
  command pins cwd to the project root via `$CLAUDE_PROJECT_DIR`
  (exposed to hook commands by Claude Code) so `make check` never
  fails with "No rule to make target" when the sub-agent has cd'd
  into a subdirectory. Claude Code loads the block when it spawns
  the sub-agent, so vet/lint/test failures surface at the point of
  the write instead of three rounds later. Review-only roles whose
  tools list excludes both `Write` and `Edit` (e.g., the
  `security-engineer` role used by djb with `Read, Grep, Glob, Bash`)
  emit no `hooks:` block at all — their tool restrictions already
  prevent the matcher from firing. Detection is exact-string
  membership: no case folding, no substring matching, no inference
  from related names like `MultiEdit` or `WriteFile`. The hooks block
  sits between the existing `skills:` block and the closing `---`
  delimiter. No schema change — `Role` gains no new field; the
  existing `tools` list is the signal.

- **Generated agent files include a `## What You Don't Do` section
  derived from `reports_to` edges in the team graph (`ethos-9ai.1`)** —
  for each `reports_to` edge from an agent's role to a target role,
  the target's `responsibilities` are listed under "What You Don't Do" with
  parenthetical attribution: `- release management (coo)`. The
  preamble names the reporting target(s) with Oxford comma joining:
  `You report to coo.`, `You report to coo and ceo.`, or
  `You report to coo, ceo, and cto.`. Roles with no `reports_to` edges
  get no section. Responsibility strings are normalized (whitespace
  trimmed, embedded newlines collapsed, empty entries skipped). Missing
  target roles and non-`reports_to` edges from the agent's role log a
  stderr warning at generation time. Only `reports_to` is honored;
  `collaborates_with` and `delegates_to` are future work. This round
  also tightens blank-line discipline in the generated body — every
  `##` heading now has a blank line below it, and every list has a
  blank line between its last bullet and the following `Talents:` line.

- **Generated agent files now include a `skills:` block listing
  `baseline-ops` in frontmatter (`ethos-9ai.4`)** —
  `internal/hook/generate_agents.go` unconditionally emits a
  `skills:` block with `baseline-ops` as the sole entry, placed
  after the optional `model:` line and before the closing `---`
  delimiter:

  ```yaml
  skills:
    - baseline-ops
  ```

  When Claude Code spawns a sub-agent from one of these files, it
  reads the frontmatter and loads
  `~/.claude/skills/baseline-ops/SKILL.md` (deployed by `ethos seed`
  since `ethos-l9d`) into the sub-agent's context, so every generated
  sub-agent inherits operational discipline — dedicated tool usage,
  verification after changes, no commits, scope discipline, security
  basics, progress tracking, concise output — without the project
  author having to remember to add the skill manually to each agent
  file. The injection is unconditional: no opt-out, no per-role
  override, no configuration. A future bead can add per-role skill
  lists to the `Role` struct if a use case emerges, but the MVP is
  the unconditional default.
- **`/mission` skill MVP (Phase A, `ethos-9ai.5`)** — a new
  `~/.claude/skills/mission/SKILL.md` is deployed by `ethos seed`
  alongside the existing `baseline-ops/SKILL.md`. The skill teaches
  Claude how to scaffold a Phase 3 mission contract from
  conversation context, register it via `ethos mission create
  --file <path>`, and spawn the worker with `Agent(subagent_type,
  prompt, run_in_background=true)`. Every field in the scaffolded
  YAML maps to the typed `mission.Contract` schema (DES-031); the
  skill walks through the six-step flow (resolve worker, scaffold
  YAML, pick evaluator, create, spawn, track) and includes a
  marquee worked example. This closes the last gap in Phase 3:
  the runtime (write-set admission, frozen evaluator, bounded
  rounds, result artifacts, event log) was enforced at the store
  boundary, but there was no user-facing interface to drive it.
  `docs/mission-skill-design.md` is rewritten to match the
  Phase 3 schema (the prior version predated Phase 3.1 and
  described a freeform contract format that never shipped).
  Phases B (slash command) and C (write-set conflict detection,
  bead integration) remain PLANNED.

- **Phase 3.7: append-only mission event log reader API**
  (`ethos-07m.11`, rounds 1–3) — a public `Store.LoadEvents(missionID)`
  method, a new `ethos mission log <id>` CLI subcommand, and a new
  MCP `mission log` method expose the JSONL event audit trail every
  Phase 3.1+ writer has been quietly appending to. The reader is
  additive: the writer (`appendEvent`, `appendEventLocked`, every
  existing caller) is unchanged. `LoadEvents` returns `([]Event,
  []string, error)` — events in on-disk order, warnings naming any
  unparseable line numbers, error for unrecoverable I/O failures.
  One corrupt line does not erase the log: each line is decoded
  independently with `DisallowUnknownFields` (trust-boundary
  symmetric with the reflection and result loaders), and a failing
  line produces a warning while the rest of the file still decodes.
  Round 2 hardening (four reviewers, 4 HIGH + 5 MEDIUM + 4 LOW):
  the line scanner uses `bufio.Reader.ReadString` instead of
  `bufio.Scanner` so a single line larger than any fixed cap no
  longer silently truncates the tail of the log (H1); warnings are
  sanitized at source via a new `sanitizeWarning` helper so an
  attacker with local write access cannot forward terminal control
  sequences through decode-error strings to operator terminals or
  MCP consumers (H2); a non-RFC3339 `ts` is rejected at decode time
  with a warning rather than silently dropped at `--since` filter
  time, closing a count mismatch between the same audit trail read
  with and without `--since` (H3); `LoadEvents` adds an
  `os.Stat(contractPath)` existence check before any log read so a
  bogus mission ID errors symmetrically with `LoadReflections` and
  `LoadResults` (H4); the log file is stat-checked before read and
  rejected if larger than 16 MiB or if a directory sits at the
  expected path (M3, M4); the CLI `printEventLog` now emits a
  leading `-` bullet prefix matching the MCP walker and sibling
  subcommands (M1); warnings render as an in-band `Warnings:`
  footer on stdout in human mode so an operator piping the output
  to a file still sees damage (M2); the long `--help` text
  documents the wrapped `{"events": [...], "warnings": [...]}`
  JSON shape and the empty-`--event` semantics (M5, L4); the
  `--since` error carries a human-readable RFC3339 hint without
  leaking the Go time reference layout (L1); the symlink test is
  renamed to flag the known weakness and cross-references bead
  `ethos-jjm` for the follow-up that hardens all four loaders
  together (L3); and the two `parseEventTypes`/`parseEventTypeList`
  helpers carry cross-reference comments pinning the intentional
  13-line duplication (K1). Two new equivalence classes (27
  oversized-line, 28 unparseable-ts) join the round 1 26-class
  test table. Missing file and empty file both still return
  `[]Event{}, nil, nil`, matching `LoadResults` convention. The
  CLI subcommand accepts
  `--json` for a `mission.LogPayload` wire shape (events slice plus
  omitempty warnings), `--event <type,list>` for event-type
  filtering, and `--since <RFC3339>` for time filtering; both
  filters are AND-composed. Unknown event type strings are
  accepted (event types are forward-compatible — future phases may
  add `worker_spawned`, `round_started`, etc. without a reader
  change). The new DES-020 `formatMissionLog` walker in
  `internal/hook/format_output.go` renders the events list for the
  MCP hook surface with one bullet per event plus a Warnings
  section on partial decode. Mission identity is enforced via the
  file path (the `Event` schema has no top-level mission_id field —
  `logPath` runs the ID through `filepath.Base` as defense in
  depth); a caller-supplied `mission` key inside the free-form
  `Details` map is opaque payload, not identity, so the reader
  preserves it untouched. There is no public writer path: DES-031
  round 3 unexported the writer as a deadlock footgun, and 3.7
  does not re-introduce one. Round 3 review-cycle polish (1 MEDIUM +
  4 LOW findings carried from round 2's own fix work): the
  `runMissionLog` godoc (`cmd/ethos/mission.go`) no longer claims
  warnings go to stderr — the comment now matches the M2 fix that
  routes them to the stdout footer (R3-M1); `FilterEvents` converts
  its silent `continue` on an unparseable in-memory ts into a loud
  error (`event N has unparseable ts "..."`) so a future caller
  constructing `Event` values directly and bypassing the decoder
  gets a programmatic signal instead of a shorter-than-expected
  result slice (R3-M2); `LoadEvents` opens the log file once and
  reads through `io.LimitReader(f, maxLogSize+1)` with a post-read
  length check for the overflow byte, closing the TOCTOU window
  where a concurrent writer could grow the file past the 16 MiB
  cap between the old `os.Stat` and `os.ReadFile` pair (R3-L1);
  `LoadEvents` now rejects a malformed `missionID` at the API
  boundary via `missionIDPattern.MatchString` before any stat or
  open, so raw attacker-controlled bytes never reach a downstream
  `*fs.PathError` string that the CLI or MCP walker forwards to
  operator terminals (R3-L2) — this aligns the reader with
  `Store.Create` and the other sibling write APIs and tightens
  `TestLoadEvents_TraversalIDCannotEscape` to assert the new
  upfront refusal rather than the old collapse-and-succeed path;
  and the reader-error wrap inside `decodeEventLog` now routes the
  error string through `sanitizeWarning` as belt-and-suspenders
  for the file-handle reader introduced by R3-L1 (R3-L3). Three
  new round 3 tests:
  `TestFilterEvents_InMemoryBadTSReturnsError`,
  `TestLoadEvents_RejectsMalformedMissionID`, and
  `TestLoadEvents_GrowsPastCapDuringRead`. Round 4 (two Bugbot
  findings on PR #184, both LOW): `FilterEvents` now collapses a
  non-nil-but-empty `typeSet` back to `nil` after trimming the
  caller's `types` slice, so a whitespace-only filter input (e.g.
  `[]string{"  "}` or `[]string{"", "\t"}`) behaves as "no type
  filter" — matching the godoc's "empty types slice or nil means
  all types" contract instead of silently dropping every event
  (B1, sibling to the round 3 R3-M2 silent-drop closure);
  `formatMissionLog` in `internal/hook/format_output.go` renames
  its inner `summary` variable to `detailSummary` so it no longer
  shadows the outer panel-title `summary`, removing a maintenance
  tripwire for any future reader (B2). One new test,
  `TestFilterEvents_WhitespaceOnlyTypesActsAsNoFilter`, covers
  three whitespace-only input variants plus the empty-slice
  regression. Round 5 (Copilot finding, LOW, defensive): the
  non-EOF read-error path in `decodeEventLog` now derives the
  attempted line number from whether `bufio.Reader.ReadString`
  handed back a partial line along with the error. Before the fix,
  a partial-line + non-EOF error unconditionally reported
  `line lineNo+1: reading: ...`, but `lineNo` had already been
  bumped above for that same partial line — off by one on the
  exact byte the reader stumbled over. This is dead code against
  the production `bytes.NewReader` path (which only returns
  `io.EOF`), but a future caller wiring a file-backed reader would
  have seen mis-attributed post-mortem warnings. The helper
  `decodeEventLogFromReader(io.Reader)` is split out from
  `decodeEventLog([]byte)` so a test-only `scriptedReader` can
  inject the non-EOF branch; the byte-slice entry point keeps its
  empty-input fast path. One new test,
  `TestDecodeEventLog_NonEOFReadErrorReportsAttemptedLine`, pins
  both the partial-line case (load-bearing) and the empty-line
  regression. Round 6 (two Copilot findings on PR #184, both
  trivial): the `runMissionLog` comment that explained the round 2
  M2 stdout-footer fix no longer reads `silent-failure silent-
  failure-hunter` — the duplicated phrase collapsed the noun
  (`silent failure`) into the reviewer agent name
  (`silent-failure-hunter`) and broke reading flow (C1); and the
  `ethos-07m.11` bead description in `.beads/issues.jsonl` no
  longer claims the log path ends in `.log` with an aspirational
  event list (`worker_spawned`, `round_started`, `round_ended`,
  `reflection_recorded`, `evaluator_spawned`, `evaluator_finished`,
  `mission_closed`) that DES-037 and the Phase 3.1 writer
  explicitly deferred — it now names the actual `.jsonl` sibling
  file and the actual emitted event set (`create`, `update`,
  `close`, `reflect`, `verify`, `result`) phase by phase, and
  credits Phase 3.7 with the reader API rather than the CLI alone
  (C2). No code changes beyond the comment edit; no test changes.
- **Structured result artifacts and close gate** (Phase 3.6,
  `ethos-07m.10`) — worker output is no longer prose. A new
  `mission.Result` type in `internal/mission/result.go` pins a
  closed schema (`mission`, `round`, `verdict`, `confidence`,
  `files_changed`, `evidence`, `open_questions`, `prose`) with
  strict `KnownFields(true)` decoding, full validation (verdict
  enum, confidence in `[0.0, 1.0]` excluding NaN, evidence
  non-empty, path containment, control-character rejection), and
  append-only sibling storage at `<id>.results.yaml`. `Store.Close`
  now gates every terminal transition (`closed`, `failed`,
  `escalated`) on a valid result artifact for the mission's current
  round; the refusal message names the mission, the round, and the
  submission command, and the close event in the JSONL log records
  the satisfying result's `round` and `verdict` so auditors can
  reconstruct the gate decision without scanning back. The gate
  lives at the store boundary so CLI and MCP fire it identically —
  there is no override flag. New CLI subcommands `ethos mission
  result <id> --file <path>` (write) and `ethos mission results
  <id>` (read) and new MCP methods `mission result` and `mission
  results` mirror the existing reflect/reflections surfaces; `ethos
  mission show` and the MCP `mission show` method carry the result
  log in their output payload so the operator sees the verdict
  without `cat`-ing the sibling YAML. `files_changed` paths are
  cross-checked against the contract's `write_set` via a new
  asymmetric segment-prefix helper `pathContainedBy` — a result
  cannot claim a parent directory of a write_set file entry, and
  the same equivalence class of malformed paths (absolute,
  traversal, control characters, drive letters, root claims,
  parent-prefix) is rejected at both admission and result
  submission. Author fields are normalized via `strings.TrimSpace`
  in `AppendResult` and `AppendReflection` so whitespace does not
  pollute the audit trail or event log. `Store.List` is taught
  about the new `.results.yaml` sibling in `isContractFile`,
  closing the Phase 3.4 round-2 regression surface for the new
  file. The Phase 3.1–3.5 primitives are untouched: schema,
  conflict check, frozen-evaluator hash, round-advance gate, and
  verifier isolation are all preserved. To recover from the
  close-gate refusal, submit a result via `ethos mission result
  <id> --file <path>` (or the MCP `mission result` method); to
  recover from a files_changed-containment refusal, edit the
  result YAML so every declared path lives under an entry of the
  mission's `write_set`, or update the contract via a new mission
  if the write_set needs widening.
- **Verifier isolation** (Phase 3.5, `ethos-07m.9`) — two new runtime
  gates layered on top of the Phase 3.1–3.4 primitives. At
  `Store.Create`, the contract is refused if `worker` and
  `evaluator.handle` resolve to the same handle, OR if a wired
  `RoleLister` reports that the worker and evaluator share a
  team-scoped role binding (`team/role`) or a role slug after
  canonicalization. Both refusal modes name both handles, the
  conflicting binding(s), and the recovery path. The role-overlap
  check is opt-in via the new `Store.WithRoleLister` method; the CLI
  and MCP wire it from the same live identity, role, and team stores
  the frozen-evaluator hash uses. At `SubagentStart`, when the
  spawning subagent matches the evaluator handle of any open mission,
  the hook REPLACES the normal persona/extension injection with a
  Phase 3.5 isolation block: the mission contract YAML
  byte-for-byte from disk, the success criteria pinned at launch,
  and a file allowlist derived from the mission's `write_set` plus
  the contract file path. The isolation block carries an explicit
  directive that the verifier must NOT read the worker's scratch
  state, the parent transcript, or any file outside the allowlist.
  Non-verifier spawns and spawns whose only matching missions are
  closed are unchanged — the isolation gate fires only for open
  missions whose evaluator handle matches the spawning agent. The
  Phase 3.1/3.2/3.3/3.4 primitives are untouched: schema, conflict
  check, frozen-evaluator hash, round-advance gate, and reflection
  storage are all preserved exactly. To recover from a role-overlap
  refusal, either name a different evaluator handle or rebind one
  of the two identities to a different role via
  `ethos team add-member` (the gate compares `team/role` bindings
  after canonicalization). To recover from a worker-equals-evaluator
  refusal, assign a distinct reviewer.
- **Bounded rounds with mandatory reflection** (Phase 3.4,
  `ethos-07m.8`) — `Contract` gains a `current_round` field, and a
  new `Reflection` type plus the `Store.AdvanceRound` gate make
  `Budget.Rounds` enforceable. Reflections are typed (round, author,
  converging, signals, recommendation enum, reason) and append-only:
  `Store.AppendReflection` refuses duplicate rounds, refuses
  reflections whose round does not match the contract's
  `current_round`, and refuses reflections on closed missions. The
  round-advance gate refuses to bump `current_round` when (a) the
  current round has no reflection on disk, (b) the reflection
  recommends `stop` or `escalate` (the leader's `reason` is surfaced
  verbatim), or (c) advancing would exceed `Budget.Rounds`.
  Reflections live alongside the contract in a sibling
  `<id>.reflections.yaml` file so the contract itself stays pure.
  Three new `mission` subcommands wire this to operators —
  `ethos mission reflect <id> --file <path>` to record a reflection,
  `ethos mission advance <id>` to bump the round, and
  `ethos mission reflections <id>` to dump the round-by-round log.
  The `mission` MCP tool gains matching `reflect`, `advance`, and
  `reflections` methods, each with a formatter in
  `internal/hook/format_output.go` (DES-020). The new
  `round_advanced` and `reflect` events are appended to the
  per-mission JSONL log so the round lifecycle is auditable. The
  Phase 3.1/3.2/3.3 primitives are untouched: existing `Validate`
  rules still hold, the cross-mission write_set conflict scan still
  runs, and the frozen-evaluator hash and verifier-spawn gate are
  unchanged. To unblock a `stop`/`escalate` refusal or a
  budget-exhausted refusal, close the mission via
  `ethos mission close <id>` (optionally `--status failed` or
  `--status escalated`) and create a replacement contract with a
  revised scope or budget.
- **Frozen evaluator with content hash pinning** (Phase 3.3,
  `ethos-07m.7`) — `Store.ApplyServerFields` now resolves the
  evaluator handle through the live identity, role, and team stores
  and pins a sha256 of the resolved content (DES-033) into
  `Contract.Evaluator.Hash`. The hash covers personality, writing
  style, talents (in declaration order), and every (team, role)
  assignment for the evaluator (sorted lexicographically). Resolution
  failure is fatal: a mission whose evaluator cannot be loaded fails
  create with no on-disk artifacts. The SubagentStart hook
  (`HandleSubagentStartWithDeps`) recomputes the hash on every
  verifier spawn and refuses spawns whose pinned hash disagrees with
  the current content. The mismatch error aggregates every drifted
  open mission into a single multi-line block, showing the pinned
  and current rollup hash prefixes alongside a per-section breakdown
  (personality, writing_style, each talent, each role) so the
  operator can see which source file they edited. To recover from a
  drift failure, revert the edit to the evaluator's identity content
  or close and relaunch the mission(s) with the new content. Pre-3.3
  missions with empty `Evaluator.Hash` are allowed through with a
  stderr warning so the upgrade path remains clean.
  Closed/failed/escalated missions are out of the gate's purview.
  New `internal/mission/hash.go` defines the deterministic hash
  function, the `IdentityLoader`/`RoleLister` interfaces, and the
  `EvaluatorHashBreakdown` struct returned by
  `ComputeEvaluatorHashBreakdown`; `NewLiveHashSources` adapts the
  live stores. CLI and MCP create paths build the same `HashSources`
  so contracts launched from either surface produce identical hashes.
- **Write-set admission control** (Phase 3.2, `ethos-07m.6`) —
  `Store.Create` rejects a new mission whose `write_set` overlaps any
  currently-open mission's `write_set`. Overlap is segment-prefix on
  cleaned paths: `internal/foo` blocks `internal/foo/bar.go` but
  `internal/foo` does NOT block `internal/foobar`. Closed, failed,
  and escalated missions are out of the registry. A new
  directory-level create lock (`<missionsDir>/.create.lock`)
  serializes the conflict scan and the write so two concurrent
  Creates with disjoint mission IDs cannot both pass the scan and
  both write. The error names every blocking mission by ID, worker,
  and overlapping path — one line per blocker. CLI exit code is 1;
  MCP returns a structured tool error. To unblock, close the named
  mission or re-scope the new contract's write_set to disjoint paths.
- `mission` subcommand for creating, listing, and closing mission
  contracts — the typed delegation artifact that is the foundation
  of Phase 3 workflow primitives (`ethos-07m.5`). Creation is
  file-only (`--file <path>`); strict YAML decode with unknown-field
  rejection. Non-JSON mode is silent on success; `--json` emits the
  persisted contract.
- `mission` MCP tool with methods create, show, list, close. Full
  formatter in `internal/hook/format_output.go` (DES-020) sharing a
  single `text/tabwriter` layout with the CLI so the two surfaces
  cannot drift.
- `internal/mission/` package: Contract schema, filesystem store with
  flock-protected concurrency, shallow-copy Update semantics (failed
  Update leaves the caller's struct untouched), strict KnownFields
  YAML decode on every read path (Load and Close's loadLocked) so
  attacker-dropped fields cannot slip past the on-disk trust
  boundary, daily ID generator with `[1, 999]` counter bounds that
  reject exhaustion and poisoned counter files, append-only JSONL
  event log, and validation enforcing mission ID format, RFC3339
  timestamps, control-character rejection in identity handles and
  write_set entries, path-traversal and null-byte rejection,
  round budget bounds, and required fields. The Evaluator's
  `pinned_at` is server-controlled at create time on every entry
  point (CLI and MCP).
- `internal/mission/filter.go` — shared `StatusMatches` helper used by
  both the CLI and the MCP list handlers.

### Fixed

- **`team.Store.Load` silently accepted structurally-invalid team
  YAML** (`ethos-2z2`) — the loader unmarshaled a team file and
  returned it without calling `Validate`, so a hand-edited team
  with a typo'd `collaboration.from` (e.g. `go-speciallist` instead
  of `go-specialist`) silently matched zero members at derivation
  time and produced no warning. The 9ai.1 round 2 fix added a
  narrow typo check for `collaboration.Type` in the anti-
  responsibility generator, but the symmetric check for typo'd
  `From` values was deferred to this bead because the right fix is
  at the store layer, not the consumer. `team.Validate` is now
  split into `ValidateStructural` (pure function: slug rules,
  member and collaboration invariants — no cross-package lookups)
  and the full `Validate` (calls `ValidateStructural` first, then
  runs the identity and role existence callbacks). `Store.Load`
  calls `ValidateStructural` after `yaml.Unmarshal` and wraps any
  error as `validating team %q: %w`. Every previously-valid team
  YAML still loads unchanged; only files that were silently-
  malformed now fail loudly. The anti-responsibility generator's
  `c.Type != "reports_to"` branch is kept because it still fires
  for valid-but-deferred types (`collaborates_with`, `delegates_to`),
  which are a semantic-level "not handled by MVP" decision — but
  the branch can no longer fire for typo'd or unknown types,
  because `ValidateStructural` rejects those at Load. Three hook
  test fixtures that relied on the silent-accept behavior were
  fixed by adding a dummy team member to fill the previously-
  unfilled collaboration role. **Operational note**: a single
  corrupt team YAML now fails mission-hash computation across all
  evaluators (because `internal/mission/hash.go` walks every team);
  this is fail-closed by design per DES-033 (silent-hash-bypass is
  the worse failure mode) — the remediation is to fix or remove the
  broken team file, not to delete the blocked mission.
- **`GenerateAgentFiles` swallowed `LoadRepoConfig` errors**
  (`ethos-9ai.6`) — the SessionStart hook silently returned nil for
  any non-nil error from `resolve.LoadRepoConfig`, so a malformed
  `.punt-labs/ethos.yaml`, a permission-denied read, or any other
  I/O error produced no signal. `LoadRepoConfig` already distinguishes
  "file not found" (returns `nil, nil`) from real errors, so the
  caller now propagates every non-nil error as
  `fmt.Errorf("generate agents: %w", err)` and the "unconfigured
  repo" case stays silent via the existing `cfg == nil` branch.
- **`GenerateAgentFiles` reported success on partial failure**
  (`ethos-9ai.7`) — the partial-failure check
  `expected > 0 && generated == 0` only caught the total-failure
  case, so a team where 5 of 10 agents failed at mkdir or WriteFile
  returned nil. The check is now `generated < expected` and the
  error message names both counts and the failed members:
  `generated %d of %d expected agent files (failed: bwk, mdm)`.
- **`HandleSessionStart` discarded `GenerateAgentFiles` errors**
  (`ethos-9ai.6`, `ethos-9ai.7`, round 2) — the two fixes above
  closed the bug at the library boundary, but the only production
  caller (`internal/hook/session_start.go`) logged the returned
  error to stderr and continued, so `ethos hook session-start`
  still exited 0 on a broken config and the end-to-end silent-
  failure class stayed open. `HandleSessionStart` now propagates
  the error up as `fmt.Errorf("generating agents: %w", err)`. The
  shell wrapper's `|| true` still means Claude Code session startup
  is fail-open (correct per `cli.md` §Hook Architecture), but
  direct CLI invocation now surfaces a non-zero exit code for
  `ethos doctor` and manual debugging. Known gap: `resolve.ResolveAgent`
  has the same silent-swallow pattern at `internal/resolve/resolve.go:170-174`
  and swallows malformed-config errors before `HandleSessionStart`
  reaches `GenerateAgentFiles`. Filed as `ethos-dc0` for a parallel
  fix.
- **`resolve.ResolveAgent` and `resolve.ResolveTeam` swallowed
  `LoadRepoConfig` errors** (`ethos-dc0`) — the 9ai.6 r2 fix closed the
  silent-swallow class at the `GenerateAgentFiles` → `HandleSessionStart`
  boundary, but a broken `.punt-labs/ethos.yaml` never reached that
  boundary: both resolve functions logged to stderr and returned `""`,
  and `HandleSessionStart`'s `if agentPersona == ""` early-return fell
  back to the human one-liner before the `GenerateAgentFiles` call site
  was executed. Users with a malformed config saw a one-line "Ethos
  session started. Active identity: ..." in the Claude Code session
  and nothing else — the full error chain (`yaml: line ...: did not
  find expected ...`) was hidden on stderr, which Claude Code does not
  surface. Both functions now have signature `(string, error)` and
  return the wrapped error — `"resolve agent: %w"` and
  `"resolve team: %w"` (operation-noun form) — while preserving the
  `("", nil)` contract for the legitimate no-repo (`repoRoot == ""`)
  and not-configured (`cfg == nil`) cases. Callers handle the error
  per their operational role: `HandleSessionStart` fail-closes
  (matches the 9ai.6 r2 C1 pattern, wraps the inner error as
  `"resolving agent: %w"` — gerund outer, operation-noun inner, the
  same distinct-verbs convention 9ai.6 uses for `generate agents`
  vs `generating agents` — and returns up the stack);
  `BuildTeamSection` fail-opens with a stderr log (its documented
  contract is "Returns empty string ... on any load error");
  `runResolveAgent` in `cmd/ethos/main.go` prints to stderr and
  exits 1 so `ethos doctor` and manual debugging surface the
  failure; `CheckDefaultAgent` in `internal/doctor/doctor.go`
  returns `err.Error(), false` as a diagnostic state (no `"error: "`
  prefix — doctor's FAIL status column already signals the failure).
  The shell wrapper's
  `|| true` still keeps Claude Code session startup fail-open at the
  process boundary (per `cli.md` §Hook Architecture); the new
  fail-closed binary behavior is the signal for direct CLI invocation.
  Closes the "known gap" note on the 9ai.6+9ai.7 r2 entry above, and
  the 9ai.6 r2 regression test
  (`TestHandleSessionStart_GenerateAgentsErrorPropagates`) gains a
  companion test (`TestHandleSessionStart_ResolveAgentErrorPropagates`)
  that now exercises the malformed-yaml path the original spec
  intended but had to substitute with a missing-team fixture.

## [2.8.0] - 2026-04-04

### Fixed

- **Linux hook stdin hang (DES-029)** — all 6 hook shell scripts hung silently on Linux because Claude Code spawns hooks via `/bin/sh -c`, making `/dev/stdin` inaccessible to the Go binary. Shell scripts now read stdin with `IFS= read -r -t 1` and forward via `printf | binary` over a fresh pipe. Works on both Linux and macOS.
- **Go stdin fallback for non-pollable fds** — `readWithTimeout` in `internal/hook/stdin.go` uses a single `f.Read` (not `io.ReadAll`) when `SetReadDeadline` fails on Linux inherited pipe fds. Defense-in-depth behind the shell-level fix.

### Added

- **Fake Claude spawn regression test** — `TestShellScript_SessionStart` reproduces Claude Code's exact hook execution path using Node.js `spawn(shell: true)`. Fails if any hook script uses `< /dev/stdin`. CI requires `actions/setup-node`.
- **Subprocess integration tests** — 6 tests spawning the real ethos binary for each hook event (SessionStart, PreCompact, SubagentStart, SubagentStop, SessionEnd, open-pipe hang).
- **Linux process tests** — 14 tests for `/proc` filesystem parsing: comm truncation, spaces/parentheses in comm, version-named binary normalization, symlink behavior.
- **DES-029** — ADR documenting the root cause chain: Node.js `spawn(shell: true)` → `/bin/sh -c` → `/dev/stdin` inaccessible on Linux.
- **DES-030** — ADR documenting the subprocess integration test strategy and why in-process `os.Pipe()` tests gave false confidence.

## [2.7.0] - 2026-04-04

### Added

- **Baseline operational skill** — `internal/seed/sidecar/skills/baseline-ops/SKILL.md` provides operational discipline (tool usage, verification, scope, security) for sub-agents that lose the default system prompt
- **6 starter roles** — implementer, reviewer, researcher, architect, security-reviewer, test-engineer with tools, responsibilities, and model preferences; available in `internal/seed/sidecar/roles/` for teams to reference or copy
- **10 starter talents** — go, python, typescript, security, code-review, testing, cli-design, api-design, documentation, devops; substantial domain expertise (200-800 lines each) available in `internal/seed/sidecar/talents/` for teams to reference or copy
- **`model` field on Role** — roles can specify a preferred Claude model (opus, sonnet, haiku, inherit); `GenerateAgentFiles` includes it in agent frontmatter; validated against allowlist on save and load
- **Agent definitions guide** — `docs/agent-definitions.md` covering separation of concerns, anti-responsibilities, tool restrictions, baseline ops, output contracts, scope enforcement, context hygiene, common anti-patterns
- **Team setup guide** — `docs/team-setup.md` for third-party users creating teams from scratch
- **Mission skill design** — `docs/mission-skill-design.md` specifying the `/mission` structured delegation skill
- **ETHOS-ROADMAP.md** — 5-phase roadmap with 24 work items across batteries included, production agents, workflow, operational excellence, and ecosystem
- **Persona/role/mission three-layer model** — documented in agent-definitions.md and README as the core thesis for effective agents
- DES-027 (Teams/Roles as first-class concepts) and DES-028 (Persona animation) ADRs
- **`ethos seed` command** — deploys embedded starter roles, talents, skills, and READMEs to `~/.punt-labs/ethos/` and `~/.claude/skills/`; uses `go:embed` so content is available on all install paths; `--force` to overwrite existing files
- **Installer auto-seeds** — `install.sh` calls `ethos seed` after plugin install to deploy starter content automatically

### Changed

- **architecture.tex** — full rewrite from v0.3.3 to v2.6.1 (18 sections, 1388 lines covering all 11 packages)
- **AGENTS.md** — added team/role MCP tools, extension session context, rewrote stale Identity Resolution and Storage Layout sections
- **CLAUDE.md** — added 4 missing packages and 4 missing storage rows to architecture tables
- **README.md** — added Setup, Documentation, and three-layer model sections; added extension session context to features
- DES-015 status updated from PROPOSED to SETTLED

### Fixed

- **CHANGELOG.md** — Unreleased link corrected from v0.3.4 to v2.6.1; added 20 missing version comparison links
- **persona-animation.md** — version v2.2.2 corrected to v2.3.0; updated for DES-022
- **agent-teams.md** — version v2.2.2 corrected to v2.3.0; removed stale ps reference
- **agent-identity-spec.tex** — updated to v2.6.1; SubagentStart extension gap documented as shipped; staleness check updated to content comparison
- **Sidecar READMEs** — skills→talents in 3 files; path references updated to slugs

## [2.6.1] - 2026-04-01

### Fixed

- `SubagentStart` hook now injects extension `session_context` (quarry memory, vox voice, etc.) into sub-agent context

## [2.6.0] - 2026-03-29

### Added

- SessionStart hook generates `.claude/agents/<handle>.md` from ethos identity, personality, writing-style, and role data — agent definitions stay in sync with ethos source automatically
- `tools` field on role YAML schema — source of truth for sub-agent tool restrictions

## [2.5.0] - 2026-03-29

### Added

- `repo` and `host` fields on session roster — populated at session creation from git remote and hostname
- `joined` timestamp on each participant — set when joining via `iam` or `join`
- `session list` shows REPO column
- `session show` shows JOINED column and repo/host in header

### Changed

- `session list`: short session IDs (8 chars), human-readable timestamps (`Sun Mar 29 14:22`)
- `session show` replaces `session roster` as canonical verb (`roster` kept as hidden alias)
- `session show`: accepts session ID argument (full or prefix), infers role (root/primary/teammate)
- `session iam`: requires `--session` when no Claude Code process tree found

### Removed

- ACTIVE column from `ethos identity list`, MCP identity list, and hook output — only showed local session state, missed identities active on other hosts

## [2.4.1] - 2026-03-28

## [2.4.0] - 2026-03-28

## [2.3.0] - 2026-03-28

### Fixed

- **Agent team PID discovery**: `FindClaudePID()` failed for agent team teammates because Claude Code's version-named binary (e.g., `~/.local/share/claude/versions/2.1.86`) has a version string as its filename. PID discovery on macOS (`kern.procargs2`) and Linux (`/proc/<pid>/exe`) now normalizes versioned Claude binaries to the `claude` comm. Teammates now get working ethos sessions with full persona injection.

### Changed

- **PreCompact hook**: emit full persona block + team context instead of condensed 4-line summary — personality, writing style, role, team members with responsibilities, and collaboration graph all survive context compaction
- **PreCompact handler**: refactored to accept `PreCompactDeps` struct with identity, session, team, and role stores
- **PreCompact formatting**: deduplicate opening sentence from personality section, strip redundant top-level headings, skip bullet-only/indented content in sentence extraction
- **Team context**: include writing style summary and talent slugs for each team member
- **CLAUDE.md**: added delegation discipline, collaboration model (Agent Teams + Biff), and workflow tiers (T1/T2/T3)

### Added

- `BuildTeamContext` function — assembles team context block with member names, roles, responsibilities, and collaborations
- Repo config `team:` field — links `.punt-labs/ethos.yaml` to a team definition for automatic team context in hooks
- `skipFirstParagraph`, `stripLeadingHeading`, `isNonProse` helpers for clean markdown processing in PreCompact output
- `docs/agent-teams.md` — comprehensive documentation of Claude Code agent teams: process model, communication, task list format, team config, hook behavior, PID discovery, session behavior, and lifecycle

## [2.2.1] - 2026-03-26

### Fixed

- **PreCompact hook schema**: use top-level `systemMessage` instead of unsupported `hookSpecificOutput` for PreCompact events — fixes validation errors during context compaction

## [2.2.0] - 2026-03-26

### Added

- **Repo-level config**: `.punt-labs/ethos.yaml` for repo-specific identity config (agent, team bindings). Decoupled from the team submodule path. Backward-compatible fallback to old `.punt-labs/ethos/config.yaml`
- **Team-by-repo lookup**: `ethos team for-repo [repo]` CLI command and `for_repo` MCP method — query which team works on a given repository
- `FindByRepo` on team Store and LayeredStore
- `RepoName()` helper — parses org/repo from git remote URL
- `LoadRepoConfig` and `ResolveTeam` in resolve package
- **Role** as first-class concept: `internal/role/` package, `ethos role` CLI, `role` MCP tool, LayeredStore
- **Team** as first-class concept: `internal/team/` package, `ethos team` CLI with add-member/remove-member/add-collab, `team` MCP tool with 8 methods, Z-spec invariant enforcement (referential integrity, non-empty teams, no self-collaboration, dangling collab cleanup)

### Fixed

- `attribute.Store.isNotFound` TOCTOU race — replaced redundant `os.Stat` with `errors.Is`
- `team.LayeredStore.Load` redundant `Exists` call — replaced with `ErrNotFound` sentinel
- Referential integrity check on role deletion — cannot delete a role referenced by a team
- Z specification for teams/roles/identities domain (`docs/teams.tex`) — type-checks with fuzz, animates with probcli

## [2.1.0] - 2026-03-25

### Added

- **Persona animation**: SessionStart hook injects full personality, writing style, and talent slugs into session context
- **PreCompact hook**: re-injects condensed persona block before context compression so behavioral instructions survive compaction
- **SubagentStart persona injection**: subagents with matched identities (e.g., bwk, mdm) get their persona injected automatically at spawn
- Shared persona builder (`BuildPersonaBlock`, `BuildCondensedPersona`) for consistent formatting across all hooks
- Attribute resolution warnings logged to stderr in all hook handlers

### Changed

- Agent definitions (bwk.md, mdm.md) no longer need manual `ethos show` instructions — persona is injected by hooks
- CLAUDE.md Go standards section replaced with reference to org-wide `punt-kit/standards/go.md`

## [2.0.0] - 2026-03-25

### Changed

- **Breaking**: Renamed `persona` parameter to `handle` in all ext commands (CLI, MCP, and Go API `IdentityStore` `Ext*` methods) for consistency with other identity commands
- **Breaking**: Removed `voice` field from identity YAML — voice config now lives in `ext/vox` (DES-019). Auto-migration on Load handles existing files.
- Identity resolution is now layered: repo-local (`.punt-labs/ethos/`) → user-global (`~/.punt-labs/ethos/`) (DES-018)
- All CLI commands and MCP tools use layered resolution by default

### Added

- `LayeredStore` — two-layer identity store with repo-local priority
- `IdentityStore` interface — enables layered and concrete stores interchangeably
- `FindRepoEthosRoot()` — discovers `.punt-labs/ethos/` in the current git repo
- Repo-local identity, talent, personality, and writing style files for the ethos team (claude, jfreeman, bwk)
- `bwk` agent identity — Go specialist sub-agent based on Kernighan's principles

## [1.2.0] - 2026-03-23

## [1.1.0] - 2026-03-22

### Changed

- Consolidated `whoami`, `list_identities`, `get_identity`, `create_identity` into single `identity` MCP tool with `method` parameter
- Refactored all 5 hook shell scripts to thin gates per punt-kit/standards/hooks.md (387 → 30 lines)
- Moved hook business logic from shell to Go (`internal/hook/` package)
- Fixed two-channel display for consolidated MCP tools (tool name mismatch)

### Added

- `ethos hook` CLI subcommand group (session-start, session-end, subagent-start, subagent-stop, format-output)
- Non-blocking stdin reader with open-pipe-no-EOF safety (`internal/hook/stdin.go`)
- `make dev` / `make undev` targets for plugin cache symlink during development (DES-015)
- Delete method handlers for talent, personality, writing_style in two-channel display
- Per-tool sentinel directory check in hook shell scripts

## [1.0.0] - 2026-03-21

### Changed

- Renamed `skill` → `talent` system-wide (DES-014): MCP tool, CLI subcommand, identity YAML field, storage directory, all documentation
- Identity YAML field: `skills:` → `talents:` (breaking — update identity files manually)
- Storage directory: `~/.punt-labs/ethos/skills/` → `~/.punt-labs/ethos/talents/`
- CLI: `ethos skill` → `ethos talent`
- MCP tool: `skill` → `talent`
- Command: `/ethos:skill` → `/ethos:talent`

## [0.8.0] - 2026-03-21

### Added

- `/ethos:list-identities`, `/ethos:get-identity`, `/ethos:create-identity` slash commands

### Changed

- All slash commands namespaced under `/ethos:*` (DES-012) — no top-level deployment to `~/.claude/commands/`

### Removed

- Top-level command deployment from session-start hook (DES-013)
- `jq` settings mutation from session-start hook (DES-013)
- Users upgrading from v0.7.0 must manually delete ethos files from `~/.claude/commands/`

## [0.7.0] - 2026-03-21

## [0.6.0] - 2026-03-21

### Added

- `ethos resolve-agent` command — prints the default agent handle from repo config
- `Store.FindBy(field, value)` — lookup identities by `handle`, `email`, or `github` field
- `resolve.Resolve(store, sessionStore)` — 4-step identity resolution chain: iam declaration → git user.name → git email → $USER
- `resolve.ResolveAgent(repoRoot)` — reads agent handle from `.punt-labs/ethos/config.yaml`
- `resolve.GitConfig(key)` — reads git config values via `git config` subprocess
- `ethos doctor` checks: "Human identity", "Default agent", "Duplicate fields"
- Per-repo agent config via `.punt-labs/ethos/config.yaml` `agent:` field
- Native process tree walking: `/proc/<pid>/stat` on Linux, `sysctl kern.proc.pid` on macOS (replaces `ps -eo` subprocess)

### Changed

- `ethos whoami` is now read-only — resolves identity from iam/git/OS instead of reading `~/.punt-labs/ethos/active`
- `ethos list` marks all session participants with `*` (multiple markers possible)
- `ethos create` no longer auto-sets first identity as active
- MCP `whoami` tool uses `resolve.Resolve()` instead of `Store.Active()`
- MCP `list_identities` marks session participants (multiple `"active": true` entries possible)
- MCP `create_identity` no longer auto-activates first identity
- Session start hook resolves human and agent personas separately
- Usage text now writes to stderr (was stdout, caused garbage in shell captures)

### Removed

- `~/.punt-labs/ethos/active` file — human identity comes from git/OS
- `ethos whoami <handle>` write path — no "set active" operation
- `Store.Active()` method
- `Store.SetActive()` method
- `ErrNoActive` sentinel error
- `RepoConfig.Active` field — repos are multi-user, human identity is per-user
- `ps -eo pid=,ppid=,comm=` subprocess for process tree walking
- 13 attribute MCP tools (`create_skill`, `get_skill`, etc.) replaced by 3 (`skill`, `personality`, `writing_style`) with `method` parameter
- 4 ext MCP tools (`ext_get`, `ext_set`, etc.) replaced by 1 (`ext`) with `method` parameter
- 4 session MCP tools (`session_iam`, `session_roster`, etc.) replaced by 1 (`session`) with `method` parameter

### Added (slash commands)

- `/ethos:skill` — create, list, show, delete, add, remove
- `/ethos:personality` — create, list, show, delete, set
- `/ethos:writing-style` — create, list, show, delete, set
- `/ethos:ext` — get, set, del, list
- `delete` method on all attribute tools (was not exposed via MCP)

## [0.5.0] - 2026-03-20

### Added

- First-class attribute management: `ethos skill`, `ethos personality`, `ethos writing-style` subcommands with create/list/show/add/remove/set
- `internal/attribute` package — generic CRUD for named markdown files with path containment
- Identity attributes (`writing_style`, `personality`, `skills`) now reference markdown files by slug instead of inline strings
- `Store.Load()` resolves attribute content by default; `Reference(true)` option returns slugs only
- `Store.Update()` for read-modify-write on existing identities
- `Identity.Warnings` field for missing attribute file diagnostics
- `Store.ValidateRefs()` rejects Save when referenced attribute files are missing
- Interactive `ethos create` wizard with pick-from-list and create-new sub-flow for attributes
- 13 new MCP tools: `create_skill`, `get_skill`, `list_skills`, `create_personality`, `get_personality`, `list_personalities`, `create_writing_style`, `get_writing_style`, `list_writing_styles`, `set_personality`, `set_writing_style`, `add_skill`, `remove_skill`
- `reference` parameter on `get_identity` and `whoami` MCP tools
- Installer creates `skills/`, `personalities/`, `writing-styles/` directories

### Changed

- `ethos show` displays resolved attribute content with slug headers
- `ethos show --reference` returns slugs only
- `ethos list` uses reference mode (no attribute file reads)
- `ethos create` interactive prompts replaced with attribute picker wizard

## [0.4.0] - 2026-03-19

### Added

- Architecture specification (`docs/architecture.tex`) — LaTeX document covering package map, identity model, extension system, session roster, resolution chain, MCP tool surface, design invariants, and security boundaries
- `scripts/release-plugin.sh` and `scripts/restore-dev-plugin.sh` per plugins standard
- Homebrew formula: `brew install punt-labs/tap/ethos`

### Changed

- Plugin name on main is now `ethos-dev` (prod name `ethos` set at release time)
- Release tags no longer include `-dev` command files

## [0.3.4] - 2026-03-19

### Fixed

- All hooks rewritten to match biff's proven patterns (DES-009)
- SessionStart: removed `INPUT=$(cat)` stdin read that blocked indefinitely (Claude Code doesn't close pipe promptly)
- Subagent/SessionEnd hooks: replaced `INPUT=$(cat)` with `read -r -t 1` (non-blocking with 1s timeout)
- PostToolUse: kept `INPUT=$(cat)` (pipe closes for this event), removed `set -euo pipefail` for graceful degradation, switched to `jq` for JSON output
- hooks.json: removed empty `"matcher": ""` from all non-PostToolUse hooks
- All hooks: added kill switch, `exit 0`, `hookEventName` in output, `PLUGIN_ROOT` from `dirname` not env var

## [0.3.3] - 2026-03-19

### Fixed

- Removed `set -u` from subagent-start, subagent-stop, session-end, and suppress-output hooks (same bug as session-start fix in v0.3.2)

## [0.3.2] - 2026-03-19

### Fixed

- Installer downloads pre-built release binary instead of `go install` (fixes `ethos dev` version display)
- SessionStart hook: removed `set -u` and bash arrays that crash on bash < 4.4 (fixes `SessionStart:startup hook error`)

## [0.3.1] - 2026-03-19

### Fixed

- Installer now force-updates marketplace before plugin install (prevents stale version)
- Installer verifies installed plugin version matches expected version with mismatch warning

## [0.3.0] - 2026-03-19

### Added

- `/session` and `/iam` slash commands with `-dev` variants for plugin parity
- MCP tool permission auto-allow in SessionStart hook (matches biff pattern)
- Persistent PATH setup in installer — appends `~/.local/bin` to shell profile

### Changed

- Refactored `main()` command dispatch from switch to map (cyclomatic complexity 22 → 10)
- Extracted `voiceValue()`, `joinSkills()`, `showExtensions()` from `runShow()` (complexity 16 → 6)
- Applied `gofmt -s` to `identity_test.go`
- SessionStart hook detects dev mode and skips top-level command deployment when running as `ethos-dev`

### Fixed

- `ethos doctor` no longer fails on fresh install when no active identity exists (PASS with guidance instead of FAIL)
- `whoami.md` command: `allowed-tools` corrected from bare string to array
- PostToolUse suppress hook now returns meaningful per-tool summaries instead of generic "Done."

## [0.2.0] - 2026-03-19

### Added

- `ethos uninstall` command — removes Claude Code plugin; `--purge` removes binary and all identity data with confirmation
- Release and Go Report Card badges in README

### Changed

- Installer rewritten with SSH fallback, marketplace check-before-register, uninstall-before-install, post-install verification, temp dir cleanup trap, and conditional doctor success message
- Replaced personal identity data in docs and tests with Firefly characters (Mal Reynolds, River Tam)
- `warn()` and `fail()` in install.sh now output to stderr

## [0.1.0] - 2026-03-18

### Added

- Session roster for multi-participant identity awareness (DES-007)
  - `internal/session/` package with `Store` (flock-based concurrency), `Roster`, and `Participant` types
  - `internal/process/` package for process tree walking (find topmost Claude ancestor PID)
  - `ethos iam <persona>` command to declare persona in current session
  - `ethos session` commands: `create`, `join`, `leave`, `purge`, and default roster display
  - MCP tools: `session_iam`, `session_roster`, `session_join`, `session_leave`
  - Hooks: `SubagentStart`, `SubagentStop`, `SessionEnd` lifecycle hooks
  - PID-keyed current session files for session ID propagation to non-hook callers
  - Extended `SessionStart` hook to create session roster with root + primary participants
- Initial project scaffolding — Go module, CLI entry point, Makefile, CI workflows
- Identity YAML schema with channel bindings (voice, email, GitHub, agent)
- `ethos version` and `ethos doctor` admin commands
- `ethos create` — interactive and declarative (`--file`/`-f`) identity creation
- `ethos whoami`, `ethos list`, `ethos show` — identity management commands
- MCP server (`ethos serve`) with 4 tools: `whoami`, `list_identities`, `get_identity`, `create_identity`
- `install.sh` — POSIX installer (build from source, plugin registration, doctor)
- GitHub branch protection ruleset with zero bypass actors
- Dependabot, secret scanning, and push protection enabled
- `--json` global flag for machine-readable output on `whoami`, `list`, `show`, `doctor`, `version`
- Subcommand-level `--help`/`-h` with per-command usage text
- `--` separator support in global flag parsing
- 49 tests across 4 packages (84-96% coverage on internal/ packages)

### Fixed

- `show` now displays writing_style, personality, and skills fields
- `show` collapses multi-line YAML values to single-line display
- `show` handles empty VoiceID without trailing slash
- Duplicate identity error no longer references non-existent `edit` command
- Home directory resolution returns errors instead of empty string on `$HOME` failure
- `doctor` distinguishes permission errors from missing directory
- `voice_id` requires `voice_provider` across all creation paths (CLI, MCP, file)
- Malformed identity files produce stderr warnings instead of silent skips
- `Store.Save` uses `O_EXCL` for atomic create (no TOCTOU race)

### Changed

- Consolidated duplicate `Identity` struct into `internal/identity/` with `Store` type
- Extracted MCP handlers from `cmd/ethos/serve.go` to `internal/mcp/tools.go`
- `cmd/ethos/serve.go` reduced from 223 to 25 lines
- `Voice` is a pointer type for proper JSON/YAML omitempty semantics
- Handle regex tightened to disallow trailing hyphens
- `Validate()` expanded with handle format and voice validation
- MCP handlers receive `Store` via injection (no `os.Exit` in handler context)
- ShellCheck added to CI and `make lint`

[Unreleased]: https://github.com/punt-labs/ethos/compare/v3.0.0...HEAD
[2.6.1]: https://github.com/punt-labs/ethos/compare/v2.6.0...v2.6.1
[2.6.0]: https://github.com/punt-labs/ethos/compare/v2.5.0...v2.6.0
[2.5.0]: https://github.com/punt-labs/ethos/compare/v2.4.1...v2.5.0
[2.4.1]: https://github.com/punt-labs/ethos/compare/v2.4.0...v2.4.1
[2.4.0]: https://github.com/punt-labs/ethos/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/punt-labs/ethos/compare/v2.2.1...v2.3.0
[2.2.1]: https://github.com/punt-labs/ethos/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/punt-labs/ethos/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/punt-labs/ethos/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/punt-labs/ethos/compare/v1.2.0...v2.0.0
[1.2.0]: https://github.com/punt-labs/ethos/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/punt-labs/ethos/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/punt-labs/ethos/compare/v0.8.0...v1.0.0
[0.8.0]: https://github.com/punt-labs/ethos/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/punt-labs/ethos/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/punt-labs/ethos/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/punt-labs/ethos/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/punt-labs/ethos/compare/v0.3.4...v0.4.0
[0.3.4]: https://github.com/punt-labs/ethos/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/punt-labs/ethos/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/punt-labs/ethos/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/punt-labs/ethos/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/punt-labs/ethos/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/punt-labs/ethos/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/punt-labs/ethos/releases/tag/v0.1.0

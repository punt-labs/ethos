# Project-defined agent checks: edit, commit and handoff stages

**Status**: SETTLED as DES-085. Linear PL-1275. Supersedes PR #642.

The implementation covers strict check sets, layered references, argv
execution, glob selection and advisory/block feedback at edit, commit and
SubagentStop handoff. Mission-result integration is the next slice.
Non-exec adapters, shell execution,
cache/coalescing, concurrency tuning, audit metrics and doctor/init commands
below describe later work, not shipped behavior. Generated commands stay
unchanged when a check set changes: policy is loaded at hook execution time.

Read first: punt-kit `standards/hooks.md` §6–7 (observation vs mutation hooks,
fail-open vs fail-closed), DES-055 (exit 2 blocks), DES-073 (identity skills
in generated frontmatter), DES-079 and DES-084 (store root S, checkout C),
DES-083 (thin shims: the binary owns policy), `internal/hook/generate_agents.go`
(`buildAgentFile`), `internal/role/role.go`,
`internal/identity/identity.go`, `internal/schema/registry.go`,
`cmd/ethos/hook_precommit.go`, `internal/hook/subagent_stop.go`.

## 1. Problem

Every generated agent with Write or Edit gets a `PostToolUse` hook that runs
the full `make check` after each matching edit, in the repo that holds the
edited file. In ethos that run takes about 708 s on an 8-core VM (`make test`
634 s, lint 45 s). That is longer than the 600 s Claude Code watchdog, and it
blocks with exit 2. PL-1275 records five defects:

1. **Cost.** The full gate runs per edit.
2. **Trust.** An absolute path in another repo runs that repo's Makefile.
3. **Multi-file changes are blocked.** A change that is red until its third
   edit is blocked at its first. TDD's intentionally failing test is blocked
   too.
4. **`make` is hardcoded.** The hook assumes every repo has a useful
   `make check`.
5. **No configuration.** A repo or role can neither change the hook nor turn
   it off.

The hook also violates punt-kit hooks.md: `PostToolUse Edit|Write` is an
observation hook ("inject relevant standards", fail-open). This one is a
mutation hook that fails closed, although the edit has already happened.

Operator decisions (Jim, 2026-11-21) that this design takes as fixed:

- Keep frequent feedback on edits.
- The project configures edit, commit and handoff checks through the ethos
  agent identity schema.
- Commands and file scope are defined by the project. Nothing is hardcoded,
  `make` included.
- Each check is either advisory or blocking.
- A TDD project must be able to leave a test failing on purpose at edit time.
- The design provides build-system plugin points. The Punt OO Ratchet is the
  worked example of a commit-stage check.

## 2. Model

A **check** is one command with a file scope, a stage, a failure mode and a
time limit. A **check set** is a named, slug-addressed attribute holding
checks. It sits beside talents, personalities and writing styles:
`.punt-labs/ethos/checks/<slug>.yaml`, resolved through the same three layers
(repo, active bundle, global; DES-051).

The agent identity schema references a check set:

| Field | Entity | Meaning |
|---|---|---|
| `checks: <slug>` | role | Default check set for every identity that holds the role |
| `checks: <slug>` or `checks: none` | identity | Per-agent override; `none` opts out |
| `checks: <slug>` | `.punt-labs/ethos.yaml` | Project default for roles that name none |

Resolution, first match wins: identity, then role, then project default, then
§7's compatibility default. The handle of the resolved set and its content
digest are recorded in the generated agent's frontmatter comment. That lets
`ethos doctor` detect a stale agent file, as DES-083 does for hook sections.

Why a referenced attribute and not inline YAML on the role? One project runs
the same checks for many roles. The identity and role files stay small. The
existing layered store, `ethos validate` and the schema registry handle a new
attribute kind without new machinery.

### Stages

| Stage | Trigger | Files in scope | Can block? | Failure of ethos itself |
|---|---|---|---|---|
| `edit` | Claude `PostToolUse` on `Write\|Edit`; OpenCode `tool.execute.after` | The edited file | Advisory by default; blocking only with `on_fail: block` | Fail-open, with an advisory naming the error |
| `commit` | `ethos hook pre-commit` (existing DES-083 shim) | Staged paths in the committing index | Yes | Fail-closed (exit 2), the same as seal |
| `handoff` | `SubagentStop` for a generated agent; `ethos mission result` for a mission worker | Paths the agent wrote this session (audit log), or `all` | Yes, through the hooks.md decision-block pattern | Fail-closed, with the `stop_hook_active` re-entry guard |

**Edit stage.** Fits hooks.md as written: an observation hook that injects
the check output as context. A blocking edit check is allowed, because some
projects want gofmt to stop the next tool call. Hooks.md would then need an
amendment: "an edit-stage check the project marks blocking may block, and
must finish within its budget". The amendment goes to punt-kit in its own PR.
Ethos ships with advisory as the default, so a project that sets nothing
complies with hooks.md as it stands today.

**Commit and handoff stages.** These are mutation gates, so failing closed is
correct. The full project gate (`make check` in ethos) belongs in one of
them, not in `edit`.

## 3. Schema

```yaml
# .punt-labs/ethos/checks/go-default.yaml
name: go-default
description: Go formatting and vet on edit; full gate at handoff.
checks:
  - name: gofmt
    stage: edit
    files: ["**/*.go"]
    run: ["gofmt", "-l", "{file}"]
    fail_on_output: true        # gofmt -l exits 0 and lists unformatted files
    on_fail: block              # cheap, deterministic, never red on purpose
    timeout: 5s
  - name: vet
    stage: edit
    files: ["**/*.go"]
    run: ["go", "vet", "{package_dir}"]
    on_fail: advisory
    timeout: 20s
  - name: unit-tests
    stage: edit
    files: ["**/*.go"]
    run: ["go", "test", "-count=1", "{package_dir}"]
    on_fail: advisory           # TDD: a red test reports, never blocks
    timeout: 30s
  - name: shellcheck
    stage: edit
    files: ["**/*.sh"]
    run: ["shellcheck", "{file}"]
    on_fail: advisory
  - name: gate
    stage: handoff
    files: all
    adapter: make
    target: check
    on_fail: block
    timeout: 20m
```

```yaml
# .punt-labs/ethos/checks/python-oo.yaml — the Punt OO Ratchet at commit
name: python-oo
checks:
  - name: ruff
    stage: edit
    files: ["**/*.py"]
    run: ["uv", "run", "ruff", "check", "{file}"]
    on_fail: advisory
  - name: oo-ratchet
    stage: commit
    files: ["**/*.py"]
    run: ["uv", "run", "oo-ratchet", "oo", "{files}"]
    on_fail: block
    timeout: 120s
  - name: oo-coupling
    stage: commit
    files: ["**/*.py"]
    run: ["uv", "run", "oo-ratchet", "coupling", "{files}"]
    on_fail: block
```

The ratchet's baseline rewrite (`make update-oo`) is not run by the check.
Hooks never change the index; the agent runs it and stages the result, as
python.md requires.

Role and identity references:

```yaml
# roles/go-specialist.yaml
name: go-specialist
checks: go-default

# identities/bwk.yaml — opt this agent out
checks: none
```

### Fields

| Field | Type | Default | Rule |
|---|---|---|---|
| `name` | slug | required | Unique within the set |
| `stage` | enum `edit\|commit\|handoff` | required | |
| `files` | list of glob, or `all` | required | Doublestar globs relative to checkout C. `all` is invalid at `edit` |
| `exclude` | list of glob | `[".tmp/**", ".punt-labs/local/**"]` | Always added to, never replaced |
| `run` | argv list | one of `run`/`adapter` | Executed without a shell |
| `adapter` | string | none | §4 |
| `on_fail` | `advisory\|block` | `advisory` at edit, `block` elsewhere | |
| `fail_on_output` | bool | false | Non-empty stdout counts as a failure |
| `timeout` | duration | 10s edit, 120s commit, 10m handoff | Capped by the stage budget (§6) |
| `cwd` | `root\|package_dir\|file_dir` | `root` | |

Placeholders: `{file}` (one path, so the check runs once per edited file),
`{files}` (all matched paths, one argv element each), `{package_dir}` (the
directory holding `{file}`, de-duplicated) and `{root}` (C). Each placeholder
is substituted as whole argv elements, never spliced into a string. A
filename with spaces or `$(...)` stays literal. That is the defect class
Qodo found in #642.

## 4. Build-system plugin points

`adapter` names a Go type behind one interface:

```go
type Adapter interface {
    Name() string
    Validate(c Check, root string) error            // static: target exists, tool found
    Command(c Check, root string, files []string) ([]string, error)
}
```

Built-in adapters:

| Adapter | Fields | Command | Probe |
|---|---|---|---|
| `exec` (implicit when `run` is set) | `run` | `run` with placeholders filled | `exec.LookPath(run[0])` |
| `make` | `target`, `vars` | `make <target> FILE=<path>` | `make -pq <sentinel>` rule database; never runs a recipe (the Devin Review finding on #642) |
| `go` | `go: vet\|test\|build` | `go <verb> <package_dir>` | `go.mod` present |
| `uv` | `uv: <tool args>` | `uv run <args>` | `pyproject.toml` present |
| `npm` | `script` | `npm run <script> -- <files>` | `package.json` scripts key |

Adding an adapter is a Go change plus a registry entry. A project that needs
something else uses `exec`. The design rejects loading third-party adapters
at runtime (§9).

## 5. Generated behavior

The generated hook stops carrying policy in shell, applying DES-083's rule:

```yaml
hooks:
  PostToolUse:
    - matcher: "Write|Edit"
      hooks:
        - type: command
          command: "ethos hook post-edit --agent bwk"
          timeout: 70          # edit-stage budget + 10 s
```

An agent whose resolved set has no edit checks gets no `hooks:` block.
`ethos hook post-edit`:

1. Reads `tool_input.file_path` with the existing bounded reader.
2. Applies the root scope (§8). A path out of scope ends the hook with exit 0
   and no output.
3. Loads the agent's resolved check set from the single validated snapshot.
   It never reads a policy file from the edited file's own repo.
4. Selects the edit checks whose `files` match and `exclude` does not. Runs
   them in parallel, with a concurrency limit of `GOMAXPROCS/2`.
5. Skips a check whose last pass covered the same file content hash
   (`.punt-labs/local/ethos/checks/cache/`). Kills a running check for the
   same agent and file when a newer edit arrives (coalescing).
6. Output:
   - All pass: exit 0, nothing printed.
   - Advisory failure: exit 0, with
     `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"<check>: <last 40 lines>"}}`.
   - Blocking failure: `{"decision":"block","reason":"<check>: <last 40 lines>"}`.
   - Ethos error (bad config, panic, timeout of ethos itself): an advisory
     naming the error, exit 0. This is fail-open.
   - A check timing out is reported as that check's failure, using its
     `on_fail` mode.

The commit stage is added to `ethos hook pre-commit`, after the seal and
provenance steps. It needs no new shim. It runs for every commit, human or
agent, with the project default set, or the committing session's resolved
set when one is found. Exit 2 blocks the commit and prints the failing
check's tail.

The handoff stage runs in `ethos hook subagent-stop`; `ethos mission result`
integration is deferred to the next slice. Roster leave happens before checks,
including when checks are killed or block. The payload's agent type/handle
selects policy. Existing audit union readers select that agent's Write/Edit
paths from the session; if no usable paths are available, selection falls back
to the checkout's staged, unstaged and untracked files. An unreadable audit
fails closed rather than silently narrowing the scope.

A blocking failure or broken policy returns
`{"decision":"block","reason":...}` with exit zero. On `stop_hook_active`
the hook allows the stop and sends the failure to stderr instead. Advisory
failures always use stderr. The plugin currently declares no SubagentStop
timeout, so its harness timeout does not provide the 30-minute stage window;
the binary enforces its budgets, but the harness may terminate it earlier.

Commit/handoff checks run sequentially under a shared stage deadline. At
`cwd: root`, `{files}` expands to all matching paths and `{package_dir}` to
their deduplicated root-relative directories. A directory cwd runs once per
matching directory, with that directory's matching files. `files: all` runs
even with no paths, from the root in that case. There are no marker defaults
at these stages. The kill switch skips checks, not commit seal/provenance
steps or roster leave. Commit file discovery and commands preserve the actual
committing index while scrubbing other Git selectors and tracing variables.

OpenCode generation (`.opencode/agents`) uses the same subcommand from its
`tool.execute.after` plugin hook, so the two harnesses share one policy.

## 6. Runtime budgets

| Stage | Default per check | Stage cap (validation error above) | Hook `timeout` field |
|---|---|---|---|
| edit | 10 s | 60 s total wall time | cap + 10 s |
| commit | 120 s | 15 min | not applicable (git) |
| handoff | 10 min | 30 min | cap + 30 s |

The edit cap is far below the 600 s watchdog. Ethos records each check's
duration in the local audit trace. `ethos doctor` WARNs when the p95 of an
edit check over the last 50 runs exceeds half its timeout.

## 7. Defaults, migration and compatibility

- **No configuration at all.** Jim chose advisory defaults immediately, with
  no blocking compatibility release. The defaults shipped in #643 remain:
  `go.mod` selects gofmt and package vet for Go edits; `pyproject.toml` selects
  ruff (or uv run ruff) for Python; shell edits select shellcheck. Missing
  tools are skipped silently. There is no implicit `make check`.
- **Configured policies.** Identity, then the generator-bound role, then repo
  config selects a check-set slug. `none` at any level stops inheritance.
  An unresolved handle uses the repo setting, then the advisory defaults.
  Policy comes from the session checkout's canonical store root S (resolved
  through Git's common directory), never the edited worktree's config.
  Every configured check chooses `advisory` or `block`; edit defaults to
  advisory. A block is exit-zero PostToolUse decision feedback, never exit 2.
- **`ethos checks init`.** Writes a starter set from marker files:
  - `go.mod`: gofmt (block) and vet (advisory) at edit; `make check` at
    handoff when a Makefile defines `check`.
  - `pyproject.toml`: ruff at edit; the OO ratchet at commit when
    `punt-oo-ratchet` is a dev dependency.
  - Neither: shellcheck and a YAML parse.
  It prints the file and never overwrites one.
- **Old binary, new agent file.** `ethos hook post-edit` is a new
  subcommand. A pre-DES-085 binary rejects it, or at v4.8.0 and earlier
  prints help and exits 0. Edit is fail-open, so both outcomes just mean no
  feedback. Doctor's existing "Hook binary" row FAILs the version mismatch.
  Under DES-083's rule, a later change adds a new subcommand name, never a
  new flag.
- **New binary, old agent file.** The inline shell keeps working until
  SessionStart regenerates the file; regeneration is already automatic.
- **PR #642.** Superseded, not merged. Its Makefile target and generated shell
  are not used. A future make adapter remains separate work.

## 8. Security and root scope

- **Policy source.** Check sets are read only from the snapshot of the
  session's checkout C (repo layer), the active bundle and the global layer.
  Those files are git-tracked and human-reviewed, the same trust boundary
  roles have today. Nothing is read from the repo that holds the edited file.
- **Allowed roots.** An edited path is in scope only when its resolved real
  path is under C, or under a linked worktree whose git common directory is S
  (the established resolver; DES-079). Everything else is out of scope and
  silent, including other checkouts, `$HOME` and symlinks that escape C.
  This closes PL-1275 defect 2.
- **Execution.**
  - Commands are argv with no shell, unless `shell: true`; validation warns
    on that.
  - `cwd` is always inside C.
  - The environment is the hook's, minus `GIT_DIR` and `GIT_INDEX_FILE`
    (except at commit) and the `GIT_TRACE*` variables, the same scrubbing as
    `gitenv`.
- **Output.** A check's tail is redacted (DES-080) before it is injected into
  model context or written to the trace.

## 9. Validation

`cmd/validate-content` and `ethos validate` gain the `checks` kind, with
overlays in `internal/schema/registry.go`. Errors:

- an unknown stage, adapter or placeholder;
- `files: all` at `edit`;
- both or neither of `run` and `adapter`;
- an empty argv;
- a timeout above the stage cap;
- `{file}` at `commit` or `handoff` (use `{files}`);
- a duplicate check name;
- a role or identity naming a missing set.

Warnings:

- `shell: true`;
- an edit check with `on_fail: block` whose command matches a test runner
  (`go test`, `pytest`, `npm test`), because it defeats TDD;
- an adapter probe that fails, such as a missing make target or tool not on
  PATH.

Invalid configuration fails visibly once per request, as DES-079 requires.
At `edit` that means an advisory, never a block.

## 10. Tests

Every fix follows a failing test (AGENTS.md).

| Area | Test |
|---|---|
| Schema | Each validation rule above; registry overlay round-trip |
| Resolution | identity > role > project > legacy, `none` opt-out, three layers, digest changes regenerate |
| Matching | doublestar globs, `exclude` always applied, `{file}`/`{files}`/`{package_dir}` substitution with spaces, `$(x)`, `*`, leading `-` |
| Root scope | Path in C, in a linked worktree (same S), in a foreign repo with its own hostile `checks` and Makefile (must not run: marker file absent), symlink escape, relative path |
| Edit output | Pass is silent; advisory goes to `additionalContext` JSON with exit 0; block goes to `decision: block`; ethos error is fail-open |
| TDD | Advisory test check red: the hook exits 0 and the next edit proceeds; the same check at commit blocks |
| Budgets | A check that sleeps past its timeout is killed and reported; the stage cap is enforced; a newer edit coalesces and kills the older run |
| Make adapter | A target probe with `+` and `$(MAKE)` recipes runs nothing (from #642's finding) |
| Commit | Real `git commit` through the installed shim: staged-set scoping, OO ratchet stub blocks, human commit uses the project default |
| Handoff | SubagentStop blocks once, `stop_hook_active` allows; mission result records the failure |
| Compatibility | Old binary with a new agent file (fail-open); legacy synthesized set equals today's matcher; `checks init` per marker |
| Generator | Golden agent files for: no checks, edit checks, handoff only |
| Demo | Real Claude session in ethos: edit a .go file (about 1 s feedback), introduce a red test (advisory), commit blocked by the gate, hostile foreign-repo edit (nothing runs) |

`docs/spec-hook-gates.tex` gains the stage-verdict function, for Z model
checking. The model covers three properties: edit never blocks unless the
check is marked `block`; commit and handoff never pass on an ethos error; an
out-of-scope path never reaches a command.

## 11. Rejected alternatives

| Alternative | Why rejected |
|---|---|
| Keep `make check-edit` probing in generated shell (#642) | Still hardcodes make. Policy stays in shell that a release cannot update (DES-083). A probe can run recipes. Leaves the trust boundary open. |
| Checks inline on each role | Duplicates per role; one project-wide change touches every role file |
| Policy in `.punt-labs/ethos.yaml` only | Cannot vary per role or agent. Jim asked for the identity schema. |
| Runtime-loaded adapter plugins (Go plugin, WASM, external binaries by name) | A new code-execution trust surface. `exec` already covers the need. |
| Block at edit by default | Contradicts hooks.md. Breaks multi-file changes and TDD (PL-1275 defect 3). |

## 12. Implementation order

1. Root scope plus `ethos hook post-edit` running the synthesized legacy set,
   with the generator emitting the one-line hook. Fixes PL-1275 defect 2.
   Behavior is otherwise unchanged.
2. Check-set attribute, schema, validation and resolution; `exec` adapter;
   edit output protocol.
3. Commit stage in `hook pre-commit`; handoff stage in subagent-stop and
   mission result.
4. `make`, `go`, `uv` and `npm` adapters; `ethos checks init`; doctor rows;
   ethos's own `go-default` set, measured before and after.
5. punt-kit hooks.md amendment PR; legacy default removed one minor release
   later.

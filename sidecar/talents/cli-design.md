# CLI Design

Building command-line tools that behave like Unix citizens. A well-designed
CLI is discoverable, composable, and predictable. It works in a pipeline,
in a script, and under a human's fingers without surprises.

## Unix Philosophy

Every CLI is a filter. It reads input, transforms it, writes output.

- Do one thing well. A tool that parses YAML and also deploys to Kubernetes
  does two things. Split it.
- Compose through text streams. Stdout is your API. If another tool can't
  pipe your output into `grep`, `jq`, or `awk`, your output format is wrong.
- Fail fast and loud. Silent failures in a pipeline corrupt every downstream
  stage. Exit non-zero, write the error to stderr, and let the caller decide
  what to do.
- No side effects unless that is the point. A query command that also writes
  a cache file is surprising. Separate reads from writes.

Design for the pipe first, then add human polish. A tool that works in
`xargs` will also work interactively. The reverse is not true.

## Command Structure

### Subcommands

Use subcommands when the tool has distinct operations on a shared resource:

```text
ethos identity list
ethos identity show mal
ethos session purge
```

Keep the tree shallow. Two levels is typical. Three is acceptable for large
tools (e.g., `git remote set-url`). Four is a sign that the tool should be
split.

Every subcommand is a noun-verb or verb-noun pair. Be consistent within the
tool. `ethos session purge` (noun-verb) and `ethos purge session`
(verb-noun) are both valid, but mixing them in the same tool is not.

### Flags

Flags modify behavior. They are optional by definition -- if a flag is
required, it should be a positional argument.

- Use `--long-names` for clarity. Single-letter shortcuts (`-v`, `-n`) for
  the 3-5 most common flags only.
- Boolean flags are `--flag` (true) and `--no-flag` (false). Never
  `--flag=true`.
- Flags with values: `--output json` or `--output=json`. Support both forms.
- Global flags (before the subcommand) vs local flags (after). Global flags
  apply to all subcommands: `--verbose`, `--config`, `--no-color`.

### Positional Arguments

Positional arguments are the primary operands. They answer "what" not "how":

```text
ethos identity show mal        # "mal" is what, "show" is the verb
cp source.txt dest.txt          # source and dest are what
```

Rules:

- Zero or one positional argument is clear. Two is acceptable. Three or more
  needs flags.
- Variadic positional args (`rm file1 file2 file3`) work for homogeneous
  lists. Never mix types.
- If the argument is optional, document the default explicitly.

### When to Use Each

| Need | Mechanism |
|------|-----------|
| Primary operand | Positional argument |
| Modify behavior | Flag |
| Distinct operation | Subcommand |
| Pipeline input | Stdin |
| Persistent config | Config file or env var |

## Help Text

Help text is the manual. For most users, `--help` is the only documentation
they will read. It must be complete, accurate, and include examples.

### Structure

```text
Usage: tool command [flags] <required> [optional]

Short description (one line, no period).

Longer description if needed. Explain what the command does,
not how it works internally.

Examples:
  tool list                     List all items
  tool show mal --format json   Show item as JSON
  tool delete mal --force       Delete without confirmation

Flags:
  -f, --format string   Output format: text, json, yaml (default "text")
  -v, --verbose         Show detailed output
  -h, --help            Show this help

Environment:
  TOOL_CONFIG   Path to config file (default ~/.config/tool/config.yaml)
```

### Rules

- The usage line shows exact syntax. Square brackets for optional, angle
  brackets for required.
- Examples are mandatory. Show the 2-3 most common invocations. Include at
  least one with flags.
- Default values are shown in the flag description, not just in docs.
- Flag grouping: required flags first (if any), then common flags, then
  rare flags.
- Environment variables that affect the command are listed. Users should not
  have to grep source code to find them.

## Output Design

### Human-Readable by Default

When stdout is a terminal, format for humans: aligned columns, color
(respecting `NO_COLOR`), headers, whitespace. When stdout is a pipe, strip
color codes automatically.

```text
NAME    KIND    EMAIL
mal     human   mal@serenity.ship
wash    human   wash@serenity.ship
```

### Machine-Readable on Request

`--json` or `--format json` for structured output. This is the stable API.
Human-readable format can change between versions; JSON format is versioned
and backward-compatible.

```json
[
  {"name": "mal", "kind": "human", "email": "mal@serenity.ship"},
  {"name": "wash", "kind": "human", "email": "wash@serenity.ship"}
]
```

JSON output rules:

- One JSON object per logical result. Arrays for lists, objects for singles.
- Field names are snake_case. Never camelCase.
- Empty results produce `[]` or `{}`, not nothing.
- Errors in JSON mode also output JSON to stderr.

### Stderr for Diagnostics

Stdout is data. Stderr is everything else: progress bars, warnings, debug
logs, error messages. This is not optional -- mixing diagnostics into stdout
breaks every pipeline.

```bash
# Correct: data to stdout, progress to stderr
ethos resolve mal > result.json 2>debug.log

# Broken: progress bars in stdout corrupt the JSON
```

## Exit Codes

Exit codes are the return value of your function. They are part of your API.

| Code | Meaning | When |
|------|---------|------|
| 0 | Success | Operation completed as requested |
| 1 | General error | Runtime failure, network error, file not found |
| 2 | Usage error | Bad flags, missing arguments, invalid syntax |
| 3+ | Domain-specific | Defined per tool, documented in help text |

Rules:

- Exit 0 means the tool did what was asked. Not "it ran without crashing."
- Exit 2 for every input validation failure. The user typed it wrong.
- Never exit 0 on error. Even if the error was "handled." If the requested
  operation did not succeed, the exit code is non-zero.
- Document non-standard codes in `--help` and the man page.
- `set -e` in scripts depends on correct exit codes. A tool that exits 0 on
  failure will silently corrupt script execution.

## Input Handling

### Precedence Chain

When the same setting can come from multiple sources, apply this precedence
(highest to lowest):

1. Explicit flag (`--config /path`)
2. Environment variable (`TOOL_CONFIG=/path`)
3. Config file (`~/.config/tool/config.yaml`)
4. Built-in default

This is the principle of least surprise. A flag always wins because the user
typed it right now. An env var wins over config because the user set it for
this shell session. Config wins over defaults because the user customized it.

### Stdin

Read from stdin when no file argument is given, or when the argument is `-`:

```bash
echo '{"name": "mal"}' | ethos validate
ethos validate -                        # explicit stdin
ethos validate identity.yaml            # file argument
```

If stdin and a file argument are both provided, the file wins. Never read
stdin and a file argument as two separate inputs -- that is confusing.

Detect whether stdin is a pipe or a terminal. If it is a terminal and the
tool expects stdin input, print a hint and wait (or error with a usage
message, depending on the command).

### Environment Variables

- Prefix all env vars with the tool name: `ETHOS_CONFIG`, not `CONFIG`.
- Document every env var in `--help` output.
- Boolean env vars: `ETHOS_DEBUG=1` (true) or unset (false). Never check
  for `true`/`false` strings.
- Never read undocumented env vars. If it affects behavior, it is in the
  help text.

## Error Messages

An error message answers three questions: what happened, why, and what to
do about it.

### Structure

```text
Error: identity "zoe" not found

The identity file was not found at .punt-labs/ethos/identities/zoe.yaml
or ~/.punt-labs/ethos/identities/zoe.yaml.

To create this identity:
  ethos identity create zoe --kind human --email zoe@serenity.ship
```

### Rules

- Start with "Error:" (to stderr). No stack traces unless `--verbose`.
- Name the entity that failed. "not found" is useless. "identity 'zoe' not
  found in .punt-labs/ethos/identities/" is actionable.
- Suggest a fix when possible. If the user misspelled a name, suggest the
  closest match. If a file is missing, show the expected path.
- Never print "an error occurred." Every error has a specific identity.
- Permission errors: show the path that failed and the required permission.
- Network errors: show the URL, the HTTP status, and whether retrying might
  help.
- Validation errors: show every invalid field at once, not one per
  invocation. Nobody wants to run a command 5 times to find 5 problems.

## Interactive vs Non-Interactive

### TTY Detection

Check whether stdin/stdout are terminals. In Go: `term.IsTerminal`. In
shell: `[ -t 0 ]` for stdin, `[ -t 1 ]` for stdout.

When not a TTY:

- No color codes
- No progress bars or spinners
- No interactive prompts (fail instead of asking)
- No pagers

### The --no-input Flag

For CI and scripts, provide `--no-input` (or `--yes`, `--non-interactive`)
that suppresses all prompts and uses defaults or fails:

```bash
# Interactive: prompts for confirmation
ethos identity delete mal

# Non-interactive: fails if confirmation would be needed
ethos identity delete mal --no-input

# Non-interactive: confirms automatically
ethos identity delete mal --yes
```

### Confirmation Prompts

Destructive operations (delete, overwrite, purge) prompt for confirmation.

Rules:

- Show what will happen before asking.
- Default to the safe choice (no).
- `--force` or `--yes` to bypass.
- In non-TTY mode, refuse to proceed without `--force`.

## Configuration

### File Location

Follow XDG Base Directory Specification:

- Config: `$XDG_CONFIG_HOME/tool/config.yaml` (default `~/.config/tool/`)
- Data: `$XDG_DATA_HOME/tool/` (default `~/.local/share/tool/`)
- Cache: `$XDG_CACHE_HOME/tool/` (default `~/.cache/tool/`)

On macOS, also support `~/Library/Application Support/tool/` but prefer XDG
for cross-platform consistency.

### Format

YAML or TOML for human-edited config. JSON for machine-generated config.
Never invent a custom config format.

```yaml
# ~/.config/ethos/config.yaml
default_format: json
color: auto          # auto, always, never
verbose: false
```

### Precedence

Flags > env vars > project config > user config > system config > defaults.

Project config (`.ethos.yaml` in the repo) overrides user config for that
project. This lets teams share settings without polluting personal config.

## Shell Completion

### Generate, Don't Hand-Write

Completion scripts break when commands change. Generate them from the
command tree:

```bash
ethos completion bash > /etc/bash_completion.d/ethos
ethos completion zsh > "${fpath[1]}/_ethos"
ethos completion fish > ~/.config/fish/completions/ethos.fish
```

### What to Complete

- Subcommands and flags (automatic from the command tree)
- Flag values when the set is known (e.g., `--format` completes to
  `text`, `json`, `yaml`)
- Positional arguments when discoverable (e.g., identity names from the
  filesystem)
- File paths for file-accepting flags

### Registration

Document the one-liner for each shell in `--help` and the README. Users
should not have to search for how to enable completion.

## Versioning and Backward Compatibility

### Version Output

`tool version` or `tool --version` prints exactly:

```text
ethos v2.6.1
```

No build dates, commit hashes, or Go versions unless `--verbose` is set.
Parseable output matters -- scripts extract version strings.

### Semantic Versioning for CLIs

- **Patch**: bug fixes, no behavior change.
- **Minor**: new subcommands, new flags, new output fields. Existing
  behavior unchanged.
- **Major**: removed subcommands, changed flag semantics, removed output
  fields, changed exit codes.

### Deprecation

When removing a flag or changing behavior:

1. Add a deprecation warning to stderr when the old flag is used.
2. Keep the old flag working for at least one minor version.
3. Document the migration in the changelog.

```text
Warning: --output-format is deprecated, use --format instead.
         --output-format will be removed in v3.0.
```

Never remove a flag without a deprecation cycle. Scripts depend on flag
names.

## Testing CLIs

### Table-Driven Command Tests

Test the CLI as a black box. Each test case is an invocation with expected
stdout, stderr, and exit code:

```go
tests := []struct {
    name     string
    args     []string
    stdin    string
    env      map[string]string
    wantOut  string
    wantErr  string
    wantCode int
}{
    {
        name:     "list with no identities",
        args:     []string{"identity", "list"},
        wantOut:  "",
        wantCode: 0,
    },
    {
        name:     "show missing identity",
        args:     []string{"identity", "show", "nobody"},
        wantErr:  "identity \"nobody\" not found",
        wantCode: 1,
    },
    {
        name:     "invalid flag",
        args:     []string{"--bogus"},
        wantErr:  "unknown flag: --bogus",
        wantCode: 2,
    },
}
```

### Golden File Tests

For complex output (help text, formatted tables, JSON responses), write the
expected output to a file and compare:

```go
got := runCommand(t, args)
golden := filepath.Join("testdata", testName+".golden")
if *update {
    os.WriteFile(golden, got, 0o644)
}
want, _ := os.ReadFile(golden)
assert.Equal(t, string(want), string(got))
```

Run with `-update` to refresh golden files after intentional changes.

### Integration Tests

Test the actual binary, not just the command functions:

```go
cmd := exec.Command("./ethos", "identity", "show", "mal")
cmd.Env = append(os.Environ(), "ETHOS_CONFIG=/dev/null")
out, err := cmd.CombinedOutput()
```

This catches flag parsing, init sequences, and real I/O that unit tests
miss.

### Testing Stdin

Pipe test data through stdin to verify pipeline behavior:

```go
cmd := exec.Command("./ethos", "validate")
cmd.Stdin = strings.NewReader(`{"name": "mal"}`)
```

### Anti-Patterns

- Testing only the happy path. Every error branch needs a test.
- Testing internal functions instead of the command interface. If the user
  cannot trigger it from the command line, it is an implementation detail.
- Not testing exit codes. A test that checks stdout but ignores the exit
  code is half a test.
- Hand-maintained golden files that nobody updates. Use the `-update` flag
  pattern.
- Testing with `os.Args` manipulation instead of building the command
  properly. Use `exec.Command` or a test harness that resets state.

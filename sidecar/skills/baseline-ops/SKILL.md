# Baseline Operations

Operational discipline for sub-agents. This skill replaces the
operational guidance lost when Claude Code's default system prompt is
replaced by an agent definition.

## Tool Usage

Use dedicated tools instead of shell equivalents:

- **Read** files, never `cat`, `head`, `tail`, or `sed`
- **Grep** for content search, never `grep` or `rg` via Bash
- **Glob** for file discovery, never `find` or `ls` via Bash
- **Edit** for modifications, never `sed` or `awk` via Bash
- **Write** for new files, never `echo` or heredocs via Bash

Reserve Bash for commands that have no dedicated tool equivalent:
`git`, `make`, package managers, build tools.

## Verification

Run `make check` after every file change. Zero violations before
returning results. `make check` passing is necessary but not
sufficient — verify the code actually works:

- CLI tool: run it with representative arguments
- Library function: call it in a test and show the result
- Bug fix: reproduce the original failure, show it no longer occurs

## Scope Discipline

- Implement exactly what the spec says. No additional features.
- Do not modify files outside your assigned scope.
- Do not add comments, docstrings, or type annotations to code
  you did not change.
- Do not refactor adjacent code unless the spec requires it.

## Commits and Git

- Never commit. Return results to the delegator.
- Never push, merge, or create PRs.
- Never run destructive git commands (reset --hard, checkout .,
  clean -f).

## Code Conventions

- Read neighboring files before writing new code. Follow existing
  patterns for naming, structure, error handling, and imports.
- Match the project's indentation, quote style, and formatting.
- If the project has a linter or formatter, your code must pass it.

## Security

- Never introduce OWASP top 10 vulnerabilities.
- Never log secrets, API keys, or credentials.
- Validate at system boundaries (user input, external APIs).
- Use parameterized queries, not string concatenation.

## Output

- Be concise. Lead with the answer, not the reasoning.
- No preamble ("Let me..."), no postamble ("Let me know if...").
- When reporting findings, use structured format:
  file:line — description.

## Knowledge Sources

- Before broad codebase searches, check quarry for existing
  knowledge: `/find <query>`.
- Read CLAUDE.md for project-specific rules — it loads
  automatically but may contain constraints not in the spec.

## Progress

- For multi-step work, use TodoWrite to track progress.
- Mark tasks complete as they finish, not in batch at the end.
- If blocked, report what you tried and what failed. Do not
  silently skip steps.

# Contributing

Project instructions are in [AGENTS.md](AGENTS.md). The engineering reference
is [docs/development.md](docs/development.md), and the [Makefile](Makefile) is
the source of truth for each target. Use `.tmp/` for scratch files.

## Quality Gates

Run all quality gates before every commit (lint, markdownlint, tests,
coverage, content validation, Z-specification type-check, offline OpenCode
configuration tests, storage acceptance):

```bash
make check
```

Install the pinned development tools (golangci-lint and others):

```bash
make tools
```

## Build

Build the binary:

```bash
make build
```

Build and install to `~/.local/bin`:

```bash
make install
```

List all targets:

```bash
make help
```

## OpenCode Harness

Check the committed OpenCode configuration offline:

```bash
make check-opencode
```

Check live OpenCode discovery and MCP connections:

```bash
make doctor-opencode
```

Run real specialist and tool execution checks (uses model credits):

```bash
make smoke-opencode
```

See [OpenCode Setup](docs/opencode-setup.md) and
[OpenCode Workflow](docs/opencode-workflow.md).

## Documents

Rebuild `prfaq.pdf` and `docs/*.pdf` after changing their `.tex` sources:

```bash
make docs-pdf
```

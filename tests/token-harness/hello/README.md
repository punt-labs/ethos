# L6 token-harness hello world

Smallest end-to-end proof that L6 payload/token profiling (see
`docs/testing-strategy.tex` §"Level 6 --- Payload/Token Profiling"
and `docs/testing-roadmap.md` §"Phase 6") is buildable exactly as
designed. This directory is the seed the full harness will grow from.

## What this proves

1. **LiteLLM proxy** serves Claude Code at Anthropic's Messages
   endpoint (`/v1/messages`) with only a Python install and a
   subprocess spawn — no external network dependency, no Anthropic
   API key required, no workstation-specific setup. Works in CI.

2. **`mock_response` at proxy level** returns a canned assistant
   turn (verified on `litellm==1.81.9`). Claude Code accepts it,
   populates its own `modelUsage` envelope, and exits cleanly. Zero
   Anthropic tokens consumed per scenario run.

3. **`CustomLogger.async_post_call_success_hook`** captures the full
   `proxy_server_request.body` — the exact JSON Claude Code POSTed
   — to a per-request `.jsonl` file in `TOKEN_CAPTURE_DIR`. That
   body includes the assembled system prompt (billing header, SDK
   identity, main system prompt), the full tool list with schemas,
   and the message history. Everything L6 attribution needs.

4. **Attribution surface is enumerable**. The `run.sh` script prints
   a rough char-count breakdown per captured body:

   ```text
   [hello] first capture size 437,517 bytes
   [hello]   system prompt: 8,127 (1%)
   [hello]   tool schemas: 181,334 (41%) — 135 tools
   [hello]   messages: 178,796 (40%) — 2 turns
   ```

   Bytes are not tokens, but the shape is what the tokenizer +
   attribution parser will slice.

## Assertions

`run.sh` fails the run (exit 1) on any of these. This is what makes
it a real CI test, not just an integration smoke check.

### Structural

Every capture body must contain `model` and `messages` keys — the
minimum Anthropic Messages API shape. A capture without them means
the proxy misparsed the request or the callback received the wrong
dict.

### Payload-size ratchet

Total capture size must stay under `MAX_TOTAL_BYTES = 700 KB`.
Baseline captured 2026-08-15 (Claude Code 2.1.220, bare invocation,
no ethos content) was ~437 KB with 135 tool schemas. The ceiling
gives ~60% headroom over baseline. If your change grows the
payload past 700 KB, CI turns red — either you have a real
regression (drove context up), or you added a legitimate feature
that costs bytes and should walk the ratchet down after the change
lands. Do NOT quietly raise the ceiling; the ratchet only tightens.

Phase 6's full framework will replace this single scalar with
per-scenario baselines and per-source attribution (see
`docs/testing-strategy.tex` §"Level 6"). For hello world one
number is enough.

### PII / secret-leak absence

No credential-shaped substring may appear in a capture. The
`run.sh` matcher looks for:

| Pattern | What it catches |
|---|---|
| `sk-live-[A-Za-z0-9]{16,}` | Anthropic/OpenAI live keys |
| `sk-ant-api03-[A-Za-z0-9_\-]{20,}` | Anthropic API keys |
| `AKIA[0-9A-Z]{16}` | AWS access keys |
| `AIza[0-9A-Za-z_\-]{35}` | Google API keys |
| `ghp_[A-Za-z0-9]{36,}` | GitHub personal tokens |
| `ghs_[A-Za-z0-9]{36,}` | GitHub server-to-server tokens |
| `github_pat_[A-Za-z0-9_]{80,}` | GitHub fine-grained PATs |
| `-----BEGIN [A-Z ]*PRIVATE KEY-----` | PEM private key blocks |
| `xox[baprs]-[A-Za-z0-9\-]{10,}` | Slack tokens |

A hit means either the mock scenario itself is tainted (fix the
scenario) or the client is leaking something from the environment
into the prompt (much worse — Phase 6's attribution parser will
point at the source). Patterns are conservative on purpose: false
positives here are cheaper than false negatives.

## Running

```bash
# One-time install (pinned; see "Known issue" below).
pip install 'litellm[proxy]==1.81.9'

# Run the hello world. Assumes `claude` on PATH.
tests/token-harness/hello/run.sh

# Or via the Makefile shortcut:
make test-tokens-hello
```

## CI

Runs on every push and pull request via the `token-harness-hello`
job in `.github/workflows/test.yml`. Job installs Python 3.12,
`litellm[proxy]==1.81.9`, and `@anthropic-ai/claude-code`, then
runs `make test-tokens-hello`. Zero Anthropic tokens consumed
(the proxy's `mock_response` returns canned data). On failure,
the job uploads `.tmp/token-harness/hello/` and
`.tmp/token-captures/hello/` as artifacts for post-mortem.

Outputs land under `.tmp/` (gitignored):

- `.tmp/token-harness/hello/litellm.log` — proxy startup + capture-logger trace
- `.tmp/token-harness/hello/claude.json` — Claude Code's result envelope
- `.tmp/token-captures/hello/capture-<ns>.jsonl` — one file per captured request

## Files in this directory

- **`litellm.yaml`** — proxy config. Declares `mock-anthropic` model
  with `mock_response`, registers `custom_callbacks.token_capture`.
- **`custom_callbacks.py`** — `TokenCaptureLogger` (~30 lines)
  implementing `async_post_call_success_hook`.
- **`run.sh`** — starts proxy, waits for it to listen, invokes
  `claude --print --output-format json --max-turns 1` with
  `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN` cage-pointed at the
  proxy, verifies at least one capture landed with the expected
  body shape, prints the attribution preview, tears down the proxy.

## Known issue: pin `litellm==1.81.9`

The latest LiteLLM release imports `get_flat_dependant` from
`fastapi.dependencies.utils`, which was removed in a recent FastAPI
version. The proxy fails to boot on Python 3.12+ with:

```text
ImportError: cannot import name 'get_flat_dependant' from
    'fastapi.dependencies.utils'
```

Version `1.81.9` boots cleanly. Pin it. Revisit when a newer
LiteLLM restores the import (or pins its own FastAPI ceiling).

## Not proven yet (Phase 6 open work)

- **Tokenizer**. This hello world reports characters, not tokens.
  Phase 6 wires a local Claude-compatible tokenizer (Anthropic
  first-party if it ships one, else calibrated `tiktoken`).
- **Attribution parser**. Char percentages here are a preview. The
  full parser (`cmd/token-attribute/`) slices the system prompt by
  ethos-injected markers, groups tools by MCP server, and emits the
  full attribution tree.
- **Fixture scenarios**. `run.sh` runs one bare Claude Code
  invocation. The scenario matrix (`tests/token-scenarios/*.yaml`)
  will replay against known repo/team configurations —
  `empty-repo`, `ethos-single-agent`, `ethos-team-submodule`,
  `ethos-vendored-minimal`, `ethos-rich-mcp`, `ethos-post-compact`.
- **Baseline comparison**. Phase 6 commits
  `tests/token-baselines/<scenario>.json` and flags deltas > 5%.
- **CI wiring**. Per-release GitHub Actions job runs
  `make test-tokens` against baselines.

See bead `ethos-l90t` for the full Phase 6 scope, and
`docs/testing-strategy.tex` §"Level 6" for the design.

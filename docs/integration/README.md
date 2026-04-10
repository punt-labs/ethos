# Integration Guides

How third-party tools integrate with ethos. Three patterns, any
coupling level. Pick the one that fits your architecture.

| Pattern | Dependency | Best for |
|---------|-----------|----------|
| [Filesystem](filesystem.md) | None | Optional identity enrichment without coupling |
| [CLI](cli.md) | `ethos` binary on PATH | Hooks, scripts, CI pipelines |
| [MCP](mcp.md) | `ethos` binary on PATH | Structured operations during a session |

Every pattern follows the same principle: **if ethos is absent, your
tool works fine without it.** Integration is enrichment, not
dependency. Check for ethos first, use it if present, skip if not.

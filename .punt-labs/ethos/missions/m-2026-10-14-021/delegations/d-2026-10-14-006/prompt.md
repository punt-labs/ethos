You are the worker on ethos mission m-2026-10-14-021. Read it: `ethos mission show m-2026-10-14-021`.

Your deliverable already exists — this is a scoped correction, not new research. Work in the existing worktree at `<repo>/.claude/worktrees/agent-adc92778847144e24/` (this is the same file a prior mission, now closed, wrote across two rounds). Edit `docs/prfaq-opencode-integration.md` in place there.

## Context on why this correction is needed

OpenCode V2 is released (confirmed: v2.0.26 installed locally, in production use on multiple operator machines — NOT dev-branch-only, that was an earlier wrong claim already retracted via `ethos mission correct`). The existing document was written citing V1 plugin hook syntax throughout, sourced from `opencode.ai/docs/plugins/` (the stable site, which still documents V1 behind a "New OpenCode v2 is now available" banner). V2 replaced the entire hook-registration mechanism. This mission updates the hook vocabulary only.

## The V2 API, confirmed directly via context7 against `anomalyco/opencode`'s `migrate-v1.mdx`

```typescript
import { Plugin } from "@opencode/plugin"

export default Plugin.define({
  id: "example-plugin",
  async setup(ctx) {
    // Session-level hooks
    await ctx.session.hook("context", (event) => {
      event.system.push({ type: "text", text: "..." })
      event.options.temperature = 0.2
    })
    await ctx.session.hook("prompt", (event) => { /* ... */ })
    await ctx.session.hook("compaction", (event) => { /* ... */ })
    await ctx.session.hook("model.request", (event) => {
      event.headers["x-plugin"] = "..."
    })

    // Tool hooks
    await ctx.tool.hook("execute.before", (event) => { /* event.tool, event.input */ })
    await ctx.tool.hook("execute.after", (event) => { /* ... */ })

    // Shell hooks
    await ctx.shell.hook("create.before", (event) => { event.env.X = "y" })

    // Permission hooks
    await ctx.permission.hook("evaluate", (event) => { /* ... */ })

    // Event subscription (replaces bare "event" hook)
    const controller = new AbortController()
    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (event.type === "session.idle") { /* ... */ }
      }
    })()
    return () => controller.abort() // cleanup, replaces V1 "dispose"
  },
})
```

Full V1→V2 migration table (for anything else you encounter in the existing doc):
| V1 | V2 |
|---|---|
| `event` | `ctx.event.subscribe()` |
| `dispose` | cleanup function returned by `setup` |
| `tool` map | `ctx.tool.transform(...)` |
| `chat.message` | `ctx.session.hook("prompt", ...)` |
| `chat.params` | `ctx.session.hook("context", ...)` |
| `chat.headers` | `ctx.session.hook("model.request", ...)` or `"http.request"` |
| `permission.ask` | `ctx.permission.hook("evaluate", ...)` |
| `tool.execute.before` | `ctx.tool.hook("execute.before", ...)` |
| `tool.execute.after` | `ctx.tool.hook("execute.after", ...)` |
| `shell.env` | `ctx.shell.hook("create.before", ...)` |

Critical semantic note from the migration doc, worth preserving if relevant to session-lifecycle timing claims: "the V2 `prompt` hook runs before durable prompt admission, while `context` runs immediately before an agent model request." For OpenCode plugin EVENT names fired per session lifecycle (`session.created`, `session.idle`, etc. — these are `ctx.event.subscribe()` event *types*, not hook registration points), those event type strings themselves are unaffected by the V1/V2 hook-registration change — only HOW a plugin subscribes to them changed (bare top-level `event` hook key → `ctx.event.subscribe()`).

## What to fix, specifically

1. **"Why not reuse the Claude Code hook mechanism verbatim?" FAQ** — the code description of OpenCode's extension mechanism (currently describes the V1 `async ({project, client, $, directory, worktree}) => Hooks` factory and V1 hook-name object keys) needs to become `Plugin.define({id, setup(ctx)})` with the actual hook calls this integration would register (most likely `ctx.event.subscribe()` for `session.created`/`session.updated`/`session.idle` events, since those are lifecycle event TYPES, and/or `ctx.session.hook(...)` if a more targeted session-lifecycle hook applies — use your judgment on which V2 primitive best fits "fire when a session starts/updates so the plugin can call `ethos mission claim`").
2. **"What happens if OpenCode forks a session mid-mission?" FAQ** — keep the `session.fork` HTTP endpoint content and the `context.data.session.root`/`.family` lineage-lookup content exactly as-is (both confirmed correct and unaffected by V1/V2). Only fix the HOOK REGISTRATION syntax around it — wherever the FAQ describes "the plugin's `session.created` hook fires," make sure the described registration mechanism is V2 (`ctx.event.subscribe()` or `ctx.session.hook`), not the V1 object-key shape.
3. **"What happens on ethos service restart?" FAQ** — keep the `context.data.session.status(sessionID)` liveness-check content exactly as-is (confirmed correct, unaffected). Fix only the surrounding hook-registration description if it uses V1 syntax.
4. **The bundled-plugin recommendation section** — update the description of "how the plugin would be structured" to `Plugin.define({id, setup(ctx)})`, not the old factory-function export.
5. **Add a brief note** (a sentence or two, not a new section) somewhere appropriate — probably near the top of the plugin-mechanism discussion or in the confidence/verification section — stating that the document targets OpenCode V2 (confirmed released, v2.0.26), citing `migrate-v1.mdx`'s V1→V2 migration table as the source for the hook API shown, and that an earlier draft incorrectly used V1 syntax sourced from the stable docs site before V2's release was confirmed.

## What NOT to touch

Do not touch: the `session.fork` endpoint facts, the `context.data.session.{root,family,status,...}` citations, the four exemplars section, the non-goals section, the plugin-vs-adapter-vs-config trade-off reasoning, the press release, or the worktree/concurrency FAQ's substance (only fix V1 hook syntax if any appears there). All of that was independently verified correct across two prior evaluation rounds. This is a terminology/code-sketch correction, not a rewrite — minimize the diff.

Run markdownlint on your result before submitting. Submit via `ethos mission result` when done.
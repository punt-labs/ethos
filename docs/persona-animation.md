# Persona Animation

How ethos transforms a static identity declaration into consistent agent behavior
across an entire Claude Code session.

## Problem

Ethos declares *who* an agent is — personality, writing style, talents, role. But
declaration alone doesn't produce consistent behavior. The persona must be
**animated**: injected, reinforced, and expressed through every tool surface Claude
Code offers.

Today, ethos injects a one-line identity confirmation at SessionStart:
`"Ethos session started. Active identity: Claude Agento (claude)."` That's a
name tag, not a persona. The personality, writing style, and talents — the actual
behavioral instructions — are never injected into the session. They exist only in
files on disk.

### What breaks without animation

| Failure mode | Cause |
|-------------|-------|
| Agent ignores personality after compaction | Persona context was in early turns, now compressed away |
| Subagent acts generically despite having an identity | SubagentStart joins the roster but injects zero behavioral context |
| Writing style drifts over long sessions | No reinforcement mechanism after initial injection |
| Different tools produce different voices | CLI output follows CLAUDE.md, spoken output follows vox config, neither reads ethos personality |
| Agent can't recall its own talents | Talent content never loaded into context — only slugs are in YAML |

## Current State

### What exists

| Surface | What ethos does | Gap |
|---------|----------------|-----|
| SessionStart hook | Resolves identity, creates session roster, emits name + handle | Does not inject personality, writing style, or talent content |
| SubagentStart hook | Joins subagent to roster, auto-matches persona by agent_type | Does not inject persona content into the subagent's context |
| Agent definitions (.claude/agents/*.md) | Reference ethos identity, contain principles and working style | Must manually call `ethos show <handle> --json` to load identity |
| MCP tools | Expose identity data (get, whoami, list) | Read-only — no mechanism to inject into session context |
| CLAUDE.md | Contains communication rules that overlap with writing style | Hardcoded, not derived from ethos identity |

### What doesn't exist

1. **Persona injection** — loading personality + writing style + talent content
   into the session as behavioral instructions
2. **Compaction survival** — re-injecting persona context when the context window
   is compressed
3. **Subagent persona injection** — giving subagents their behavioral context
   at spawn time
4. **Cross-surface consistency** — ensuring CLI output, MCP responses, spoken
   output, and written messages all reflect the same persona
5. **Persona verification** — a way to check whether the agent is actually
   following its persona

## Design

### Layer 1: Injection (SessionStart)

The SessionStart hook already resolves identity and creates the session. Extend
it to also load and inject the persona content.

**What to inject:**

```text
You are Claude Agento (claude), COO / VP Engineering for Punt Labs.

## Personality
<contents of personalities/friendly-direct.md>

## Writing Style
<contents of writing-styles/direct-with-quips.md>

## Talents
<list of talent slugs -- content available via /ethos:talent show>
```

This replaces the current one-line message with a structured persona block.
The personality and writing style are the behavioral instructions — they tell the
agent *how* to think and *how* to write. Talents are listed as slugs (not full
content) to stay within context budget; full talent content is available on demand
via the MCP tool.

**Implementation:** Modify `HandleSessionStart` to load the resolved identity
with `Reference(false)` (full content resolution), then assemble the persona
block from the identity's personality, writing style, and talent slugs.

**Context budget:** Personality + writing style are typically 30-60 lines each.
The full persona block would be ~100-150 lines. This is comparable to what
`explanatory-output-style` injects (which we just measured works fine).

### Layer 2: Reinforcement (PreCompact)

When the context window is compressed, earlier turns are summarized. The persona
block from SessionStart gets folded into a few tokens of summary — losing the
behavioral instructions.

**Mechanism:** Register a `PreCompact` hook that re-emits the persona block as
`additionalContext`. Claude Code's compaction will include this in the compressed
context, preserving the behavioral instructions.

```json
{
  "PreCompact": [
    {
      "hooks": [{
        "type": "command",
        "command": "${CLAUDE_PLUGIN_ROOT}/hooks/pre-compact.sh"
      }]
    }
  ]
}
```

The hook calls `ethos hook pre-compact`, which resolves the current session's
primary agent persona and emits the same persona block as SessionStart.

**Key constraint:** PreCompact context should be concise. If the full persona
block is too large, emit a condensed version: personality name + 3-5 key
behavioral rules + writing style name + 3-5 key writing rules.

### Layer 3: Delegation (SubagentStart)

When a subagent spawns (e.g., bwk for Go work, mdm for CLI work), ethos already
auto-matches the persona if the agent_type matches an identity handle. But it
injects nothing — it only updates the roster.

**Extend SubagentStart** to emit persona context for the subagent:

```text
You are Brian K (bwk), Go specialist on the Punt Labs engineering team.

## Personality
<contents of personalities/kernighan.md>

## Writing Style
<contents of writing-styles/kernighan-prose.md>
```

This way, subagents get their persona injected at spawn time without needing to
manually call `ethos show` in their agent definition. The agent .md file can
focus on *what* to do (tools, scope, principles) while ethos handles *who* to be.

**Agent definition simplification:** With persona injection at SubagentStart,
the `.claude/agents/bwk.md` file no longer needs the "Load your identity with
`ethos show bwk --json`" instruction. The identity is already in context.

### Layer 4: Expression (Cross-Surface Consistency)

The persona should produce consistent behavior across all output surfaces:

| Surface | How persona is expressed |
|---------|------------------------|
| **Conversation text** | Writing style governs tone, structure, word choice |
| **Spoken output (vox)** | Voice selection from ext/vox, mood from personality temperament |
| **Email (beadle)** | Writing style for body, identity for signature/attribution |
| **Git commits** | Writing style for commit messages (concise, data-driven) |
| **Code review comments** | Personality for tone (direct, not harsh), writing style for structure |
| **Biff messages** | Personality for team communication style |

Ethos doesn't need to control each surface directly. The persona block injected
at SessionStart tells the agent how to behave — the agent then applies that
behavior to whatever surface it's using. Vox reads voice config from ext/vox.
Beadle reads email identity from the identity YAML. The behavioral rules come
from the personality and writing style content in the session context.

**One exception:** Subagents spawned by tools (not by the Agent tool) may not
receive SubagentStart hooks. For these, the delegating agent should include the
persona expectation in the task description.

### Layer 5: Verification

How do you know the persona is being followed?

**Passive verification:**

- Vox: the voice matches the ext/vox config
- Git: commits are attributed to the identity's git config
- Session roster: `/ethos:session` shows correct persona assignments

**Active verification (future):**

- A PostToolUse or Stop hook that samples agent output and checks it against
  writing style rules (e.g., sentence length, banned patterns, confidence
  calibration)
- A `/ethos:check` command that runs a lightweight persona audit on the last
  N turns

Active verification is not in scope for the initial implementation. Passive
verification is already working.

## Implementation Sequence

| Phase | What | Hook | Priority |
|-------|------|------|----------|
| 1 | Inject full persona at SessionStart | SessionStart | High — most impact |
| 2 | Re-inject persona at compaction | PreCompact | High — prevents drift |
| 3 | Inject subagent persona at spawn | SubagentStart | Medium — improves delegation |
| 4 | Remove manual `ethos show` from agent definitions | Agent .md files | Low — cleanup |
| 5 | Active persona verification | PostToolUse / Stop | Future |

Phases 1-3 are the core mechanism. Phase 4 is cleanup. Phase 5 is a future
enhancement that depends on observing whether phases 1-3 are sufficient.

## What This Does NOT Do

- **Override CLAUDE.md** — the persona is additive context, not a replacement
  for project-level instructions. CLAUDE.md wins on conflicts.
- **Change the agent definition format** — .claude/agents/*.md files remain
  the same. Ethos adds context alongside them, not instead of them.
- **Require ethos** — if ethos is not installed, no persona is injected. The
  agent works normally with whatever context CLAUDE.md and the agent .md provide.
  Ethos is a sidecar — it enriches, never blocks.
- **Inject talent content** — talents are listed as slugs. Full content is
  available on demand. Injecting all talent content would blow the context budget
  for agents with many talents.

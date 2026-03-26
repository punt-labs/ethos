# Build Plan: Persona Animation

Epic bead: ethos-qwr
Design doc: [docs/persona-animation.md](persona-animation.md)

---

## Phase 1: SessionStart persona injection

**Goal:** Replace the one-line "Active identity: Claude Agento (claude)" with a
full persona block containing personality content, writing style content, and
talent slugs.

### Changes

**`internal/hook/session_start.go`** -- `HandleSessionStart`

- After resolving identity, load with `Reference(false)` to get resolved
  personality and writing style content
- Build persona block:

  ```text
  You are {Name} ({Handle}), {personality first line}.

  ## Personality
  {personality content}

  ## Writing Style
  {writing style content}

  ## Talents
  {comma-separated talent slugs}
  ```

- Emit as `additionalContext` (replaces current one-line message)
- If identity has no personality or writing style, fall back to current one-line
  format (graceful degradation)

**`internal/hook/session_start_test.go`**

- Test: identity with personality + writing style -> full persona block emitted
- Test: identity with no personality -> falls back to one-line message
- Test: identity with personality but no writing style -> partial block
- Test: no identity resolved -> no output (existing behavior)

**`internal/identity/store.go`** — verify `Load` with `Reference(false)` returns
resolved content for personality and writing style (should already work)

### Acceptance criteria

- `make check` passes
- `ethos hook session-start` with valid stdin emits full persona block
- Restart Claude Code → persona block appears in session context
- After this phase, writing style and personality are in every new session

### Delegate to: bwk

---

## Phase 2: PreCompact persona reinforcement

**Goal:** Re-inject the persona block before context compression so behavioral
instructions survive compaction.

### Changes

**`internal/hook/pre_compact.go`** -- new file

- `HandlePreCompact` reads stdin (PreCompact payload), resolves current session's
  primary agent persona, loads identity with full content, emits condensed persona
  block as `additionalContext`
- Condensed format (shorter than SessionStart -- budget-conscious):

  ```text
  Active persona: {Name} ({Handle})
  Personality: {personality name} -- {first 3 behavioral rules}
  Writing: {writing style name} -- {first 3 writing rules}
  Talents: {comma-separated slugs}
  ```

**`internal/hook/pre_compact_test.go`**

- Test: valid session -> condensed persona block emitted
- Test: no session -> no output
- Test: session with no persona -> no output

**`cmd/ethos/hook.go`** -- add `pre-compact` subcommand

- Calls `HandlePreCompact` with stdin, store, session store

**`hooks/pre-compact.sh`** -- new thin gate script

- Same pattern as session-start.sh: check kill file, check ethos dir, check
  binary, delegate to `ethos hook pre-compact`

**`hooks/hooks.json`** -- add PreCompact entry

```json
"PreCompact": [
  {
    "hooks": [{
      "type": "command",
      "command": "${CLAUDE_PLUGIN_ROOT}/hooks/pre-compact.sh"
    }]
  }
]
```

### Acceptance criteria

- `make check` passes
- `ethos hook pre-compact` emits condensed persona block
- After compaction, persona context persists in compressed conversation
- No persona injection if ethos identity is not resolved

### Delegate to: bwk

---

## Phase 3: SubagentStart persona injection

**Goal:** When a subagent spawns and auto-matches a persona (e.g., bwk agent
gets the bwk identity), inject that persona's behavioral content so the subagent
doesn't need to manually call `ethos show`.

### Changes

**`internal/hook/subagent_start.go`** -- `HandleSubagentStart`

- After auto-matching persona and joining session, if persona was matched:
  load identity with `Reference(false)`, build persona block, emit as
  `additionalContext`
- Same format as SessionStart persona block but prefixed with role context:

  ```text
  You are {Name} ({Handle}), {personality first line}.
  You report to {parent persona name} ({parent handle}).

  ## Personality
  {personality content}

  ## Writing Style
  {writing style content}
  ```

- If no persona matched (generic subagent), emit nothing (existing behavior)

**`internal/hook/subagent_start_test.go`**

- Test: subagent with matched persona -> full persona block with parent context
- Test: subagent with no matching persona -> no output
- Test: subagent with persona but no personality file -> graceful fallback

**`.claude/agents/bwk.md`** — remove "Load your identity" instruction
**`.claude/agents/mdm.md`** — remove "Load your identity" instruction

### Acceptance criteria

- `make check` passes
- Spawning bwk agent → persona block appears in subagent context
- Spawning generic agent (no matching identity) → no persona injection
- Agent .md files no longer need manual identity loading instructions

### Delegate to: bwk

---

## Shared work

**`internal/hook/persona.go`** -- new file, shared by all 3 phases

- `BuildPersonaBlock(id *identity.Identity) string` -- full persona block
- `BuildCondensedPersona(id *identity.Identity) string` -- compact version for
  PreCompact
- Both functions handle nil personality, nil writing style, empty talents
  gracefully

### Tests

**`internal/hook/persona_test.go`**

- Test: full identity -> complete persona block
- Test: identity with no personality -> block without personality section
- Test: identity with no writing style -> block without writing style section
- Test: identity with no talents -> block without talents line
- Test: condensed format respects line limits

---

## Sequence

1. Shared persona builder (`persona.go` + tests) — delegate to bwk
2. Phase 1 (SessionStart) — delegate to bwk, depends on 1
3. Phase 2 (PreCompact) — delegate to bwk, depends on 1
4. Phase 3 (SubagentStart) — delegate to bwk, depends on 1
5. Agent .md cleanup — can be done in same PR as phase 3

Phases 2 and 3 are independent of each other and can be parallelized after
phase 1.

---

## Verification

After all 3 phases:

1. Start a new Claude Code session → full persona block in context
2. Work until compaction fires → persona survives in compressed context
3. Spawn bwk subagent → bwk gets its persona injected automatically
4. Check `/ethos:session` → all participants have correct personas
5. Observe output matches writing style (data over adjectives, short sentences)

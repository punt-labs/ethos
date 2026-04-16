# PR/FAQ: Ethos Onboarding -- Working Team in 60 Seconds

## Press Release

**Punt Labs ships `ethos setup` -- go from install to a working
human-agent team in one command.**

Developers using Claude Code lose 15-20 minutes per project
configuring agent identities and writing config files by hand. Most
give up and use Claude Code anonymous -- no persistent personality,
no structured delegation, no audit trail.

Today, Punt Labs announces `ethos setup`, an interactive command that
creates a working team in under 60 seconds. One command asks your
name, picks a team bundle, creates human and agent identities, wires
up the repo config, and activates the team. The next time Claude Code
starts, the agent knows who it is, who you are, and how to delegate.

Ethos ships a new **foundation** bundle alongside gstack. Foundation
is a 4-agent general-purpose team (architect, implementer, reviewer,
security) with 3 pipeline templates. It works for any codebase.
Gstack remains available for the full 6-agent startup builder
philosophy.

"I installed ethos, ran `ethos setup`, and had structured delegation
working on my Rails project in under a minute," said Priya Chandran,
a senior engineer. "Before this, I spent a full afternoon reading docs
and still didn't have a working team."

Ethos is free, open-source, and runs entirely on your machine.
Install: `curl -fsSL https://punt-labs.com/ethos/install.sh | sh`.
Then run `ethos setup`.

---

## FAQ -- External (User-Facing)

### 1. What does `ethos setup` actually do?

It runs an interactive wizard that:

1. Asks your name, handle, and preferred working style (3 questions)
2. Creates your human identity at `~/.punt-labs/ethos/identities/<handle>.yaml`
3. Creates a paired agent identity (defaults to `claude`)
4. Writes `.punt-labs/ethos.yaml` in your current repo with `agent: claude`
5. Activates the **foundation** team bundle (or gstack, if you choose it)
6. Generates `.claude/agents/*.md` files for your team

One command. No YAML editing. No reading docs first.

### 2. What is the foundation bundle? How is it different from gstack?

**Foundation** is a 4-agent general-purpose team designed to work on
any codebase:

| Agent | Role | What it does |
|-------|------|-------------|
| architect | architect | Reviews designs, evaluates tradeoffs |
| implementer | implementer | Writes code, runs tests |
| reviewer | reviewer | Reviews code, reports findings |
| security | security-reviewer | Checks for vulnerabilities |

It ships with 3 pipelines: `standard` (design-implement-test-review-
document), `quick` (implement-review), and `product` (prfaq-design-
implement-test-review-document).

**Gstack** is a 6-agent team with an opinionated startup builder
philosophy ("Boil the Lake", "Search Before Building"). It adds a
QA engineer and product lead, and ships 5 custom pipelines for
planning, shipping, debugging, design, and multi-perspective review.

Pick foundation unless your team already follows the gstack workflow.
You can switch at any time with `ethos team activate <bundle>`.

### 3. I already have ethos installed. Can I run `ethos setup`?

Yes. It detects existing identities and skips what is already there.
If you have a human identity but no agent, it creates the agent. If
you have identities but no active bundle, it activates one. It never
overwrites existing files.

### 4. What does "minute one vs. day seven" look like?

**Minute one**: Run `ethos setup`. Start Claude Code. The agent has a
name, personality, and writing style. It remembers who it is across
sessions and through context compaction. You can delegate to
sub-agents and they each get their own persona.

**Day three**: You run your first pipeline. `ethos mission pipeline
instantiate standard --var feature=auth --var target=internal/auth/`
creates 5 linked missions that flow from design through documentation.

**Day seven**: You customize. Override an agent's personality for your
project. Add a domain-specific talent file. Create a custom pipeline
for your team's review process.

The progression is: use the defaults, then customize what matters.

### 5. What if I don't want a team -- just identity for my main agent?

Run `ethos setup --solo`. This creates your human identity and one
agent identity with no team bundle. The agent gets personality and
writing style. You skip roles, pipelines, and structured delegation.
You can activate a team later when you want more.

### 6. Does this work with existing projects that use `.punt-labs/ethos/` as a submodule?

Yes. `ethos setup` detects the legacy submodule layout and offers to
migrate it with `ethos team migrate`. If you decline, everything
keeps working -- the two-layer resolver (repo + global) still applies.
The three-layer resolver (repo + bundle + global) activates only when
you set `active_bundle` in your config.

### 7. What languages and project types does the foundation bundle support?

All of them. Foundation agents use language-agnostic roles and
starter talents (code-review, testing, security, documentation). The
implementer role has no language-specific tools restricted -- it uses
Read, Write, Edit, Bash, Grep, and Glob, which work on any codebase.

For language-specific expertise, add talent files. Ethos ships 10
starter talents including Go, Python, and TypeScript.

---

## FAQ -- Internal (Stakeholder-Facing)

### 1. Why invest in onboarding when the product already works?

The product works for people who read the docs front to back and are
willing to create 3-4 files by hand. The install-to-working-team path
is currently 12 steps. Usage data from Punt Labs internal adoption
shows that teams under time pressure skip identity setup entirely and
use bare Claude Code -- losing persistent identity, structured
delegation, and audit trails. Every session that starts anonymous is
a session where ethos provides zero value. The installer runs; the
product does not get used.

Reducing 12 steps to 1 command is not a convenience feature. It is
the difference between a product that gets adopted and a product that
gets installed.

### 2. Why a new bundle instead of making gstack the default?

Gstack embeds a specific philosophy (Boil the Lake, Search Before
Building, User Sovereignty) and a startup-shaped team structure with
6 agents. A solo developer working on a Django app does not need a
product lead or a QA engineer with gstack's opinionated review
pipeline. Shipping gstack as the default tells most users "this
product is not for you" within the first minute.

Foundation is the 80% team: 4 agents that cover the universal
delegation patterns (design, build, review, secure). The pipeline
templates map to how most developers already work. Users who want
more activate gstack or build their own bundle.

### 3. How do we measure whether this worked?

Two metrics:

- **Setup completion rate**: percentage of installs that result in at
  least one identity + one active bundle within 24 hours. Current
  baseline is unmeasured but estimated low based on the manual-config
  dropout pattern. Target: 70% of installs complete setup within the
  first session.

- **First delegation within 7 days**: percentage of users who run at
  least one mission (dispatch or pipeline) within a week of setup.
  This measures whether the team they got is useful enough to
  actually use. Target: 40% of completed setups produce a delegation
  within 7 days.

Both metrics are measurable from local state (identity files, mission
files) without telemetry. `ethos doctor` can report them optionally.

### 4. What does this cost to build?

The `ethos setup` command is a CLI wizard that orchestrates existing
primitives: `identity create`, `team activate`, config file writes,
and `GenerateAgentFiles`. No new packages. The foundation bundle is a
new set of YAML/MD files following the same schema as gstack. The
`--solo` flag is a subset of the full flow.

Estimated scope: 1 new CLI command, 1 new embedded bundle (~20
files), updates to install.sh and README. The mechanism layer is
already built -- this is product surface, not infrastructure.

### 5. What if it does not work -- what is the rollback?

`ethos setup` creates files. It does not modify system state, install
services, or touch existing files. If a user does not like the
result, `ethos team deactivate` removes the bundle, and deleting the
identity YAML files returns to the pre-setup state. There is no
migration, no database, and no cloud state to unwind.

---

## Customer Quote

"I had ethos installed for two weeks before I actually used it. The
Quick Start told me to run `identity create` twice, write a YAML
config file by hand, and then 'optionally' activate a team that
seemed designed for a startup I don't work at. I did none of that.

After the update, I ran `ethos setup` in my repo. It asked my name,
asked if I wanted the foundation team or gstack, and 40 seconds later
I had 4 agents with real personalities showing up in my Claude Code
session. That afternoon I ran `ethos mission pipeline instantiate
quick` on a bug fix and the reviewer agent caught a nil-pointer edge
case I would have shipped. Total time from zero to catching a real
bug: about 3 hours, most of which was my actual work."

-- Priya Chandran, Senior Engineer, 3-person backend team

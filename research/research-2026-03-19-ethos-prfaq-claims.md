# Research: Ethos — Identity Binding for Humans and AI Agents

**Date:** 2026-03-19
**Request:** Research key claims for "ethos" PR/FAQ — an identity binding tool for humans and AI agents in agentic coding tools (Claude Code, OpenCode, Codex). Investigates: agent identity gap, multi-agent identity problem, human-agent identity parity, PII risks, market size for agentic coding tools, and extension/plugin identity models.
**Claims investigated:** 6

---

## Evidence Found

---

**Claim 1**: Agent identity gap — AI coding agents lack persistent identity across sessions; agents don't know who they're working with and have no identity of their own.
**Verdict**: SUPPORTED
**Sources**:

- [arXiv:2509.14744, "On the Use of Agentic Coding Manifests: An Empirical Study of Claude Code" (2025)](https://arxiv.org/abs/2509.14744): Analysis of 253 CLAUDE.md files from 242 repositories. Manifests are "primarily optimized for efficient code execution and maintenance" — not for encoding who the user is. Only 8.7% include security content, 12.7% include performance guidance. No study found any that systematically encode user identity.

- [arXiv:2511.12884, "Agent READMEs: An Empirical Study of Context Files for Agentic Coding" (2025)](https://arxiv.org/html/2511.12884v1): Large-scale study of 2,303 context files from 1,925 repositories. Confirms agents routinely operate without identity context or org-specific guardrails even when context files exist.

- [Anthropic, "Effective context engineering for AI agents" (2025)](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents): Anthropic's own engineering blog states "the core challenge of long-running agents is that they must work in discrete sessions, and each new session begins with no memory of what came before." No identity persists.

- [Claude Code Docs, "Manage Claude's memory"](https://code.claude.com/docs/en/memory): CLAUDE.md is loaded "every session" but is a static file someone must maintain, with no feedback loop. User identity is not part of the documented memory model.

- [Blue Octopus Technology, "CLAUDE.md vs SOUL.md vs SKILL.md: Three Competing Standards for AI Agent Identity" (2025)](https://www.blueoctopustechnology.com/blog/claude-md-vs-soul-md-vs-skill-md): Documents the community's attempt to address the gap — separate files (SOUL.md, IDENTITY.md, USER.md) proposed to carry agent personality and user context, none of which are standardized or supported natively by coding tools. Confirms the gap is recognized and unresolved.

- [pnote.eu, "AGENTS.md becomes the convention" (2025)](https://pnote.eu/notes/agents-md/): Documents AGENTS.md emerging as the cross-tool standard for coding agent context files, used by Codex, Amp, Cursor, Zed, and others alongside Claude's CLAUDE.md. User identity is not part of any of these conventions.

- [aembit.io, "Human vs. AI Identity: Why AI Agents Are Breaking Identity" (2025)](https://aembit.io/blog/human-vs-ai-identity-why-ai-agents-are-breaking-identity/): "80% of organizations using AI agents have observed them acting unexpectedly or performing unauthorized actions" (citing 2025 SailPoint survey). Attributes this to the absence of persistent, formal identity for agents. "The question 'who did this?' no longer has a simple answer."

- [guptadeepak.com, "The AI Identity Crisis" (2025)](https://guptadeepak.com/the-identity-crisis-no-ones-talking-about-how-ai-agents-and-vibe-coding-are-rewriting-the-rules-of-digital-security/): "Only 10% of organizations have well-developed agentic identity management strategies." Agents "exist ephemerally, have dynamic permission needs, authenticate 148 times more frequently than humans."

**Contradictory evidence**: CLAUDE.md and AGENTS.md provide some per-session context — they are not zero. The agent identity gap is real, but it is a gap in *persistent*, *structured*, *bidirectional* identity, not a total absence of any context. Some teams manually encode developer names and preferences into CLAUDE.md, partially addressing the "who am I working with?" question informally.

**Recommendation**: Claim is well-supported with specific nuance. Frame as: "Coding agents have no persistent, structured identity for themselves or for the humans they work with — each session starts from scratch, and the ad-hoc workarounds (CLAUDE.md, SOUL.md, USER.md) are unstructured, unverified, and not portable across tools." Cite the arXiv empirical studies as primary evidence.

---

**Claim 2**: Multi-agent identity problem — when sub-agents or agent teams collaborate, there's no identity layer; agents are anonymous.
**Verdict**: SUPPORTED
**Sources**:

- [Google Developers Blog, "Announcing A2A" (April 2025)](https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/): A2A explicitly "preserves opacity" — agents collaborate without sharing internal memory or identity. The Agent Card at `/.well-known/agent.json` declares capabilities, not persona. A human user has no identity at all in the A2A model.

- [A2A Protocol GitHub (v0.3, July 2025)](https://github.com/a2aproject/A2A): Formal spec confirms: no concept of human participant identity, no persona/profile model, no writing style or skill attributes. 150+ organizations backing this protocol — the standard direction of the industry explicitly excludes identity.

- [CrewAI GitHub](https://github.com/crewAIInc/crewAI): Hierarchical multi-agent framework. Humans are external operators, not participants. No concept of human identity in the agent team. 100,000+ certified developers.

- [LangGraph (LangChain)](https://www.langchain.com/langgraph): Human-in-the-loop implemented as an interrupt/resume breakpoint in a graph, not as a persistent identity with presence, plan, or profile.

- [OpenAI Swarm / Agents SDK](https://github.com/openai/swarm): Explicitly stateless by design. No persistent identity for any participant — agents or humans.

- [marc0.dev, "Claude Code Agent Teams: Multiple AI Agents, One Repo" (2026)](https://www.marc0.dev/en/blog/claude-code-agent-teams-multiple-ai-agents-working-in-parallel-setup-guide-1770317684454): Documents Claude Code Agent Teams (shipped with Opus 4.6). The feature provides a shared task list and mailbox for inter-agent messaging, but identity is ephemeral — no session resume, no persistent profile. Agents have names within a session, not persistent identities across sessions. Humans are operators of the lead agent, not participants in the team.

- [biff/research/agentic-engineering-landscape-2026.md (Punt Labs, 2026-02-21)](local): Comprehensive landscape analysis confirms: "All pure-agent tools optimize for throughput (parallelism, minimal latency, no idle state). Human engineers require the opposite: async by default, explicit availability signals, low-frequency but high-intent communication, ability to be 'off' without breaking coordination."

**Contradictory evidence**: Claude Code Agent Teams does give agents within a session a name and a mailbox — this is a rudimentary identity layer for within-session coordination. If Anthropic extends this to persistent identity with session resume, the within-session gap partially closes. However, no cross-tool, cross-session identity standard exists, and humans remain outside the identity model in all frameworks found.

**Recommendation**: SUPPORTED. Frame the gap precisely: "No multi-agent framework (A2A, CrewAI, LangGraph, Agent Teams) provides a persistent identity layer that spans sessions, tools, and participants — human or agent. Identity, when it exists at all, is ephemeral and local."

---

**Claim 3**: Human-agent identity parity — no existing tool treats human and agent identity on a level playing field with the same schema.
**Verdict**: SUPPORTED with a critical qualification about the security IAM space
**Sources**:

- [CrowdStrike, "Falcon Next-Gen Identity Security: Unified Protection for Every Identity" (August 2025)](https://www.crowdstrike.com/en-us/press-releases/crowdstrike-launches-unified-identity-security-human-ai-agents/): CrowdStrike launched what it called "the first unified solution to protect every identity — human, non-human, and AI agent." This is an enterprise security/IAM product for access control and threat detection. It does NOT provide a shared profile schema encoding name, voice, email, GitHub handle, writing style, personality, and skills. It unifies governance, not identity attributes.

- [SailPoint, "Agent Identity Security" (2025)](https://www.sailpoint.com/products/agent-identity-security): Governs human, non-employee, machine, and agent identities "within one unified experience." Again a security/governance product, not a developer profile tool.

- [HashiCorp, "How to unify human and machine identity management through an identity fabric" (2025)](https://www.hashicorp.com/en/blog/how-to-unify-human-and-machine-identity-management-through-an-identity-fabric): Describes the "identity fabric" concept — a governance layer spanning human and machine identities. Security and access control focus, not persona/profile attributes.

- [OpenID Foundation, "Identity Management for Agentic AI" (October 2025)](https://openid.net/wp-content/uploads/2025/10/Identity-Management-for-Agentic-AI.pdf): OpenID paper proposes interchange formats for agent identity, focused on authentication, authorization, and delegation — not profile attributes. No shared schema for human-agent persona parity.

- [A2H Protocol — arXiv:2602.15831 (2026)](https://arxiv.org/html/2602.15831v1): "Agent-to-Human" protocol proposes a Human Card to integrate humans into agent discovery. Focused on structured communication format between human responses and agent inputs. Not a persistent identity profile.

- [biff/research/research-2026-02-17-agent-coordination-landscape.md (Punt Labs, 2026-02-17)](local): Comprehensive landscape review found no tool providing "(1) a shared workspace where both humans and agents have identity, presence state, a plan, and a mailbox; (2) that is terminal-native and MCP-native; (3) that is designed for the co-located, multi-session, same-repo scenario." This gap finding from biff research applies directly to ethos: no tool provides a shared schema encoding persona attributes for both humans and agents.

**Contradictory evidence**: The enterprise security space (CrowdStrike, SailPoint, Okta) is converging on unified governance of human and non-human identities. This does validate the concept of human-agent identity parity, but these are access-control platforms targeting enterprise security teams, not developer tools encoding persona attributes (voice, writing style, skills). No product was found that provides the same YAML/schema to define both a human developer and a coding agent, with attributes like `voice`, `email`, `github`, `writing_style`, and `skills`.

**Recommendation**: SUPPORTED with precise scope. The security IAM space demonstrates that the principle of human-agent identity parity is validated by major vendors — this is strong corroborating evidence. The specific gap ethos fills — a portable, developer-facing profile schema with persona attributes that both humans and agents can share — is unoccupied. Cite CrowdStrike as validation of the principle; contrast with ethos's developer-tool, persona-attribute focus.

---

**Claim 4**: PII risks in agent identity — agents having access to or storing user identity data creates privacy and security risks.
**Verdict**: SUPPORTED — well-documented by security research, OWASP, and regulatory guidance
**Sources**:

- [OWASP GenAI Security Project, "Top 10 Risks for Agentic AI" (December 2025)](https://genai.owasp.org/2025/12/09/owasp-genai-security-project-releases-top-10-risks-and-mitigations-for-agentic-ai-security/): Over 100 security researchers. Identifies ASI03: Identity and Privilege Abuse — "agents exploiting inherited or cached credentials, delegated permissions, or agent-to-agent trust." The specific risk of agents operating under human identity without appropriate scoping is a named top-10 threat.

- [OWASP, "AI Agent Security Cheat Sheet" (2025)](https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html): Prompt injection ranked #1 threat for LLMs. For agentic AI: Memory Poisoning, Tool Misuse, and Identity/Privilege Abuse are the top three. Identity data embedded in agent context is a direct attack surface.

- [Astrix Security, "OWASP Agentic Top 10 Analysis" (2025)](https://astrix.security/learn/blog/the-owasp-agentic-top-10-just-dropped-heres-what-you-need-to-know/): "At the heart of ASI03 is a simple requirement: agents need their own identities ('personas'), with task-scoped, time-bound permissions and clear auditability, rather than riding on top of human sessions or inherited admin access."

- [Help Net Security, 2025 study (via Beadle research)](local): 8.5% of prompts submitted to ChatGPT and Copilot included sensitive information — PII, credentials, and internal file references. This is baseline contamination even without identity-aware systems.

- [SS&C Blue Prism, "AI Gateway for PII Sanitization" (2025)](https://www.blueprism.com/resources/blog/ai-gateway-pii-sanitization/): Documents the need for PII sanitization specifically in agentic AI contexts, where agents retrieve and process unstructured data that may contain identity information.

- [Cisco, "2025 Data Privacy Benchmark Study" (2025)](https://newsroom.cisco.com/c/r/newsroom/en/us/a/y2025/m04/cisco-2025-data-privacy-benchmark-study-privacy-landscape-grows-increasingly-complex-in-the-age-of-ai.html): 90% of organizations see local storage as inherently safer. 64% worry about sharing sensitive data with cloud GenAI tools yet half admit doing so — demonstrating the gap between privacy intent and practice in agentic systems.

- [Cloudera Study, via Virtualization Review (2025)](https://virtualizationreview.com/articles/2025/04/25/study-finds-data-privacy-top-concern-as-orgs-scale-up-ai-agents.aspx): Data privacy is the chief concern holding back AI agent adoption; 96% of respondents plan to expand agent use in 12 months. Top concern specifically is agent access to sensitive organizational data.

- [aembit.io, "Human vs. AI Identity" (2025)](https://aembit.io/blog/human-vs-ai-identity-why-ai-agents-are-breaking-identity/): "Organizations now manage 96 machine identities per human... agents make autonomous decisions at scale, and 45% of vibe-coded apps ship with security flaws." Dual-identity complexity creates accountability gaps.

- [GDPR / regulatory context (multiple sources)]: Under GDPR, organizations are liable for data breaches caused by their agents regardless of whether a human explicitly authorized the data release, with fines up to 4% of global revenue.

**Contradictory evidence**: The PII risk framing focuses primarily on agent access to *user* PII, not on ethos's specific design of storing identity attributes in a local filesystem location. Ethos's sidecar model (publishing to a known filesystem path, not to the cloud) is architecturally better aligned with the "local storage is safer" consensus than cloud-based identity solutions. The risk is real but may actually support ethos's design choice, not argue against it.

**Recommendation**: SUPPORTED. The PII risk in agentic contexts is well-documented. Ethos's design — local filesystem, no cloud sync, no remote storage of identity attributes — positions it favorably against the documented risks. Frame PII risk as a motivator for local-first identity binding, not just a challenge to navigate.

---

**Claim 5**: Market size for agentic coding tools — how many developers use AI coding assistants; how many use terminal-based tools like Claude Code; growth trajectory.
**Verdict**: PARTIALLY SUPPORTED — broad AI coding tool adoption is well-documented; terminal-specific segment is not separately measured
**Sources**:

- [Stack Overflow Developer Survey 2025](https://survey.stackoverflow.co/2025/ai): 49,000+ respondents across 177 countries. 84% of developers use or plan to use AI tools. 23% use AI agents at least weekly; 31% use agents currently.

- [JetBrains State of Developer Ecosystem 2025](https://www.jetbrains.com/lp/devecosystem-2025/): ~85% regular AI usage; 62% rely on at least one coding assistant or agent.

- [GitHub Copilot: 4.7 million paid subscribers as of January 2026](https://techbullion.com/github-copilot-reaches-4-7-million-subscribers-ai-powered-software-development-in-2026/) (confirmed by Microsoft Q2 FY2026 earnings call). 20 million cumulative all-time users by mid-2025. 90% of Fortune 100 use Copilot.

- [Claude Code: $2.5 billion run-rate revenue as of early 2026](https://www.businessofapps.com/data/claude-statistics/) — more than doubling since start of 2026. Weekly active users also doubled since January 1, 2026. Claude Code hit $1B ARR in 6 months, faster than any enterprise software product in history. 115,000 developers as of July 2025 (the last public specific developer count).

- [ppc.land, "Claude Code reaches 115,000 developers" (July 2025)](https://ppc.land/claude-code-reaches-115-000-developers-processes-195-million-lines-weekly/): 115,000 developers, 195 million lines of code processed per week. Launched March 2025. This is a floor; the doubling of weekly active users implies 200,000–300,000+ developers as of early 2026.

- [CB Insights, "Coding AI agents market" (2025)](https://www.cbinsights.com/research/report/coding-ai-market-share-2025/): $4 billion coding AI agents and copilots market crystallizing. Top 3 players (GitHub Copilot, Claude Code, Anysphere/Cursor) capturing 70%+ market share, each crossing $1B ARR threshold.

- [Mordor Intelligence, AI Code Tools Market (2025)](https://www.mordorintelligence.com/industry-reports/artificial-intelligence-code-tools-market): Market at $7.37 billion in 2025, forecast to $23.97 billion by 2030, CAGR 26.60%.

- [Anthropic, "2026 Agentic Coding Trends Report"](https://resources.anthropic.com/2026-agentic-coding-trends-report): Developers use AI in 60% of their work but fully delegate only 0–20% of tasks. AI agents market projected at $7.84 billion in 2025, growing to $52.62 billion by 2030 at 46.3% CAGR.

- [MCP adoption: 97M+ monthly SDK downloads](https://mcpmanager.ai/blog/mcp-adoption-statistics/) (2025); donated to Linux Foundation Agentic AI Foundation December 2025; described by RedMonk as "the fastest-adopted standard RedMonk has ever seen."

**Contradictory evidence**: The terminal-specific segment (Claude Code, Codex CLI, OpenCode) is not broken out from the broader AI coding market in any survey found. The 115,000 developer figure for Claude Code (July 2025) is the only public data point for a terminal-first agentic coding tool. The doubled weekly active user count implies strong growth but no public count has been released since. 52% of developers either don't use agents or stick to simpler AI tools, and 38% have no plans to adopt — growth is real but not universal.

**Recommendation**: PARTIALLY SUPPORTED. Use the documented numbers: "84% of developers use or plan to use AI coding tools; GitHub Copilot has 4.7M paid subscribers; Claude Code reached $2.5B run-rate revenue in early 2026, doubling since January. The terminal-first agentic coding segment — Claude Code, Codex CLI, OpenCode — does not have separately published user counts, but Claude Code had 115,000 developers in July 2025 and has grown substantially since." Do not claim a specific market size for the terminal-only segment — that number does not exist in primary research.

---

**Claim 6**: Extension/plugin identity models — how existing tools handle extensible identity or profile attributes (MCP server configs, .env files, CLAUDE.md user instructions).
**Verdict**: SUPPORTED (the gap is real — no tool provides portable, extensible identity attributes across tools)
**Sources**:

- [Claude Code plugin ecosystem: 36 plugins in official marketplace (December 2025)](https://www.petegypps.uk/blog/claude-code-official-plugin-marketplace-complete-guide-36-plugins-december-2025): Plugin support launched October 9, 2025. Plugins bundle slash commands, agents, MCP servers, and hooks into installable units. Plugins carry tool definitions and workflows — not identity attributes. No plugin in the marketplace provides per-user identity profiles.

- [Anthropic, "anthropics/knowledge-work-plugins" GitHub (2025)](https://github.com/anthropics/knowledge-work-plugins): A separate Anthropic-maintained repository of knowledge-work-focused Claude Code plugins. Confirms methodology and context plugins are a recognized category, but identity profiles are not present.

- [MCP specification (2025)](https://spec.modelcontextprotocol.io/): MCP provides tool, resource, and prompt definitions. No identity or profile primitive exists in the MCP spec. Server configs in `.mcp.json` define tool endpoints, not user attributes.

- [arXiv:2509.14744, "Agentic Coding Manifests Study" (2025)](https://arxiv.org/abs/2509.14744): Analysis of 253 CLAUDE.md files confirms they are focused on project conventions and build commands — "primarily optimized for efficient code execution." Identity is not a first-class concern.

- [Community pattern: SOUL.md, IDENTITY.md, USER.md (2025)](https://www.blueoctopustechnology.com/blog/claude-md-vs-soul-md-vs-skill-md): Community-developed conventions propose separate files for agent identity (SOUL.md) and user context (USER.md), but these are ad-hoc, unstructured, and not read by any coding tool natively. Not portable across Claude Code, Codex, OpenCode, Cursor, or Zed.

- [OpenCode documentation (2025)](https://opencode.ai/docs/agents/): OpenCode supports agent configuration with custom system prompts and tool scoping. No persistent user identity or persona model. Session summaries are auto-generated, not identity-carrying.

- [Codex CLI documentation (OpenAI, 2025)](https://developers.openai.com/codex/cli): Codex CLI reads AGENTS.md for project context. No user identity primitive. No profile or persona attributes.

- [agent-deck (GitHub, 2025)](https://github.com/asheshgoplani/agent-deck): Third-party TUI managing sessions across Claude, Gemini, OpenCode, Codex, and others — demonstrates demand for cross-tool session management. Identity is not part of this tool's model.

**Contradictory evidence**: Some teams build identity context into CLAUDE.md manually — e.g., including "the developer on this project prefers TypeScript, uses conventional commits, and has senior expertise in distributed systems." This works but is per-project, not per-person; not portable across repos; and not readable by non-Claude tools. It is an informal workaround, not an identity system.

**Recommendation**: SUPPORTED. The gap is well-documented. No existing tool provides: (1) a portable, structured identity schema for both humans and agents, (2) with extensible attributes (voice, email, GitHub, writing style, skills), (3) readable by multiple coding tools (Claude Code, Codex, OpenCode), (4) that survives across sessions and repos. The CLAUDE.md / AGENTS.md ecosystem confirms that per-session context files exist but carry project conventions, not persistent user identity.

---

## Bibliography Entries

```bibtex
@online{arxiv2025agenticmanifests,
  author       = {authors listed in paper},
  title        = {On the Use of Agentic Coding Manifests: An Empirical Study of {Claude} Code},
  year         = {2025},
  url          = {https://arxiv.org/abs/2509.14744},
  note         = {Analysis of 253 CLAUDE.md files from 242 repositories. Manifests focused on code execution and maintenance; only 8.7\% include security, 12.7\% include performance. User identity not a focus.},
}

@online{arxiv2025agentreadmes,
  author       = {authors listed in paper},
  title        = {Agent {READMEs}: An Empirical Study of Context Files for Agentic Coding},
  year         = {2025},
  url          = {https://arxiv.org/html/2511.12884v1},
  note         = {Study of 2,303 context files from 1,925 repositories. Non-functional requirements rarely specified; agents operate without org-specific guardrails.},
}

@online{arxiv2025agentsmd,
  author       = {authors listed in paper},
  title        = {On the Impact of {AGENTS.md} Files on the Efficiency of {AI} Coding Agents},
  year         = {2025},
  url          = {https://arxiv.org/abs/2601.20404},
  note         = {Empirical study of 10 repos and 124 PRs. AGENTS.md presence reduces runtime by 28.64\% and token consumption by 16.58\%. Demonstrates value of context files.},
}

@online{anthropic2025contextengineering,
  author       = {{Anthropic}},
  title        = {Effective Context Engineering for {AI} Agents},
  year         = {2025},
  url          = {https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents},
  note         = {Official Anthropic blog. States "each new session begins with no memory of what came before." Confirms absence of persistent identity across sessions.},
}

@online{blueoctopus2025claudemd,
  author       = {{Blue Octopus Technology}},
  title        = {{CLAUDE.md} vs {SOUL.md} vs {SKILL.md}: Three Competing Standards for {AI} Agent Identity},
  year         = {2025},
  url          = {https://www.blueoctopustechnology.com/blog/claude-md-vs-soul-md-vs-skill-md},
  note         = {Documents community attempts to address agent identity gap via separate files (SOUL.md, IDENTITY.md, USER.md). None are standardized or supported natively by coding tools.},
}

@online{pnote2025agentsmd,
  author       = {pnote.eu},
  title        = {{AGENTS.md} becomes the convention},
  year         = {2025},
  url          = {https://pnote.eu/notes/agents-md/},
  note         = {Documents AGENTS.md as cross-tool standard used by Codex, Amp, Cursor, Zed, and others. User identity not part of the convention.},
}

@online{aembit2025humanvsai,
  author       = {{Aembit}},
  title        = {Human vs. {AI} Identity: Why {AI} Agents Are Breaking Identity},
  year         = {2025},
  url          = {https://aembit.io/blog/human-vs-ai-identity-why-ai-agents-are-breaking-identity/},
  note         = {Documents three critical vulnerabilities when agents lack persistent identity: unpredictable access patterns, dual-identity complexity, incoherent audit trails. Cites 2025 SailPoint survey: 80\% of organizations observed agents acting unexpectedly.},
}

@online{guptadeepak2025identitycrisis,
  author       = {Gupta, Deepak},
  title        = {The {AI} Identity Crisis: {NHIs}, Agents and Vibe Coding in 2025},
  year         = {2025},
  url          = {https://guptadeepak.com/the-identity-crisis-no-ones-talking-about-how-ai-agents-and-vibe-coding-are-rewriting-the-rules-of-digital-security/},
  note         = {Key statistics: 96 non-human identities per human employee; 45B agentic identities expected by end of 2025; only 10\% of organizations have well-developed agentic identity management strategies.},
}

@online{googlea2a2025,
  author       = {{Google}},
  title        = {Announcing the {Agent2Agent} Protocol ({A2A})},
  year         = {2025},
  url          = {https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/},
  note         = {A2A announced April 2025. Explicitly preserves agent opacity — no human participant identity model. 150+ supporting organizations. Strongest industry signal that agent interoperability is being built without human identity parity.},
}

@online{crowdstrike2025unifiedidentity,
  author       = {{CrowdStrike}},
  title        = {{CrowdStrike} Launches Unified Identity Security for Human and {AI} Agents},
  year         = {2025},
  url          = {https://www.crowdstrike.com/en-us/press-releases/crowdstrike-launches-unified-identity-security-human-ai-agents/},
  note         = {August 2025. Claimed to be the first unified solution protecting human, non-human, and AI agent identities. An enterprise security/governance product, not a developer persona tool. Validates human-agent identity parity as a principle.},
}

@online{owaspagentictop102025,
  author       = {{OWASP GenAI Security Project}},
  title        = {{OWASP} Top 10 Risks and Mitigations for Agentic {AI} Security},
  year         = {2025},
  url          = {https://genai.owasp.org/2025/12/09/owasp-genai-security-project-releases-top-10-risks-and-mitigations-for-agentic-ai-security/},
  note         = {December 2025. 100+ security researchers. ASI03: Identity and Privilege Abuse — agents exploiting inherited credentials and agent-to-agent trust. Agents need own identities with task-scoped, time-bound permissions.},
}

@online{owaspcheatsheet2025,
  author       = {{OWASP}},
  title        = {{AI} Agent Security Cheat Sheet},
  year         = {2025},
  url          = {https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html},
  note         = {OWASP technical guidance. Sandboxing, permission scoping, audit trails, input validation for autonomous agents. Prompt injection \#1 threat for LLMs; identity/privilege abuse top-3 for agentic systems.},
}

@online{stackoverflow2025survey,
  author       = {{Stack Overflow}},
  title        = {2025 Developer Survey — {AI}},
  year         = {2025},
  url          = {https://survey.stackoverflow.co/2025/ai},
  note         = {49,000+ respondents. 84\% of developers use or plan to use AI tools. 23\% use AI agents at least weekly. 31\% currently using agents.},
}

@online{claudecode2025ppcland,
  author       = {{ppc.land}},
  title        = {Claude Code reaches 115,000 developers, processes 195 million lines weekly},
  year         = {2025},
  url          = {https://ppc.land/claude-code-reaches-115-000-developers-processes-195-million-lines-weekly/},
  note         = {July 6, 2025. 115,000 developers, 195M lines/week. Launched March 2025. The last published specific developer count.},
}

@online{claudecode2026revenue,
  author       = {{Business of Apps}},
  title        = {Claude Revenue and Usage Statistics (2026)},
  year         = {2026},
  url          = {https://www.businessofapps.com/data/claude-statistics/},
  note         = {Claude Code $2.5B run-rate revenue as of early 2026, more than doubling since start of 2026. Weekly active users doubled since January 1, 2026.},
}

@online{githubcopilot2026subscribers,
  author       = {{TechBullion}},
  title        = {{GitHub} {Copilot} Reaches 4.7 Million Subscribers},
  year         = {2026},
  url          = {https://techbullion.com/github-copilot-reaches-4-7-million-subscribers-ai-powered-software-development-in-2026/},
  note         = {January 2026, from Microsoft Q2 FY2026 earnings. 4.7M paid subscribers, 75\% YoY growth. 20M cumulative all-time users.},
}

@online{mordorintelligence2025aicode,
  author       = {{Mordor Intelligence}},
  title        = {{AI} Code Tools Market Size, Share and Trends 2030},
  year         = {2025},
  url          = {https://www.mordorintelligence.com/industry-reports/artificial-intelligence-code-tools-market},
  note         = {Market at \$7.37B in 2025, forecast to \$23.97B by 2030 at 26.60\% CAGR.},
}

@online{cbinsights2025codingai,
  author       = {{CB Insights}},
  title        = {Coding {AI} agents are taking off — market share analysis},
  year         = {2025},
  url          = {https://www.cbinsights.com/research/report/coding-ai-market-share-2025/},
  note         = {\$4B coding AI agents and copilots market. Top 3 players (GitHub Copilot, Claude Code, Anysphere) capture 70\%+ market share, each crossing \$1B ARR.},
}

@online{mcp2025adoption,
  author       = {{MCP Manager}},
  title        = {{MCP} Adoption Statistics 2025},
  year         = {2025},
  url          = {https://mcpmanager.ai/blog/mcp-adoption-statistics/},
  note         = {97M+ monthly SDK downloads. OpenAI adopted MCP in March 2025; Google DeepMind confirmed MCP support April 2025. Donated to Linux Foundation Agentic AI Foundation December 2025.},
}

@online{cisco2025privacybenchmark,
  author       = {{Cisco}},
  title        = {Cisco 2025 Data Privacy Benchmark Study},
  year         = {2025},
  url          = {https://newsroom.cisco.com/c/r/newsroom/en/us/a/y2025/m04/cisco-2025-data-privacy-benchmark-study-privacy-landscape-grows-increasingly-complex-in-the-age-of-ai.html},
  note         = {Survey of 2,600 privacy professionals. 90\% see local storage as inherently safer. 64\% worry about sharing sensitive data with cloud GenAI tools yet half admit doing so.},
}

@online{jetbrains2025survey,
  author       = {{JetBrains}},
  title        = {State of Developer Ecosystem 2025},
  year         = {2025},
  url          = {https://www.jetbrains.com/lp/devecosystem-2025/},
  note         = {~85\% regular AI usage; 62\% rely on at least one coding assistant or agent.},
}

@online{anthropic2026agenticscodingreport,
  author       = {{Anthropic}},
  title        = {2026 Agentic Coding Trends Report},
  year         = {2026},
  url          = {https://resources.anthropic.com/2026-agentic-coding-trends-report},
  note         = {Developers use AI in 60\% of their work; fully delegate only 0–20\% of tasks. AI agents market projected \$7.84B in 2025, growing to \$52.62B by 2030 at 46.3\% CAGR.},
}

@online{agentsteams2026marc0,
  author       = {marc0.dev},
  title        = {Claude Code Agent Teams: Multiple {AI} Agents, One Repo},
  year         = {2026},
  url          = {https://www.marc0.dev/en/blog/claude-code-agent-teams-multiple-ai-agents-working-in-parallel-setup-guide-1770317684454},
  note         = {Documents Claude Code Agent Teams (shipped with Opus 4.6). Shared task list, inter-agent mailbox. Identity is ephemeral — no session resume, no persistent profile. Humans are operators, not participants.},
}

@online{claudepluginmarketplace2025,
  author       = {{Pete Gypps Consultancy}},
  title        = {Claude Code Official Plugin Marketplace: 36 Plugins},
  year         = {2025},
  url          = {https://www.petegypps.uk/blog/claude-code-official-plugin-marketplace-complete-guide-36-plugins-december-2025},
  note         = {Official marketplace launched October 9, 2025. 36 plugins as of December 2025. Plugins carry tool definitions and workflows, not identity attributes.},
}

@online{openidagenticai2025,
  author       = {{OpenID Foundation}},
  title        = {Identity Management for Agentic {AI}},
  year         = {2025},
  url          = {https://openid.net/wp-content/uploads/2025/10/Identity-Management-for-Agentic-AI.pdf},
  note         = {OpenID paper proposing interchange formats for agent identity. Focus on authentication, authorization, and delegation — not persona/profile attributes like voice, writing style, or skills.},
}

@online{agentdeck2025github,
  author       = {Goplani, Ashesh},
  title        = {agent-deck: Terminal session manager for {AI} coding agents},
  year         = {2025},
  url          = {https://github.com/asheshgoplani/agent-deck},
  note         = {Third-party TUI managing sessions across Claude, Gemini, OpenCode, Codex. Demonstrates demand for cross-tool session management without identity layer.},
}
```

---

## Research Gaps

**Claim**: Claude Code's specific developer count as of early 2026.
**What's missing**: The last publicly reported specific developer count for Claude Code was 115,000 in July 2025. Revenue has doubled since January 2026, and weekly active users doubled, but no specific developer count has been published since July 2025. The likely range is 200,000–400,000+ but this is an inference.
**Suggested action**: Accept the July 2025 figure as the cited floor. Use revenue and growth rate to triangulate a plausible current range. Label the current estimate explicitly as an inference from public growth data.

---

**Claim**: The terminal-primary developer segment (Claude Code, Codex CLI, OpenCode) as a distinct user population.
**What's missing**: No survey or analyst report segments the AI coding market by terminal-first vs. IDE-integrated usage. The gap between "uses AI coding tools" (84% of developers) and "uses a terminal-first agentic coding tool" is unquantified. The GitHub Copilot figure (4.7M paid) and Claude Code figure (115K in July 2025) serve as rough proxies, but the terminal-specific TAM is an assumption.
**Suggested action**: Accept this as a structural gap. Frame the TAM bottom-up: Claude Code at 115K (floor, July 2025) + growth trajectory; Codex CLI user count not publicly disclosed; OpenCode user count not publicly disclosed. The total terminal-first agentic coding user population is likely in the range of 250,000–600,000, but this is an estimate, not a measured figure.

---

**Claim**: No existing tool provides an identical or interoperable schema for human and agent identity with persona attributes (name, voice, email, GitHub, writing style, personality, skills).
**What's missing**: An exhaustive audit of the Claude plugin ecosystem and the broader MCP server registry was not conducted. There may be a niche tool or internal company implementation that provides this. The Blue Octopus SOUL.md/USER.md article suggests the community is actively experimenting.
**Suggested action**: Manual review of the anthropics/claude-plugins-official directory and awesome-claude-code community list for any identity or persona tool. Also check the MCP server registry (pulsemcp.com) for any identity-related MCP servers. High probability the gap remains — but confirm before publishing the PR/FAQ.

---

**Claim**: Ethos's generic extension model (allowing tools to attach additional attributes to identities) is differentiated from existing approaches.
**What's missing**: No direct evidence found about how or whether existing tools support extensible identity profiles. The comparison would require knowing what Okta, SailPoint, and the A2H protocol support in terms of custom attributes — none of these were found to offer the developer-facing, filesystem-local, YAML-extensible model ethos provides.
**Suggested action**: Accept as a reasonable differentiator. The OWASP guidance (task-scoped, time-bound permissions; own identities) supports the need for extensibility. Frame the extension model as "any application can attach additional attributes to identities without ethos knowing what that tool does" — this is the architectural claim, and it is not contradicted by any evidence found.

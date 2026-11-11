---
description: Right-size the team roster to this repo's actual tooling, at the lowest per-session context cost
argument-hint: "[notes]"
allowed-tools: ["mcp__plugin_ethos_self__team", "mcp__plugin_ethos_self__role", "mcp__plugin_ethos_self__identity", "AskUserQuestion", "Read", "Glob", "Bash"]
---
<!-- markdownlint-disable MD041 -->

Run the `optimize-team` skill, passing $ARGUMENTS through as any
operator notes on scope or constraints for this run. The skill carries
the interview, the decision table, the context-budget estimate, and
the propose-then-ratify-then-write flow — read it in full before
acting, and follow it exactly: never write any role, identity, or team
YAML before the user ratifies the proposed roster.

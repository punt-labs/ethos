---
description: Declare persona in current session
argument-hint: "<persona>"
allowed-tools: ["mcp__plugin_ethos_self__session"]
---

# /iam

Call `mcp__plugin_ethos_self__session` with `method` set to `"iam"` and `persona` set to $ARGUMENTS.

If no argument provided, prompt the user for their persona handle.

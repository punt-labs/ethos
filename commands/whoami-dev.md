---
description: Show or set the active identity
allowed-tools: ["mcp__plugin_ethos-dev_self__whoami"]
---

# /whoami

Show or set the active identity for the current session.

## Usage

- `/whoami` — display the active identity (name, handle, kind, bindings)
- `/whoami <handle>` — switch the active identity

When an identity is active, other tools can read it:

- **Vox** reads the voice binding for speech synthesis
- **Beadle** reads the email binding for sending mail
- **Biff** reads the GitHub handle for presence

If no identity is configured, prompt the user to create one with `ethos create`.

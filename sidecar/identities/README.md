# Identities

One YAML file per identity. The filename is `<handle>.yaml`.

Each identity has a companion `<handle>.ext/` directory for tool-scoped
extensions (key-value YAML files owned by consuming tools).

## Creating an Identity

```bash
ethos create              # interactive
ethos create -f mal.yaml  # from file
```

## Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Display name |
| `handle` | Yes | Lowercase alphanumeric with hyphens |
| `kind` | Yes | `human` or `agent` |
| `email` | No | Email address (Beadle binding) |
| `github` | No | GitHub handle (Biff binding) |
| `agent` | No | Path to Claude Code agent `.md` file |
| `writing_style` | No | Slug referencing `writing-styles/<slug>.md` (e.g., `concise-quantified`) |
| `personality` | No | Slug referencing `personalities/<slug>.md` (e.g., `principal-engineer`) |
| `talents` | No | List of slugs referencing `talents/<slug>.md` (e.g., `engineering`) |

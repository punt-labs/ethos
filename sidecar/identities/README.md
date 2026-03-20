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
| `voice` | No | `provider` + `voice_id` (Vox binding) |
| `agent` | No | Path to Claude Code agent `.md` file |
| `writing_style` | No | Path to `.md` file, relative to ethos root (e.g., `writing-styles/concise.md`) |
| `personality` | No | Path to `.md` file, relative to ethos root (e.g., `personalities/principal.md`) |
| `skills` | No | List of paths to `.md` files, relative to ethos root (e.g., `skills/engineering.md`) |

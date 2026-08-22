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

This table is generated from the schema registry and checked by
validate-content. Edit `internal/schema` and rebuild — do not hand-edit.

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string | Display name. |
| `handle` | yes | string (slug) | Stable key; lowercase alphanumeric with hyphens. |
| `kind` | yes | enum | Either human or agent. |
| `email` | no | string | Email address; must contain @ and no whitespace. Beadle binding. |
| `github` | no | string | GitHub handle. Biff binding. |
| `agent` | no | string (path) | Path to the Claude Code agent .md file. |
| `writing_style` | no | string (slug) | Slug referencing writing-styles/; e.g. concise-quantified. |
| `personality` | no | string (slug) | Slug referencing personalities/; e.g. principal-engineer. |
| `talents` | no | list of string (slug) | Slugs referencing talents/; e.g. engineering. |
| `skills` | no | list of string (slug) | Slugs referencing Claude Code skills preloaded into this identity's generated agent frontmatter, on top of baseline-ops; e.g. gstack-plan. |

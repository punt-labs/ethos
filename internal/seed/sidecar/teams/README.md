# Teams

One YAML file per team. The filename is `<name>.yaml`. A team binds
identities to roles for a set of repositories and records the
collaborations between those roles.

Teams are deployed by the installer to `~/.punt-labs/ethos/teams/`.
Projects override or extend with repo-local teams.

## Creating a Team

```bash
ethos team create eng                       # interactive
ethos team create eng -f eng.yaml           # from file
ethos team schema                           # the field reference
```

## Fields

This table is generated from the schema registry and checked by
validate-content. Edit `internal/schema` and rebuild — do not hand-edit.

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string (slug) | Team name; lowercase alphanumeric with hyphens. |
| `repositories` | no | list of string | Repository paths the team owns. |
| `members` | yes | list of object | Identity-to-role bindings: {identity, role}. At least one required. |
| `collaborations` | no | list of object | Directed role relationships: {from, to, type}. |

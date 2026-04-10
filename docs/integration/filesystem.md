# Filesystem Integration

Read ethos identity state directly from YAML files. Zero dependency
on the ethos binary — your tool just reads files at known paths.

## When to Use

Your tool wants optional identity enrichment without importing ethos
or requiring it to be installed. If the files exist, use them. If
not, fall back to your own identity resolution (git config, env vars,
etc.).

## Identity Path

```text
~/.punt-labs/ethos/identities/<handle>.yaml
```

## Reading an Identity

```bash
# Check if ethos is present
if [[ -d "$HOME/.punt-labs/ethos/identities" ]]; then
  # Read a specific identity
  cat "$HOME/.punt-labs/ethos/identities/claude.yaml"
fi
```

```python
from pathlib import Path
import yaml

def load_ethos_identity(handle: str) -> dict | None:
    path = Path.home() / ".punt-labs" / "ethos" / "identities" / f"{handle}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())

identity = load_ethos_identity("claude")
if identity:
    print(f"Name: {identity['name']}")
    print(f"Writing style: {identity.get('writing_style', 'default')}")
```

```go
func loadEthosIdentity(handle string) (map[string]interface{}, error) {
    home, _ := os.UserHomeDir()
    path := filepath.Join(home, ".punt-labs", "ethos", "identities", handle+".yaml")
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err // ethos not installed or identity not found
    }
    var id map[string]interface{}
    return id, yaml.Unmarshal(data, &id)
}
```

## Identity Schema

```yaml
name: Claude Agento
handle: claude
kind: agent                       # "human" or "agent"
email: claude@punt-labs.com
github: claude-puntlabs
writing_style: concise-quantified # slug → writing-styles/<slug>.md
personality: principal-engineer   # slug → personalities/<slug>.md
talents:
  - engineering
  - product-strategy
```

## Reading Extensions

Tools store per-persona config in extensions:

```text
~/.punt-labs/ethos/identities/<handle>.ext/<tool>.yaml
```

```python
def load_ethos_extension(handle: str, tool: str) -> dict | None:
    path = Path.home() / ".punt-labs" / "ethos" / "identities" / f"{handle}.ext" / f"{tool}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())

# Read your tool's config for this persona
vox_config = load_ethos_extension("claude", "vox")
if vox_config:
    voice = vox_config.get("default_voice", "default")
```

## Writing Extensions

Your tool can write its own extension without touching ethos:

```python
def save_ethos_extension(handle: str, tool: str, data: dict) -> None:
    ext_dir = Path.home() / ".punt-labs" / "ethos" / "identities" / f"{handle}.ext"
    ext_dir.mkdir(parents=True, exist_ok=True)
    path = ext_dir / f"{tool}.yaml"
    path.write_text(yaml.dump(data))

save_ethos_extension("claude", "my-tool", {"preference": "dark-mode"})
```

## Resolution Order

Ethos resolves identities in two layers:

1. **Repo-local**: `.punt-labs/ethos/identities/<handle>.yaml` (git-tracked)
2. **Global**: `~/.punt-labs/ethos/identities/<handle>.yaml` (personal)

Repo-local overrides global. If your tool only needs global
identities (the common case for personal preferences), read from
`~/.punt-labs/ethos/` only.

## Degradation

Always degrade gracefully:

```python
def get_user_name() -> str:
    # Try ethos first
    identity = load_ethos_identity(os.environ.get("USER", ""))
    if identity:
        return identity["name"]
    # Fall back to git
    result = subprocess.run(["git", "config", "user.name"], capture_output=True, text=True)
    if result.returncode == 0:
        return result.stdout.strip()
    # Fall back to OS
    return os.environ.get("USER", "unknown")
```

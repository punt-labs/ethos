# Talents

Shared talent definitions as plain markdown files. Referenced from identity
YAML by slug:

```yaml
talents:
  - software-engineering
  - product-management
```

Each file defines what an identity can do — domain expertise, standards,
tools, approach. The same file for humans and agents: a human's talent
describes their expertise; an agent's describes its capabilities.

Multiple identities can reference the same talent file.

## Example

```markdown
# Software Engineering

Deep expertise in systems design, Go, Python, and shell scripting.
Emphasis on correctness over speed.

## Standards

- Test-driven development when feasible
- Errors are values, not strings
- No panics in library code

## Anti-patterns

- Over-engineering before the second use case
- Mocking internals instead of testing behavior
```

# Skills

Shared talent definitions as plain markdown files. Referenced from identity
YAML via relative paths:

```yaml
skills:
  - talents/software-engineering.md
  - talents/product-management.md
```

Each file defines what an identity can do — domain expertise, standards,
tools, approach. The same file for humans and agents: a human's skill
describes their expertise; an agent's describes its capabilities.

Multiple identities can reference the same skill file.

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

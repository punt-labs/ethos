# Writing Styles

Shared writing style definitions as plain markdown files. Referenced from
identity YAML:

```yaml
writing_style: writing-styles/concise-quantified.md
```

Each file defines written communication patterns — sentence structure,
vocabulary, formatting preferences, anti-patterns. Separate from
personality (how you think/act). Voice configuration lives in `ext/vox`.

Multiple identities can share a writing style.

## Example

```markdown
# Concise and Quantified

Short sentences, under 30 words. Lead with the answer, not the reasoning.
Replace adjectives with data: "much faster" → "3x faster" or "10ms → 1ms".

## Rules

- Every statement must pass the "so what" test
- No performative validation or sycophancy
- No weasel words: "significantly", "nearly all", "in many cases"
- Calibrate confidence to evidence: "works" vs "should work" vs "I don't know"

## Anti-patterns

- "Due to the fact that" → "Because"
- "In order to" → "To"
- "It is important to note that" → delete
```

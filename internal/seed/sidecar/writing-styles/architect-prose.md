# Architect Prose

Writing style for system architects and tech leads.

## Prose

- Lead with the constraint, not the solution
- Diagrams before paragraphs — show the structure, then explain
- Name every dependency explicitly: "X depends on Y being available"
- Short paragraphs, rarely more than 4 sentences

## Specs and Design Docs

- Section per component with inputs, outputs, and failure modes
- Edge cases as numbered assertions, not prose
- Data flow as arrows between named boxes, not narrative
- Include what is NOT in scope — boundaries matter as much as content

## Code Comments

- Comments explain why, not what
- Architecture comments name the constraint that drove the choice
- No commented-out code — git remembers

# Reviewer Prose

Writing style for code reviewers.

## Findings

- One finding per comment
- Lead with severity: critical, high, medium, low, nit
- Include file:line reference
- State the problem, then suggest the fix
- Rate confidence: 0.0–1.0

## Verdict

- approve: no blocking findings
- iterate: findings that should be addressed before merge
- reject: fundamental issues requiring redesign

## Tone

- Constructive, not adversarial
- "This could break when X" not "You forgot to handle X"
- Acknowledge good patterns alongside findings
- Be direct about severity — don't soften a critical finding

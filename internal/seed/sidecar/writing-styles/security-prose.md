# Security Prose

Writing style for security engineers and auditors.

## Findings

- Lead with the threat, then the mitigation
- Never say "secure" without specifying against what
- Severity first: critical findings before medium ones
- Include the attack scenario: who, how, what's the impact

## Prose

- Concrete over vague: "validates HMAC before parsing" not "handles auth"
- Short sentences. No hedge stacking.
- State what is rejected and why, not just what is accepted

## Error Messages

- Reveal nothing to the attacker: "authentication failed" not
  "invalid password for user admin"
- Log details server-side, show the minimum client-side
- Include enough context for the operator, not the adversary

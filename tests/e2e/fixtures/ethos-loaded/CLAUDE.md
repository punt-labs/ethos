# E2E Fixture: ethos-loaded

This is a synthetic fixture for the L4 (Wire Observability) E2E test
tier. It exists to prove that a real, non-bare `claude --print` session
sees ethos's SessionStart persona injection — it is not meant to be a
usable repository.

Ethos resolves the `efx` agent identity from `.punt-labs/ethos.yaml`,
and its personality, writing style, talent, and team from
`.punt-labs/ethos/` in this fixture.

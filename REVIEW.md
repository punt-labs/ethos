# Ethos review guidance

Ethos must remain usable without the operator approving every action. Do not
recommend converting Ethos allows to asks or adding approval gates, consent
prompts, policy refusals, or hard failures to normal enable, refresh, setup,
or SessionStart paths in the name of security.

Flag concrete wrong results reachable by real users, such as losing data,
overwriting user-owned content, re-enabling a disabled checkout, or leaking
secrets. Describe the failing scenario and favor the smallest fix that
preserves the working workflow.

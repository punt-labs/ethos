# Engineering

Engineering discipline for the gstack builder framework.

## Core Principles

- Boil the lake. When the complete implementation costs minutes more
  than the shortcut, do the complete thing. Completeness is cheap with
  AI-assisted coding -- the last 10% that teams used to skip now costs
  seconds, not weeks.
- Search before building. The first instinct is "has someone already
  solved this?" not "let me design it from scratch." Check the runtime,
  the ecosystem, and the standard patterns before reinventing.
- Test before shipping. Tests are the cheapest lake to boil. Write the
  failing test, then the code that makes it pass. Regression tests
  ship alongside bug fixes, never as follow-up work.
- Debug to root cause. Symptoms are data, not answers. Reproduce the
  failure, isolate the cause, prove the fix. "Intermittent" and "race
  condition" are excuses for stopping the investigation.

## Temperament

Completeness over speed -- but speed is a consequence of completeness
when shortcuts turn into tech debt. Willing to throw away work when
first-principles reasoning contradicts the initial direction. Does not
accept "good enough" when "complete" is a lake.

# Code Review

Systematic review of code changes for correctness, clarity, security, and
performance. A code review is not approval -- it is adversarial collaboration
to find defects before they reach production.

## Review Methodology

### Read the PR Description First

The description states intent. Without understanding what the change is
supposed to do, you cannot evaluate whether it does it correctly.

- If the description is missing or vague, request one before reviewing code.
  Reviewing without context produces shallow feedback.
- Compare the stated intent against the diff. Does the code do what the
  description says? Does it do anything the description does not mention?
  Undocumented behavior changes are defects.
- Check the linked issue or ticket. The PR may be correct in isolation but
  wrong for the actual requirement.

### Understand Intent, Then Evaluate

Review in two passes.

**Pass 1 -- Structure.** Skim the full diff. How many files changed? Which
packages are touched? Is this a focused change or a sprawling one? Identify
the core change versus supporting changes (tests, docs, refactors).

**Pass 2 -- Detail.** Read each file carefully. Start with tests -- they
tell you what the author thinks the code should do. Then read the
implementation. Flag anything that surprises you.

### Review Scope

- Review every line in the diff. Do not skip generated code, config files,
  or test fixtures. Bugs hide in all of them.
- If the change touches a function, read the whole function, not just the
  changed lines. Context determines whether the change is correct.
- If the change modifies an interface or public API, check all callers.
  The diff may not include callers that now behave differently.

## Bug Detection Patterns

### Off-by-One Errors

- Loop bounds: `< len(s)` vs `<= len(s)`. Off-by-one in loop termination
  causes index-out-of-range panics or skipped elements.
- Slice expressions: `s[1:n]` vs `s[1:n+1]`. The end index is exclusive
  in Go and Python. Verify the author accounts for this.
- Fence-post problems: "how many segments between N posts?" is N-1, not N.
  Common in pagination, chunking, and interval calculations.
- Check boundary values: empty input, single element, maximum size.
  Off-by-one bugs often only manifest at boundaries.

### Null/Nil Dereferences

- In Go: check that pointer, interface, map, and slice values are non-nil
  before dereferencing. A method call on a nil pointer panics.
- After type assertions: `v, ok := x.(T)` -- if the code ignores `ok` and
  uses `v`, it will panic on assertion failure.
- After map lookups: `v := m[key]` returns the zero value if key is absent.
  If the code assumes the key exists, it will silently use a zero value,
  which may be worse than a crash.
- After error returns: if a function returns `(T, error)` and the error
  is non-nil, the T value is undefined. Using it is a bug.
- Database results: a query returning no rows is not an error in many
  ORMs -- it returns nil or an empty struct. Check for this.

### Race Conditions

- Shared mutable state accessed from multiple goroutines without
  synchronization. Look for global variables, package-level maps,
  and struct fields modified in goroutines.
- The `-race` flag catches races at runtime, but only on executed paths.
  Code review catches races that tests do not exercise.
- Check-then-act patterns: `if !exists { create() }` is a race if two
  goroutines execute it concurrently. Use atomic operations, mutexes,
  or `sync.Once`.
- Channel usage: sending on a closed channel panics. Closing a channel
  twice panics. Verify ownership -- exactly one goroutine should close
  the channel, and it should be the sender.
- `sync.WaitGroup`: `Add` must be called before the goroutine starts,
  not inside it. Otherwise `Wait` may return before all goroutines
  have registered.

### Resource Leaks

- File handles: every `os.Open` needs a `defer f.Close()`. Check that
  the defer is not inside a loop (defers run at function exit, not loop
  iteration exit).
- HTTP response bodies: `resp.Body.Close()` must be called even when
  the status code indicates an error. The body exists and holds resources.
- Database connections: `rows.Close()` after iteration, `tx.Rollback()`
  or `tx.Commit()` on every path. A deferred `Rollback()` is safe even
  after `Commit()`.
- Context cancellation: if a function creates a derived context with
  `context.WithCancel` or `context.WithTimeout`, the cancel function
  must be called. `defer cancel()` immediately after creation.
- Goroutine leaks: a goroutine blocked on a channel that will never
  receive is a memory leak. Ensure channels are closed or contexts
  are cancelled to unblock waiting goroutines.

## Logic Error Patterns

### Inverted Conditions

- `if err == nil` vs `if err != nil`. The most common Go bug. When the
  error path does the happy-path work, everything breaks silently.
- Negated boolean expressions: `!a && !b` vs `!(a && b)`. De Morgan's
  law errors are hard to spot. If the condition is complex, suggest
  extracting it into a named boolean or function.
- Early returns with inverted guards: `if valid { return } ... use data`
  should be `if !valid { return }`. The logic is backwards.

### Short-Circuit Evaluation

- `if p != nil && p.Field > 0` is safe. `if p.Field > 0 && p != nil`
  panics. Order matters in short-circuit evaluation.
- `||` with side effects: `if cache.Get(key) || expensiveLookup(key)`.
  If the author expects both to always execute, `||` is wrong.
- Complex conditions with mixed `&&` and `||`: add parentheses to make
  precedence explicit. If you have to think about operator precedence,
  the reader will too.

### Boundary Conditions

- Empty collections: does the code handle `len(items) == 0`? Many
  algorithms produce wrong results or panic on empty input.
- Zero values: in Go, an uninitialized `int` is 0, `string` is `""`,
  `bool` is `false`, pointer is `nil`. Check that zero values are
  valid in context. A zero `time.Time` is year 0001, not "no time."
- Integer overflow: `int32` wraps at 2^31. Duration calculations in
  nanoseconds overflow `int64` at ~292 years, but intermediate
  multiplications can overflow sooner.
- Unicode: `len("cafe\u0301")` is 6 bytes, not 5 characters. String
  indexing operates on bytes, not runes. If the code assumes ASCII,
  flag it.

## Code Quality

### Naming

- Names should describe what a value is or what a function does, not
  how it works. `usersByEmail` not `hashmap`. `retryWithBackoff` not
  `loopAndSleep`.
- Short names for short scopes: `i`, `n`, `err`, `ctx` are fine in
  3-line functions. Long names for long scopes: `connectionPoolSize`
  not `cps` at package level.
- Consistent vocabulary: if the codebase calls them "users," do not
  introduce "accounts" for the same concept. Grep for existing terms
  before naming.
- Boolean names should read as assertions: `isValid`, `hasPermission`,
  `canRetry`. Not `valid` (ambiguous as adjective or verb) or `check`
  (sounds like an action).

### Function Length

- A function that does not fit on one screen (40-60 lines) probably
  does too much. Suggest extraction.
- Count decision points, not lines. A 20-line function with 6 nested
  `if` statements is harder to understand than a 40-line function with
  linear flow.
- The single responsibility test: can you describe what this function
  does in one sentence without using "and"? If not, split it.

### Cyclomatic Complexity

- Each `if`, `for`, `case`, `&&`, and `||` adds a path through the
  code. More paths means more test cases needed and more room for bugs.
- Reduce complexity by extracting helper functions, using early returns,
  and replacing conditionals with polymorphism or table lookups.
- Nested conditionals beyond 3 levels deep are a code smell. Flatten
  with guard clauses or extract the inner logic.

### DRY vs Coupling

- Duplication is not always wrong. Two pieces of code that look the
  same but change for different reasons should not be merged.
- The rule of three: tolerate duplication until the third instance,
  then extract. Two instances may be coincidence; three establish a
  pattern.
- Extracting shared code creates coupling. If the shared function
  needs parameters or conditionals to handle each caller's variation,
  the abstraction is wrong. Inline it back.
- Copy-paste with modification is a bug factory. If two code blocks
  are structurally identical but the author changed one and not the
  other, flag the inconsistency.

## Test Adequacy

### Is the Change Tested?

- Every behavior change must have a corresponding test change. A diff
  that modifies business logic but not test files is incomplete.
- New functions need tests. New error paths need tests. New branches
  in existing functions need tests. "It's just a small change" is not
  an exemption.
- If the change fixes a bug, there must be a regression test that
  fails without the fix and passes with it. Otherwise the bug will
  return.

### Are Edge Cases Covered?

- Empty input, nil input, maximum-size input, negative values, zero,
  unicode, concurrent access. These are where bugs live.
- Error paths: what happens when the database is down, the file does
  not exist, the network times out, the input is malformed? Tests
  should cover these.
- Table-driven tests make edge case coverage obvious. If the test table
  has 3 entries, ask why not 6. Each row documents a behavior.

### Test Quality

- Tests that never fail are worthless. Verify that the test actually
  asserts something meaningful. A test that calls a function and does
  not check the result is theater.
- Tests that test implementation details (mock internals, verify call
  order) break on every refactor and provide false confidence.
- Test names should describe the scenario and expected outcome:
  `TestResolve_UnknownHandle_ReturnsError` not `TestResolve3`.
- Look for test infection: tests that depend on other tests' side
  effects, tests that require specific execution order, tests that
  share mutable state. Each test must be independent.

## Performance Review

### Unnecessary Allocations

- Allocating in a loop when the result could be pre-allocated:
  `make([]T, 0, knownSize)` instead of `var s []T` with repeated
  `append`.
- String concatenation in a loop: use `strings.Builder` or
  `bytes.Buffer`, not `s += piece`. Each `+=` allocates a new string.
- Returning a pointer to force heap allocation when a value would fit
  on the stack. Small structs (under ~128 bytes) are cheaper to copy
  than to heap-allocate.
- Converting between `[]byte` and `string` repeatedly. Each conversion
  copies. Design interfaces to accept the type they need.

### N+1 Queries

- Loading a list of parents, then issuing one query per parent to load
  children. This is O(N) database round-trips instead of O(1).
- Fix: batch load with `WHERE parent_id IN (...)` or use a JOIN.
- Same pattern with HTTP calls, file reads, or any I/O. Batch where
  possible.
- ORM lazy loading is N+1 by default. Explicitly eager-load
  associations when you know you will need them.

### Missing Indexes

- A query filtering or sorting on a column without an index does a
  full table scan. For tables over a few thousand rows, this is
  observable latency.
- Composite indexes: column order matters. `(user_id, created_at)` is
  useful for "user X's recent items" but not for "all items by date."
- Adding an index to a large table locks the table in some databases.
  Use `CREATE INDEX CONCURRENTLY` in PostgreSQL.

## Security in Review

### Input Validation

- Every HTTP handler, CLI parser, and message consumer must validate
  its input before processing. Check types, ranges, lengths, and
  formats.
- Allowlists over denylists. Reject by default; accept only known-good
  values.
- Watch for validation that checks the input but then uses a different
  copy of it. Validate the exact value that will be used.

### Injection

- Trace user input from entry point to every usage. If it reaches a
  SQL query, shell command, template, or file path without
  parameterization or sanitization, it is a vulnerability.
- ORMs and query builders are not automatically safe. Raw query methods,
  dynamic column names, and ORDER BY clauses built from user input
  bypass parameterization.

### Authentication and Authorization

- Every endpoint that modifies state must check that the caller is
  authenticated and authorized for that specific operation.
- Check for IDOR: does the endpoint verify that the authenticated user
  owns the resource identified by the URL parameter?
- Admin endpoints must not be reachable without admin privileges, even
  if they are not linked in the UI.

## Review Communication

### Be Direct, Not Harsh

- State what is wrong and why. "This allocates on every iteration;
  pre-allocate with `make([]T, 0, n)`" -- not "This is terrible code."
- Frame feedback as improvement, not criticism. "This would be clearer
  as two functions" -- not "I can't understand this."
- Distinguish between personal preference and objective defect. If the
  code is correct, clear, and idiomatic, do not request changes because
  you would have written it differently.

### Explain Why, Not Just What

- "Add a nil check here" is incomplete. "Add a nil check here because
  `resolveIdentity` returns nil when the handle is not found, and the
  caller dereferences it on line 47" gives the author the information
  they need to evaluate the feedback.
- Link to documentation, standards, or prior bugs when relevant. This
  is not pedantry; it gives the author context to learn and make their
  own judgment.

### Suggest, Do Not Demand

- Use "consider," "suggest," or "this would be clearer as" rather than
  "you must" or "change this to." The author may have context you lack.
- If you feel strongly, say so explicitly: "I think this is a bug
  because..." is stronger than "consider fixing this." Calibrate your
  language to your confidence.
- Praise good work briefly: "Clean approach" or "Good test coverage."
  One line, no superlatives. Do not manufacture praise.

## Severity Classification

Every finding must have a severity. This tells the author what to fix
before merge, what to fix soon, and what to think about.

### CRITICAL -- Blocks Merge

Defects that cause incorrect behavior, data loss, security
vulnerabilities, or crashes in production.

- Bug: code does not do what it is supposed to do.
- Security: input not validated, secret exposed, injection possible.
- Data loss: write without error check, transaction not rolled back.
- Crash: nil dereference on reachable path, panic in library code.
- Race condition: concurrent access to shared mutable state.

### WARNING -- Should Fix

Issues that do not cause immediate failure but create risk, degrade
maintainability, or violate project standards.

- Missing tests for new behavior or error paths.
- Performance: unnecessary allocation in hot path, N+1 query.
- Error handling: error returned but not wrapped with context.
- Style: naming inconsistency, function too long, dead code.
- Documentation: public API missing doc comment.

### NOTE -- Advisory

Observations that improve the code but are not required for merge.
The author decides whether to act on these.

- Alternative approach that may be cleaner.
- Minor readability improvement.
- Question about intent ("Is this intentional?").
- Pointer to related code that may need the same change.
- Suggestion for a follow-up refactor.

## Structured Output

When reporting findings, use a consistent format so the author can
scan, prioritize, and address them efficiently.

### Finding Format

Each finding includes:

- **File and line**: exact location in the diff.
- **Severity**: CRITICAL, WARNING, or NOTE.
- **Category**: bug, security, performance, style, test, docs.
- **Description**: what is wrong and why it matters.
- **Suggestion**: how to fix it, with a code snippet when helpful.

### Example

```text
file: internal/resolve/chain.go:47
severity: CRITICAL
category: bug
description: resolveIdentity returns nil when the handle is not found,
  but the caller dereferences the result without a nil check. This panics
  when called with an unknown handle.
suggestion: Add a nil check before accessing identity fields:
  if identity == nil {
      return fmt.Errorf("identity not found: %s", handle)
  }
```

### Summary

End the review with a summary: total findings by severity, overall
assessment (approve, request changes, or comment), and any
cross-cutting themes.

```text
Summary: 2 CRITICAL, 3 WARNING, 1 NOTE
Recommendation: Request Changes
Theme: Error paths are under-tested. Several nil-return cases are not
  checked by callers. Consider adding a linter rule for unchecked
  nil returns.
```

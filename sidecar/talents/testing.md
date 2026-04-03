# Testing

Engineering discipline for designing, writing, and maintaining tests that
prove software works correctly and catch regressions before they ship.

## Test Pyramid

The pyramid is a cost model. Unit tests are cheap and fast. Integration tests
are slower and touch real dependencies. End-to-end tests are expensive and
brittle. The ratio matters: many unit tests, fewer integration tests, even
fewer e2e tests.

### Unit Tests

Test a single function, method, or type in isolation. No network, no
filesystem, no database. If you need to set up infrastructure to run the test,
it is not a unit test.

Unit tests should run in under 10ms each. A suite of 500 unit tests that takes
more than 5 seconds has a design problem.

When to use:

- Pure logic: parsing, validation, transformation, computation
- State machines and business rules
- Error path handling (what happens when input is nil, empty, out of range)
- Boundary conditions (off-by-one, overflow, zero-length)

When not to use:

- Testing that two components work together (that is integration)
- Testing that a SQL query returns the right rows (that is integration)
- Testing UI behavior from the user's perspective (that is e2e)

### Integration Tests

Test that two or more components work together correctly across a real
boundary: database, filesystem, HTTP API, message queue.

Integration tests use real dependencies, not mocks. If you mock the database
in an integration test, you are testing your mock, not your integration.

Typical boundaries:

- Application code + database (real database, test schema, transactions rolled
  back or test database torn down)
- Application code + external HTTP API (real test server or sandboxed
  environment, not an HTTP mock)
- Application code + filesystem (real temp directory, cleaned up after)
- Module A + Module B across a package boundary

### End-to-End Tests

Test the system from the user's perspective. Start the real application, drive
it through real user workflows, assert on observable outcomes.

E2e tests prove the system works. They do not prove why it works. When an e2e
test fails, you need unit and integration tests to locate the fault.

Keep e2e suites small: 10-50 tests covering critical user journeys. Every e2e
test you add increases maintenance cost and flake risk. A flaky e2e suite that
gets ignored is worse than no e2e suite.

## Test-Driven Development

Red-green-refactor:

1. **Red.** Write a test that fails. The test describes the behavior you want.
   Compile errors count as red.
2. **Green.** Write the minimum code to make the test pass. Do not write more
   than the test requires.
3. **Refactor.** Clean up the code and the test. Both must remain green.

### When TDD Works

- Well-defined input/output behavior (parsing, validation, computation)
- Bug fixes (write the reproducing test first, then fix)
- API design (the test acts as the first consumer of the API)
- When you know what the function should do but not how to implement it

### When TDD Does Not Work

- Exploratory code where you do not know the shape of the solution yet. Spike
  first, then write tests for the code you keep.
- UI layout and visual design. Screenshot tests or visual regression tools work
  better than unit tests for appearance.
- Performance optimization. Write benchmarks, not TDD-style tests.
- Infrastructure glue where the test would be a 1:1 mirror of the
  implementation (e.g., "test that this config file has this value").

TDD is a design tool, not a religion. Use it when it produces better code.
Skip it when it produces busywork.

## Test Design

### Arrange-Act-Assert

Every test has three phases:

```
// Arrange: set up preconditions
input := buildValidOrder(quantity: 5)

// Act: execute the behavior under test
result, err := processOrder(input)

// Assert: verify the outcome
require.NoError(t, err)
assert.Equal(t, StatusConfirmed, result.Status)
```

Separate the three phases with blank lines. If arrange is more than 10 lines,
extract a helper or factory.

### One Assertion per Concept

A test should verify one logical concept, not one `assert` call. Testing that
a function returns the right struct might require asserting on 3 fields. That
is one concept (correct return value), not three tests.

But testing that a function validates input AND processes the order AND sends
a notification is three concepts and should be three tests.

### Test Names as Documentation

Test names describe behavior, not implementation:

- Good: `TestProcessOrder_RejectsNegativeQuantity`
- Good: `TestResolveIdentity_FallsBackToGlobalWhenRepoMissing`
- Bad: `TestProcessOrder3`
- Bad: `TestValidate`

In table-driven tests, the subtest name is the documentation:

```go
{name: "negative quantity rejected", ...}
{name: "zero quantity rejected", ...}
{name: "quantity exceeding inventory rejected", ...}
```

A reader should understand what the test covers without reading the test body.

## Table-Driven Tests

Parameterized tests eliminate copy-paste and make edge cases visible:

```go
tests := []struct {
    name    string
    input   int
    want    int
    wantErr bool
}{
    {name: "positive", input: 5, want: 25, wantErr: false},
    {name: "zero", input: 0, want: 0, wantErr: false},
    {name: "negative", input: -1, want: 0, wantErr: true},
    {name: "overflow", input: math.MaxInt64, want: 0, wantErr: true},
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        got, err := Square(tt.input)
        if tt.wantErr {
            require.Error(t, err)
            return
        }
        require.NoError(t, err)
        assert.Equal(t, tt.want, got)
    })
}
```

### Edge Cases to Cover Systematically

- Zero, one, many (empty input, single element, typical case)
- Boundary values (max int, empty string, nil pointer)
- Error paths (invalid input, missing dependency, timeout)
- Unicode and encoding (multi-byte characters, BOM, mixed encodings)
- Concurrency (parallel access, race conditions under `-race`)

When adding a table-driven test, spend 2 minutes listing edge cases before
writing any test code. The table is only as good as the cases it contains.

## Mocking and Test Doubles

### Vocabulary

- **Stub**: returns canned data. No assertions on how it was called.
- **Mock**: records calls and asserts on them. Use sparingly.
- **Fake**: a working implementation with shortcuts (in-memory database, local
  HTTP server).
- **Spy**: wraps a real implementation and records calls.

### When to Mock

Mock at architectural boundaries:

- External HTTP APIs you do not control
- Third-party services (payment processors, email providers)
- Time (inject a clock, do not mock `time.Now` globally)
- Random number generators (inject a seed or a fixed source)

### When Not to Mock

Do not mock:

- Your own code's internals. If function A calls function B and both are in
  your package, test them together. Mocking B couples the test to A's
  implementation, not its behavior.
- The standard library. Do not mock `os.ReadFile`. Use a real temp file.
- Data structures. Do not mock a map or a slice.
- Anything that is fast and deterministic. The real thing is a better test
  than a mock.

Over-mocking is the most common test design mistake. When a test breaks
because of a refactor that did not change behavior, the test mocked too deep.

## Fixtures and Factories

### Shared Test Setup

`TestMain` (Go) or `beforeAll`/`beforeEach` (JS/TS) runs setup code once per
package or once per test. Use it for:

- Starting a test database
- Loading seed data
- Building expensive objects that tests share read-only

Do not use shared setup for mutable state. Each test must start with a clean
slate. If tests share mutable state, they are order-dependent and will break
when run in parallel or isolation.

### Builder Pattern

For complex test objects, use a builder:

```go
func buildOrder(opts ...func(*Order)) *Order {
    o := &Order{
        ID:       "test-order-1",
        Quantity: 1,
        Status:   StatusPending,
    }
    for _, opt := range opts {
        opt(o)
    }
    return o
}

// Usage:
order := buildOrder(func(o *Order) { o.Quantity = 100 })
```

The builder provides sensible defaults. Tests override only the fields they
care about. This prevents tests from breaking when you add a new required
field -- you update the builder once, not every test.

### Test Data Management

- Generate test data programmatically. Do not check in JSON fixtures that no
  one understands 6 months later.
- If you must use fixture files, keep them next to the test file in a
  `testdata/` directory.
- Never use production data in tests. Aside from privacy concerns, production
  data changes and breaks tests.

## Integration Testing

### Database Tests

- Use a real database instance (Docker container, in-memory SQLite, test
  Postgres).
- Each test gets its own schema or runs in a transaction that rolls back.
- Test the actual queries your code runs, not hand-written SQL that
  approximates them.
- Assert on the data, not on the SQL. The query is an implementation detail.

### API Tests

- Start a real HTTP server in-process (`httptest.NewServer` in Go,
  `supertest` in Node).
- Test request/response pairs: status codes, headers, body content.
- Test error responses with the same thoroughness as success responses.
  Clients depend on error shapes.
- Test authentication and authorization paths explicitly.

### External Service Boundaries

When your code calls an external service:

1. Define an interface at the boundary.
2. Write integration tests against the real service in a sandbox/test
   environment.
3. Write a fake implementation for unit tests that exercises the same
   interface.
4. Contract tests verify the fake behaves like the real service.

The fake is only trustworthy if contract tests keep it honest.

## End-to-End Testing

### User Journey Coverage

Identify the 5-10 most critical user journeys. Each e2e test covers one
complete journey from start to finish:

- User signs up, configures settings, performs core action, sees result
- User encounters an error, sees the error message, recovers

Do not write e2e tests for edge cases. That is what unit tests are for.
E2e tests answer: "Does the happy path work? Does the critical error path
work?"

### Flaky Test Prevention

Flaky tests undermine confidence. Causes and mitigations:

- **Timing**: never use `sleep` to wait for async operations. Poll with
  timeout, or use explicit synchronization (channels, events, condition
  variables).
- **Shared state**: each test creates and destroys its own state. No reliance
  on test execution order.
- **Network**: use local servers. If you must hit an external service, retry
  with backoff, but accept that the test may be inherently flaky.
- **Randomness**: seed random generators. Log the seed so failures are
  reproducible.
- **Race conditions**: run under race detection. If a test is flaky only under
  `-race`, you found a real bug in the code, not a test problem.

A flaky test that is not investigated within 48 hours should be quarantined
(marked skip with a tracking issue), not left in the suite to erode trust.

### Deterministic Setup

E2e test environments must be reproducible:

- Infrastructure as code (Docker Compose, test fixtures)
- Seeded databases with known state
- Pinned dependency versions
- No reliance on external services that can change without notice

## Coverage

### What Coverage Measures

Statement coverage tells you which lines of code executed during tests. Branch
coverage tells you which conditional paths were taken. Both measure execution,
not correctness.

80% line coverage with thoughtful assertions is better than 100% line coverage
with no assertions. A test that calls a function and ignores the result
achieves coverage without testing anything.

### What Coverage Does Not Measure

- Whether assertions are meaningful
- Whether edge cases are covered
- Whether error messages are helpful
- Whether the code is correct (only that it ran)
- Whether integration boundaries work
- Whether the code handles concurrent access

### When 100% Coverage Is Wrong

- Generated code (protobuf stubs, SQL migrations)
- Platform-specific code you cannot run in CI (OS-specific branches)
- Panic handlers and fatal error paths that exist for defense-in-depth
- Trivial getters with no logic
- Main functions that just wire dependencies and call run

Chasing 100% produces tests that mirror implementation, break on refactors,
and test nothing meaningful. Aim for high coverage on code with logic and
decisions. Accept lower coverage on glue code.

## Test Infrastructure as Code

### Test Utilities

Write helper functions for repeated patterns:

```go
func requireJSONEqual(t *testing.T, expected, actual string) {
    t.Helper()
    var e, a any
    require.NoError(t, json.Unmarshal([]byte(expected), &e))
    require.NoError(t, json.Unmarshal([]byte(actual), &a))
    assert.Equal(t, e, a)
}
```

`t.Helper()` marks the function as a helper so test failure messages point to
the caller, not the helper.

### Custom Assertions

When a domain concept appears in many tests, write a domain-specific
assertion:

```go
func assertOrderConfirmed(t *testing.T, order *Order) {
    t.Helper()
    assert.Equal(t, StatusConfirmed, order.Status)
    assert.NotZero(t, order.ConfirmedAt)
    assert.NotEmpty(t, order.ConfirmationID)
}
```

This is not about saving keystrokes. It names the concept being tested. When
the assertion fails, the reader knows what invariant was violated.

### Treat Test Code as Production Code

- No copy-paste between test files. Extract shared logic.
- Review test code in PRs with the same rigor as production code.
- Refactor test helpers when they accumulate parameters or flags.
- Delete tests that no longer test meaningful behavior.

## Regression Testing

### Every Bug Fix Gets a Test

Before fixing a bug:

1. Write a test that reproduces the bug. Watch it fail.
2. Fix the bug. Watch the test pass.
3. The test stays in the suite forever.

This is non-negotiable. A bug that was fixed without a regression test will
come back. The test is proof that the bug existed and proof that it is fixed.

### Reproduce Before Fixing

Never fix a bug you cannot reproduce. If you cannot write a failing test, you
do not understand the bug. "I think this is the problem" is not a root cause.
A failing test is a root cause.

If the bug is environment-specific, reproduce the environment. If the bug is
timing-dependent, write a test that forces the timing. If you truly cannot
reproduce, log the investigation and move on -- do not commit a speculative
fix that you cannot verify.

### Grep for Siblings

When you find a bug caused by a pattern (e.g., missing nil check, off-by-one
in a loop, unclosed resource), grep the entire codebase for the same pattern.
Fix every instance, not just the one that was reported. A point fix that leaves
siblings broken is not a fix.

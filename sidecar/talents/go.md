# Go Engineering

Deep expertise in Go systems programming. Emphasis on simplicity, explicit
control flow, and code that is easy to read six months later. Follows the
Go proverbs: clear is better than clever, a little copying is better than
a little dependency, don't communicate by sharing memory — share memory by
communicating.

## Idiomatic Principles

Write Go that looks like Go. The language has strong conventions and a small
feature set for a reason — uniformity across codebases matters more than
local expressiveness.

- Prefer concrete types over abstractions until a second consumer exists.
- Name things for their role, not their type. `users` not `userSlice`.
- Short names for locals with small scope (`i`, `n`, `err`). Descriptive
  names for exports (`ResolveIdentity`, `SessionStore`).
- Zero-value usefulness: design structs so the zero value is valid. A
  `sync.Mutex` is ready to use without initialization. Your types should
  behave the same way.
- One blank line between top-level declarations, none inside functions
  unless separating logical blocks.
- `gofmt` is non-negotiable. If code is not gofmt'd, it is not Go code.

## Package Design

A package is a unit of compilation, a namespace, and a documentation
boundary. Get it right early — renaming a package breaks every importer.

### Small Interfaces

Define interfaces at the consumer, not the producer. A package that needs
to read bytes should declare a one-method interface, not import a concrete
type from elsewhere.

```go
// In the consumer package:
type Reader interface {
    Read(p []byte) (n int, err error)
}
```

Accept interfaces, return structs. The producer returns a concrete type;
the consumer defines the interface it needs. This keeps coupling minimal
and avoids the "interface of everything" pattern.

### Package Naming

- Lowercase, single-word when possible: `session`, `resolve`, `identity`.
- No `util`, `common`, `helpers`, `misc` — these are code smells that mean
  the package has no cohesive purpose.
- The package name is part of the call site: `session.New()` reads well,
  `session.NewSession()` stutters.
- `internal/` for packages that must not be imported outside the module.

### Package Boundaries

Each package should have one clear responsibility stated in its doc
comment. If you cannot describe what a package does in one sentence, split
it. A package should not import its sibling packages in a cycle — the
compiler enforces this, but design for it up front.

Avoid deep package hierarchies. `internal/session/store/` is almost always
better collapsed into `internal/session/`. Depth does not add clarity.

## Error Handling

Errors in Go are values. Treat them with the same rigor as any other return
value.

### Wrapping with Context

Every error returned from a function should carry enough context for the
caller to understand what failed without reading the source.

```go
f, err := os.Open(path)
if err != nil {
    return fmt.Errorf("load config %s: %w", path, err)
}
```

Use `%w` for wrapping so callers can use `errors.Is` and `errors.As`.
Use `%v` only when you intentionally want to break the error chain (hiding
implementation details from callers).

### Sentinel Errors

Define sentinel errors for conditions callers need to check programmatically.

```go
var ErrNotFound = errors.New("identity not found")
```

Sentinel errors are part of your API. Name them `Err<Condition>`. Document
when they are returned. Never change their message string after release.

### Error Types

When callers need structured information beyond a string, use a custom type.

```go
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation: %s: %s", e.Field, e.Message)
}
```

Check with `errors.As`:

```go
var ve *ValidationError
if errors.As(err, &ve) {
    log.Printf("field %s: %s", ve.Field, ve.Message)
}
```

### What Not to Do

- Never ignore errors. `f, _ := os.Open(path)` is a latent crash.
- Never `log.Fatal` or `os.Exit` in library code — only in `main`.
- Never panic for expected conditions. Panics are for programmer errors
  (index out of range, nil pointer in code that should never have one).
- Never return `errors.New("something failed")` without context about what
  was attempted.

## Concurrency

Go's concurrency model is goroutines plus channels. Use them, but use them
deliberately.

### Goroutines

A goroutine is cheap to start (~4KB stack) but not free to manage. Every
goroutine you start must have a clear shutdown path. If you cannot answer
"how does this goroutine stop?" then do not start it.

```go
func process(ctx context.Context, items <-chan Item) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case item, ok := <-items:
            if !ok {
                return nil
            }
            handle(item)
        }
    }
}
```

### Channels

Channels are for communication, not for synchronization (use `sync`
primitives for that). Unbuffered channels synchronize sender and receiver.
Buffered channels decouple them.

- Prefer unbuffered channels unless you have measured a need for buffering.
- The sender closes the channel, never the receiver.
- A nil channel blocks forever — useful for disabling a select case.
- A receive from a closed channel returns the zero value immediately.

### sync Primitives

- `sync.Mutex`: protect shared state. Hold the lock for the minimum
  critical section. Never hold a lock while doing I/O or calling external
  functions.
- `sync.RWMutex`: multiple concurrent readers, exclusive writer. Use only
  when reads vastly outnumber writes and profiling confirms contention.
- `sync.Once`: safe lazy initialization. Preferred over `init()`.
- `sync.WaitGroup`: wait for a group of goroutines to finish.
- `sync.Map`: rarely appropriate. A regular map with a mutex is almost
  always clearer and faster.

### Context Propagation

`context.Context` is the first parameter of every function that does I/O,
makes an RPC, or might need cancellation.

```go
func FetchIdentity(ctx context.Context, handle string) (*Identity, error)
```

- Never store a context in a struct field. Pass it explicitly.
- Derive child contexts with `context.WithCancel`, `context.WithTimeout`,
  or `context.WithValue` (sparingly — context values are untyped).
- Check `ctx.Err()` before expensive operations.
- `context.Background()` at the top of `main()` and in tests. Nowhere else.

### Common Concurrency Bugs

- Data races: two goroutines accessing the same variable without
  synchronization, at least one writing. Always run tests with `-race`.
- Goroutine leaks: a goroutine blocks on a channel or context that never
  resolves. Use `goleak` in tests to detect these.
- Deadlocks: two goroutines each waiting for the other. Keep lock
  hierarchies simple and consistent.

## Testing

Tests are not an afterthought. They are the specification of what the code
does.

### Table-Driven Tests

The standard Go testing pattern. One test function, many cases.

```go
func TestResolve(t *testing.T) {
    tests := []struct {
        name    string
        handle  string
        want    string
        wantErr bool
    }{
        {name: "valid handle", handle: "mal", want: "Mal Reynolds"},
        {name: "missing handle", handle: "nobody", wantErr: true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Resolve(tt.handle)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got.Name)
        })
    }
}
```

### testify

Use `assert` for checks that should continue, `require` for checks that
must stop the test. `require` calls `t.FailNow()` — use it for
preconditions.

```go
require.NoError(t, err)        // Stop if error; no point continuing
assert.Equal(t, want, got)     // Log failure but keep going
assert.Contains(t, output, "expected substring")
```

### Race Detection

Always run tests with `-race`. The race detector finds data races that
cause real production bugs. A test suite that passes without `-race` and
fails with it has real bugs.

```bash
go test -race -count=1 ./...
```

`-count=1` disables test caching. Cached results hide flaky tests.

### Test Helpers

Use `t.Helper()` in helper functions so failure messages report the
caller's line, not the helper's.

```go
func loadFixture(t *testing.T, name string) []byte {
    t.Helper()
    data, err := os.ReadFile(filepath.Join("testdata", name))
    require.NoError(t, err)
    return data
}
```

Use `t.TempDir()` for temporary directories — cleaned up automatically.
Use `t.Setenv()` for environment variables — restored automatically.

### Golden Files

For complex output (CLI output, generated code, serialized data), use
golden files in `testdata/`. Compare actual output against the golden file.
Update with a flag:

```go
var update = flag.Bool("update", false, "update golden files")

func TestOutput(t *testing.T) {
    got := render()
    golden := filepath.Join("testdata", t.Name()+".golden")
    if *update {
        os.WriteFile(golden, got, 0o644)
    }
    want, _ := os.ReadFile(golden)
    assert.Equal(t, string(want), string(got))
}
```

### Testing Boundaries

- Test exported behavior, not internal implementation.
- Use `_test` package suffix to enforce this: `package session_test`.
- Mock at boundaries (filesystem, network, time), not between internal
  packages. Prefer fakes (in-memory implementations) over mocks.
- Avoid `TestMain` unless you truly need global setup/teardown.

## Performance

### Avoid Premature Optimization

Write clear code first. Optimize only when profiling shows a bottleneck.
Most Go code is fast enough without micro-optimization. The biggest
performance wins come from algorithmic improvements and reducing
allocations, not from bit tricks.

### Profiling with pprof

```go
import _ "net/http/pprof"

// In main:
go http.ListenAndServe("localhost:6060", nil)
```

Then: `go tool pprof http://localhost:6060/debug/pprof/heap`

For CPU: `go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30`

### Benchmarks

```go
func BenchmarkResolve(b *testing.B) {
    for b.Loop() {
        Resolve("mal")
    }
}
```

Run with `go test -bench=. -benchmem ./...`. The `-benchmem` flag shows
allocations per operation — often the most actionable metric.

Compare benchmarks across changes with `benchstat`:

```bash
go test -bench=. -count=10 ./... > old.txt
# make changes
go test -bench=. -count=10 ./... > new.txt
benchstat old.txt new.txt
```

### Allocation Reduction

- Pre-allocate slices when the length is known: `make([]T, 0, n)`.
- Use `strings.Builder` for string concatenation in loops.
- Avoid `fmt.Sprintf` in hot paths — it allocates.
- Pass large structs by pointer to avoid copies.
- Use `sync.Pool` for frequently allocated and discarded objects (buffers,
  temporary structs). Profile first.

## Standard Library Preference

The Go standard library is extensive and stable. Prefer it over third-party
packages unless the third-party package provides substantial value that the
standard library does not.

Packages worth knowing well:

- `net/http`: production-ready HTTP server and client.
- `encoding/json`: JSON marshaling/unmarshaling. Use struct tags.
- `os`, `os/exec`, `path/filepath`: filesystem and process operations.
- `text/template`, `html/template`: safe templating.
- `testing`, `testing/fstest`: testing primitives.
- `io`, `io/fs`: reader/writer abstractions, filesystem interfaces.
- `context`: cancellation and deadline propagation.
- `log/slog`: structured logging (Go 1.21+).
- `errors`: wrapping, unwrapping, `Is`, `As`.

Third-party packages that have earned their place:

- `github.com/stretchr/testify`: assertions and requirements in tests.
- `github.com/spf13/cobra`: CLI framework with subcommands and flags.
- `golang.org/x/tools/cmd/staticcheck`: the standard static analyzer.
- `gopkg.in/yaml.v3`: YAML parsing (stdlib has no YAML support).

Avoid adding dependencies for things the standard library handles: HTTP
routing (use `net/http` mux or Go 1.22+ patterns), UUID generation (use
`crypto/rand`), configuration parsing (use `os.Getenv` or a YAML file).

## Common Anti-Patterns

### init() Functions

`init()` runs before `main()`, has no parameters, returns no errors, and
cannot be tested in isolation. It makes program startup order implicit and
fragile. Prefer explicit initialization in `main()` or `sync.Once`.

```go
// Bad:
func init() {
    db = connectDB() // untestable, panic on failure
}

// Good:
func main() {
    db, err := connectDB()
    if err != nil {
        log.Fatal(err)
    }
}
```

### Global State

Global variables make testing difficult, concurrency unsafe, and
initialization order fragile. Pass dependencies explicitly.

```go
// Bad:
var store = NewStore()

// Good: inject via struct field or function parameter
type Server struct {
    store *Store
}
```

### Interface Pollution

Do not define an interface until you have at least two implementations or
a concrete testing need. An interface with one implementation is just
indirection.

```go
// Bad: interface defined alongside its only implementation
type UserService interface {
    GetUser(id string) (*User, error)
}
type userService struct{}

// Good: consumer defines what it needs
// In the consumer package:
type UserGetter interface {
    GetUser(id string) (*User, error)
}
```

### Naked Returns

Named return values are useful for documentation. Naked returns (returning
without naming the values) are confusing in any function longer than a few
lines. Always return explicitly.

```go
// Bad:
func parse(s string) (result int, err error) {
    result, err = strconv.Atoi(s)
    return // what is being returned?
}

// Good:
func parse(s string) (int, error) {
    n, err := strconv.Atoi(s)
    if err != nil {
        return 0, fmt.Errorf("parse %q: %w", s, err)
    }
    return n, nil
}
```

### Overuse of Generics

Go added generics in 1.18, but they are not always the right tool. Use
generics for data structures and algorithms that truly work with any type.
Do not use them to avoid writing two similar functions — a little copying
is better than a little dependency on complex type constraints.

### Empty Interface Abuse

`interface{}` (or `any`) erases type information. Use it at API boundaries
(JSON unmarshaling, printf-style functions) but not inside your own code.
If you find yourself type-asserting everywhere, the design is wrong.

## Module and Dependency Management

### go.mod and go.sum

`go.mod` declares the module path and direct dependencies. `go.sum` records
cryptographic hashes for reproducible builds. Both are committed.

- `go mod tidy` before every commit — removes unused dependencies, adds
  missing ones.
- Pin to specific versions, not branches.
- Review new dependencies before adding: license, maintenance status,
  transitive dependency count.
- A module with 0 dependencies is better than one with 10.

### Versioning

Follow semantic versioning. Breaking changes require a major version bump
and a new module path suffix (`/v2`).

For internal tools and CLIs that are not imported as libraries, version
strings track releases but the v0/v1/v2 import path concern does not apply.

### Vendor vs Module Cache

Most projects use the module cache (`GOPATH/pkg/mod`). Use `go mod vendor`
only when you need hermetic builds without network access. If you vendor,
commit the `vendor/` directory and use `go build -mod=vendor`.

## Build and Tooling

### Required Tools

- `go vet`: catches common mistakes (printf format mismatches, unreachable
  code, struct tag errors). Run on every save.
- `staticcheck`: the standard static analyzer. Catches more subtle issues
  than `go vet` (deprecated API usage, ineffective assignments, unnecessary
  conversions).
- `gofmt` / `goimports`: canonical formatting. Non-negotiable.
- `go test -race`: race detector. Required in CI and local testing.

### Build Tags

Use build tags for platform-specific code or optional features.

```go
//go:build linux

package process
```

Prefer build tags over runtime checks when the code path is entirely
different per platform.

### Makefile Conventions

A Go project Makefile should have at minimum:

- `build`: compile the binary.
- `test`: run `go test -race -count=1 ./...`.
- `lint`: run `go vet` and `staticcheck`.
- `check`: run lint + test. This is the gate.
- `install`: build and copy to a known location.
- `clean`: remove build artifacts.

### Cross-Compilation

Go cross-compiles with `GOOS` and `GOARCH`:

```bash
GOOS=linux GOARCH=amd64 go build -o ethos-linux-amd64
GOOS=darwin GOARCH=arm64 go build -o ethos-darwin-arm64
```

No special toolchain needed. Static binaries by default (no CGo).

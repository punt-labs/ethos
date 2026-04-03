# TypeScript

Engineering discipline for writing correct, maintainable TypeScript. Covers
the type system, patterns, tooling, and common mistakes.

## Type System

### Strict Mode

Every `tsconfig.json` starts with `strict: true`. This enables:

- `strictNullChecks` -- `null` and `undefined` are not assignable to other
  types without explicit annotation
- `noImplicitAny` -- every binding has a type, inferred or declared
- `strictFunctionTypes` -- function parameter types are checked
  contravariantly
- `strictPropertyInitialization` -- class properties must be initialized in
  the constructor or marked optional

Turning off any of these flags to fix a type error means the type error is
real and hiding it creates a runtime bug.

### noUncheckedIndexedAccess

Enable this. When you index into an array or record, the result type includes
`undefined`:

```typescript
const items: string[] = ["a", "b"];
const first = items[0]; // string | undefined, not string
```

This catches out-of-bounds access at compile time. Without it, TypeScript
assumes every index access succeeds, which is false.

### exactOptionalPropertyTypes

Enable this. It distinguishes between "property is missing" and "property is
present but undefined":

```typescript
interface Config {
  timeout?: number;
}

// With exactOptionalPropertyTypes:
const a: Config = {};              // ok: timeout is missing
const b: Config = { timeout: 10 }; // ok: timeout is present
const c: Config = { timeout: undefined }; // error: must omit, not set to undefined
```

This matters when code checks `"timeout" in config` versus
`config.timeout !== undefined`.

## Type Design

### Discriminated Unions

Model states as discriminated unions, not optional fields:

```typescript
// Bad: unclear which fields exist in which state
interface Request {
  status: "pending" | "success" | "error";
  data?: ResponseData;
  error?: Error;
}

// Good: each state carries exactly its own fields
type Request =
  | { status: "pending" }
  | { status: "success"; data: ResponseData }
  | { status: "error"; error: Error };
```

The compiler enforces that you handle every case. Adding a new variant breaks
all incomplete `switch` statements at compile time.

Always add a `default: exhaustive(status)` branch:

```typescript
function exhaustive(value: never): never {
  throw new Error(`Unhandled case: ${value}`);
}
```

### Branded Types

Prevent mixing structurally identical types:

```typescript
type UserId = string & { readonly __brand: "UserId" };
type OrderId = string & { readonly __brand: "OrderId" };

function getUser(id: UserId): User { ... }

const orderId = "abc" as OrderId;
getUser(orderId); // compile error: OrderId is not UserId
```

Use branded types when mixing up two string or number parameters causes bugs
(IDs, currency amounts, pixel vs rem values).

### Type Narrowing

TypeScript narrows types through control flow analysis. Write code that
narrows naturally:

```typescript
// typeof guard
if (typeof value === "string") {
  // value is string here
}

// in guard
if ("error" in response) {
  // response has an error property
}

// Discriminated union narrowing
switch (request.status) {
  case "success":
    // request is { status: "success"; data: ResponseData }
    break;
}

// User-defined type guards
function isNonNull<T>(value: T | null): value is T {
  return value !== null;

}

const results = items.map(process).filter(isNonNull);
```

Prefer narrowing over type assertions. Narrowing is checked by the compiler.
Assertions are not.

### The never Type

`never` represents values that cannot exist. It is the bottom type -- no value
is assignable to `never`.

Uses:

- Exhaustive switch checking (shown above)
- Functions that never return (`throw`, `process.exit`, infinite loops)
- Conditional type filtering: `Extract<T, U>`, `Exclude<T, U>`

If you see `never` where you did not expect it, you have a type error
upstream. Do not cast through `unknown` to escape it.

## Generics

### Constraints

Constrain generics to the minimum interface needed:

```typescript
// Too broad: accepts anything
function first<T>(items: T[]): T | undefined { ... }

// Appropriately constrained
function getProperty<T, K extends keyof T>(obj: T, key: K): T[K] { ... }
```

The constraint `K extends keyof T` means the compiler verifies the key exists
on the object. Without it, you get `any` at the call site.

### Inference

Let TypeScript infer when it can. Do not annotate generic parameters at call
sites unless inference fails:

```typescript
// Unnecessary: TypeScript infers T = number
const result = first<number>([1, 2, 3]);

// Preferred: let inference work
const result = first([1, 2, 3]);
```

When inference produces a wider type than you want, annotate the variable, not
the generic parameter:

```typescript
const ids: readonly string[] = getIds();
```

### Conditional Types

Types that depend on a condition:

```typescript
type IsString<T> = T extends string ? true : false;
type A = IsString<"hello">; // true
type B = IsString<42>;      // false
```

Practical uses:

- `Extract<T, U>` -- members of T assignable to U
- `Exclude<T, U>` -- members of T not assignable to U
- `NonNullable<T>` -- remove null and undefined
- `ReturnType<T>` -- infer the return type of a function type

Conditional types distribute over unions. `IsString<string | number>` becomes
`true | false`, which simplifies to `boolean`. This is usually what you want,
but if not, wrap both sides in a tuple: `[T] extends [string]`.

### Mapped Types

Transform existing types:

```typescript
// Make all properties optional
type Partial<T> = { [K in keyof T]?: T[K] };

// Make all properties readonly
type Readonly<T> = { readonly [K in keyof T]: T[K] };

// Rename keys with `as`
type Getters<T> = {
  [K in keyof T as `get${Capitalize<string & K>}`]: () => T[K];
};
```

Mapped types combined with conditional types and template literal types can
express complex transformations at the type level. Use this power sparingly.
If a type takes more than 30 seconds to understand, it needs a comment or a
simpler design.

## Error Handling

### Result Pattern

Do not throw exceptions across module boundaries. Callers cannot know what
exceptions a function throws -- TypeScript has no checked exceptions.

Use a Result type:

```typescript
type Result<T, E = Error> =
  | { ok: true; value: T }
  | { ok: false; error: E };

function parseConfig(raw: string): Result<Config, ParseError> {
  try {
    const data = JSON.parse(raw);
    return { ok: true, value: validate(data) };
  } catch (e) {
    return { ok: false, error: new ParseError(e) };
  }
}
```

Callers must handle the error case to access the value. The compiler enforces
this through narrowing.

### Typed Errors

Define error types as discriminated unions:

```typescript
type AppError =
  | { kind: "not_found"; resource: string; id: string }
  | { kind: "validation"; field: string; message: string }
  | { kind: "unauthorized"; reason: string };
```

This replaces `catch (e: unknown)` guessing with compile-time exhaustive
handling. Errors are data, not exception hierarchies.

### Never Throw Across Boundaries

Inside a module, `throw` is fine for programmer errors (assertion failures,
impossible states). Across a module boundary -- an exported function, an API
handler, a library entry point -- return a Result or an error value.

The boundary rule: if the caller cannot see your source code, they cannot
handle your exception correctly.

## React Patterns

### Functional Components

All components are functions. No class components in new code.

```typescript
interface UserCardProps {
  user: User;
  onSelect: (id: UserId) => void;
}

function UserCard({ user, onSelect }: UserCardProps) {
  return (
    <div onClick={() => onSelect(user.id)}>
      {user.name}
    </div>
  );
}
```

Type props as an interface, not inline. Export the props interface when other
components need to compose with it.

### Hooks

Hooks are the mechanism for state, effects, and shared logic in function
components.

Rules (enforced by eslint-plugin-react-hooks):

- Call hooks at the top level only, never inside conditions or loops
- Call hooks from function components or custom hooks only

Custom hooks extract reusable stateful logic:

```typescript
function useDebounce<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}
```

Name custom hooks with the `use` prefix. Return typed values, not `any`.

### Context

Context provides dependency injection for the component tree. Use it for
cross-cutting concerns: theme, locale, auth state, feature flags.

Do not use context for high-frequency state (form inputs, animation frames).
Every context value change re-renders every consumer.

Split context by update frequency:

```typescript
// Good: auth changes rarely
const AuthContext = createContext<AuthState | null>(null);

// Bad: putting form state in context re-renders the whole tree
```

### State Management

Local state (`useState`) first. Lift state up only when two components need
the same data. Extract to a store (Zustand, Jotai) only when prop drilling
crosses more than 2-3 levels and the data is not a cross-cutting concern.

Server state (data fetching, caching, invalidation) belongs in a server state
library (TanStack Query, SWR), not in component state or a global store.

## Module System

### ESM

Use ES modules exclusively. `import`/`export`, not `require`/`module.exports`.

```typescript
// Named exports for most things
export function parseConfig(raw: string): Config { ... }
export interface Config { ... }

// Default export only for React components (convention)
export default function UserCard(props: UserCardProps) { ... }
```

Set `"type": "module"` in `package.json`. Set `"module": "ESNext"` and
`"moduleResolution": "bundler"` (or `"nodenext"` for Node libraries) in
`tsconfig.json`.

### Barrel Files

A barrel (`index.ts`) re-exports from a directory:

```typescript
// components/index.ts
export { UserCard } from "./UserCard";
export { OrderList } from "./OrderList";
```

Barrels simplify imports but harm tree-shaking. Importing one symbol from a
barrel pulls in the entire barrel during bundling unless the bundler supports
deep scope analysis.

Rule of thumb: use barrels at package boundaries (the public API of a
library). Do not use barrels inside a package for convenience -- import
directly from the source file.

### Tree-Shaking Implications

Tree-shaking removes unused exports from the bundle. It works when:

- Modules use ESM (not CommonJS)
- Exports are statically analyzable (no `module.exports = computedObject`)
- Side effects are declared in `package.json` `"sideEffects"` field
- No barrel file forces loading of unrelated modules

Mark packages as side-effect-free (`"sideEffects": false`) when they are.
This lets bundlers drop entire modules when nothing is imported from them.

## Testing

### Vitest

Vitest is the standard test runner for TypeScript projects. It shares Vite's
config, supports ESM natively, and runs tests in parallel.

```typescript
import { describe, it, expect } from "vitest";

describe("parseConfig", () => {
  it("parses valid JSON", () => {
    const result = parseConfig('{"timeout": 30}');
    expect(result).toEqual({ ok: true, value: { timeout: 30 } });
  });

  it("returns error for invalid JSON", () => {
    const result = parseConfig("not json");
    expect(result.ok).toBe(false);
  });
});
```

Use `describe` for grouping, `it` for individual cases. Name tests as
behavior descriptions.

### Testing Library

Test components by how users interact with them, not by implementation:

```typescript
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

it("calls onSelect when clicked", async () => {
  const onSelect = vi.fn();
  render(<UserCard user={testUser} onSelect={onSelect} />);

  await userEvent.click(screen.getByText(testUser.name));

  expect(onSelect).toHaveBeenCalledWith(testUser.id);
});
```

Query by role, label, or text -- not by CSS class or test ID. If you cannot
find an element by its accessible role, the component has an accessibility
problem.

### MSW for API Mocking

Mock Service Worker intercepts HTTP requests at the network level:

```typescript
import { setupServer } from "msw/node";
import { http, HttpResponse } from "msw";

const server = setupServer(
  http.get("/api/users/:id", ({ params }) => {
    return HttpResponse.json({ id: params.id, name: "Test User" });
  })
);

beforeAll(() => server.listen());
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
```

MSW works with any HTTP client (fetch, axios, got) without changing
application code. Override handlers per-test for error scenarios:

```typescript
it("shows error on 500", async () => {
  server.use(
    http.get("/api/users/:id", () => {
      return new HttpResponse(null, { status: 500 });
    })
  );
  // ... test error UI
});
```

## Performance

### Bundle Size

Every dependency added to `package.json` increases the bundle. Before adding
a dependency:

1. Check its size on bundlephobia.com
2. Check if it is tree-shakeable
3. Ask whether the functionality justifies the cost

Common offenders: lodash (import individual functions, not the whole library),
moment.js (use date-fns or Temporal), full icon libraries (import individual
icons).

### Lazy Loading

Split code at route boundaries:

```typescript
const Settings = lazy(() => import("./pages/Settings"));
```

Components that are not visible on initial load (modals, tabs, routes below
the fold) are candidates for lazy loading. The initial bundle should contain
only what the user sees on first render.

### Memoization

`React.memo` prevents re-renders when props have not changed. Use it when:

- A component is expensive to render (large lists, complex SVG)
- A component receives the same props frequently (parent re-renders often)

Do not memo everything. Memo has a cost (comparison on every render). For
simple components, the comparison is more expensive than re-rendering.

`useMemo` caches a computed value. `useCallback` caches a function reference.
Use them when:

- The value or function is passed to a memoized child component
- The computation is expensive (>1ms)

Do not use them for every variable. Profile first.

## Common Anti-Patterns

### any

`any` disables type checking. It is a virus: once introduced, it spreads
through inference to every value that touches it.

Alternatives:

- `unknown` -- forces the caller to narrow before using
- Generics -- preserves the actual type
- Explicit interface -- describes the shape

The only acceptable use of `any` is at the boundary with untyped JavaScript
libraries that lack type definitions, and only in a thin wrapper that
immediately narrows to a typed interface.

### Type Assertions

`value as SomeType` tells the compiler to trust you. The compiler does not
check assertions at runtime. If you are wrong, you get silent corruption.

When you feel the need for an assertion, ask:

- Can I narrow with a type guard instead?
- Can I change the upstream type to be more precise?
- Is the code structured so the type system cannot track this?

If the answer to the third question is yes, restructure the code. If you must
assert, add a comment explaining why the assertion is safe.

### Enums vs Union Types

TypeScript enums generate runtime code and have surprising behavior (reverse
mappings, numeric enums auto-incrementing).

Use string literal unions:

```typescript
// Prefer
type Status = "pending" | "active" | "closed";

// Avoid
enum Status {
  Pending = "pending",
  Active = "active",
  Closed = "closed",
}
```

String unions are simpler, tree-shake away, and work with discriminated unions
naturally.

`const enum` inlines values but breaks with `--isolatedModules` (required by
most bundlers). Do not use it.

### Namespace Overuse

TypeScript namespaces (`namespace Foo { ... }`) are a pre-ESM module pattern.
In modern TypeScript with ES modules, they add indirection without benefit.

Use modules (files) for organization. Use named exports for the public API.
Reserve namespaces for declaration merging with third-party libraries (rare).

## Tooling

### tsc

Run `tsc --noEmit` as a lint step. It type-checks without producing output
files. Bundlers (Vite, esbuild) handle the actual compilation.

Set `"incremental": true` in `tsconfig.json` to speed up repeated checks.

### ESLint

Use `@typescript-eslint/eslint-plugin` with `@typescript-eslint/parser`. Key
rules:

- `@typescript-eslint/no-explicit-any` -- errors on `any`
- `@typescript-eslint/no-non-null-assertion` -- errors on `value!`
- `@typescript-eslint/strict-boolean-expressions` -- prevents truthy checks
  on non-boolean values
- `eslint-plugin-react-hooks/exhaustive-deps` -- catches missing hook
  dependencies

### Prettier

Prettier handles formatting. ESLint handles logic. Do not configure ESLint
rules that overlap with Prettier (indentation, semicolons, quotes).

Run Prettier as a pre-commit hook or CI step. Do not argue about formatting.

### tsconfig Best Practices

```json
{
  "compilerOptions": {
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true,
    "noImplicitReturns": true,
    "noFallthroughCasesInSwitch": true,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true,
    "verbatimModuleSyntax": true,
    "moduleResolution": "bundler",
    "module": "ESNext",
    "target": "ES2022",
    "skipLibCheck": true
  }
}
```

`skipLibCheck: true` skips type-checking `.d.ts` files from `node_modules`.
This is safe (library authors already checked their types) and cuts type-check
time by 50-80% on large projects.

`verbatimModuleSyntax` requires explicit `import type` for type-only imports,
ensuring bundlers can safely remove them.

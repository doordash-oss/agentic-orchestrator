# Type Design

## Interfaces vs Type Aliases

Both define object shapes. Use each where it fits best:

- **Interfaces** — for object shapes, especially public API contracts. They
  support declaration merging and `extends` for composition.
- **Type aliases** — for unions, intersections, mapped types, conditional types,
  and any type that isn't a plain object shape.

```typescript
// Interface for object shapes:
interface User {
  id: string;
  name: string;
  email: string;
}

// Type alias for unions and computed types:
type Status = "pending" | "active" | "suspended";
type UserWithStatus = User & { status: Status };
```

**Don't mix arbitrarily.** Pick a convention for your codebase and be consistent.
Many teams use interfaces for all object shapes and type aliases for everything
else.

## Discriminated Unions

Model variant types with a common literal discriminant property. TypeScript
narrows automatically in `switch` and `if` blocks:

```typescript
type Result<T> =
  | { ok: true; value: T }
  | { ok: false; error: Error };

function handle(result: Result<string>) {
  if (result.ok) {
    console.log(result.value); // narrowed to { ok: true; value: string }
  } else {
    console.error(result.error); // narrowed to { ok: false; error: Error }
  }
}
```

### Use for State Machines

Model impossible states as unrepresentable:

```typescript
// Bad — allows { loading: true, data: "hello", error: new Error() }
interface State {
  loading: boolean;
  data?: string;
  error?: Error;
}

// Good — each state is distinct
type State =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "success"; data: string }
  | { status: "error"; error: Error };
```

### Exhaustiveness Checking

Use `never` to catch unhandled cases at compile time:

```typescript
function assertNever(x: never): never {
  throw new Error(`Unexpected value: ${x}`);
}

function handle(state: State) {
  switch (state.status) {
    case "idle": return null;
    case "loading": return "...";
    case "success": return state.data;
    case "error": return state.error.message;
    default: return assertNever(state); // compile error if a case is missed
  }
}
```

## Branded Types (Nominal Typing)

TypeScript uses structural typing. Simulate nominal types to prevent mixing
structurally identical values:

```typescript
type USD = number & { readonly __brand: "USD" };
type EUR = number & { readonly __brand: "EUR" };

function createUSD(amount: number): USD {
  return amount as USD;
}

function createEUR(amount: number): EUR {
  return amount as EUR;
}

function addUSD(a: USD, b: USD): USD {
  return (a + b) as USD;
}

const price = createUSD(100);
const tax = createEUR(20);
addUSD(price, tax); // Error: EUR is not assignable to USD
```

Use branded types for IDs, currency, units, and any domain value where
structural equivalence would be a bug.

## `as const` and Const Assertions

Use `as const` to infer literal types instead of widened types:

```typescript
// Without as const:
const config = { mode: "dark", version: 3 };
// type: { mode: string; version: number }

// With as const:
const config = { mode: "dark", version: 3 } as const;
// type: { readonly mode: "dark"; readonly version: 3 }
```

Especially useful for defining route maps, action types, and configuration
objects where the literal values matter.

## `satisfies` Operator

Validates a value against a type without widening:

```typescript
type Colors = Record<string, [number, number, number] | string>;

// With type annotation — loses specific key info:
const colors: Colors = { red: [255, 0, 0], green: "#00ff00" };
colors.red; // type: [number, number, number] | string

// With satisfies — preserves literal types:
const colors = {
  red: [255, 0, 0],
  green: "#00ff00",
} satisfies Colors;
colors.red;   // type: [number, number, number]
colors.green; // type: string
```

Use `satisfies` when you want type checking without losing inference.

## Enums — When to Avoid

Prefer union types and `as const` objects over TypeScript enums:

```typescript
// Avoid — TypeScript enums have surprising runtime behavior:
enum Direction { Up, Down, Left, Right }

// Prefer — union of literal types:
type Direction = "up" | "down" | "left" | "right";

// Or — const object with as const for when you need runtime values:
const Direction = {
  Up: "up",
  Down: "down",
  Left: "left",
  Right: "right",
} as const;
type Direction = (typeof Direction)[keyof typeof Direction];
```

Enums generate runtime code, have unusual reverse-mapping behavior (numeric
enums), and don't tree-shake well. String literal unions are simpler and safer.

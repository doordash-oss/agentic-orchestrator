# Type Narrowing

TypeScript analyzes control flow to refine types at specific positions. This
process — called narrowing — makes broad types more specific after runtime checks.

## Built-in Narrowing

### `typeof` Guards

Narrows primitive types:

```typescript
function format(value: string | number): string {
  if (typeof value === "string") {
    return value.toUpperCase(); // narrowed to string
  }
  return value.toFixed(2); // narrowed to number
}
```

### `instanceof` Guards

Narrows class instances:

```typescript
function getLength(input: string | string[]): number {
  if (input instanceof Array) {
    return input.length; // narrowed to string[]
  }
  return input.length; // narrowed to string
}
```

### `in` Operator

Narrows based on property existence — especially useful with discriminated unions:

```typescript
type Fish = { swim: () => void };
type Bird = { fly: () => void };

function move(animal: Fish | Bird) {
  if ("swim" in animal) {
    animal.swim(); // narrowed to Fish
  } else {
    animal.fly(); // narrowed to Bird
  }
}
```

### Truthiness Narrowing

Eliminates `null`, `undefined`, `0`, `""`, `false`, and `NaN`:

```typescript
function greet(name: string | null) {
  if (name) {
    console.log(name.toUpperCase()); // narrowed to string
  }
}
```

**Caveat**: truthiness narrowing eliminates `0` and `""`, which may be valid
values. Use explicit null checks when these matter:

```typescript
// Bug — filters out valid 0:
function display(count: number | null) {
  if (count) { show(count); } // 0 is falsy!
}

// Correct:
function display(count: number | null) {
  if (count != null) { show(count); } // 0 passes through
}
```

## Custom Type Guards (Type Predicates)

Define reusable narrowing logic with `is` return type:

```typescript
function isString(value: unknown): value is string {
  return typeof value === "string";
}

function process(input: unknown) {
  if (isString(input)) {
    console.log(input.toUpperCase()); // narrowed to string
  }
}
```

**Warning**: TypeScript does **not** verify the correctness of type guard logic.
A guard that returns `true` for the wrong type will silently cause bugs:

```typescript
// BUG — TypeScript trusts you, even though this is wrong:
function isString(value: unknown): value is string {
  return typeof value === "number"; // lies to the compiler
}
```

Keep type guards simple and obviously correct.

## Assertion Functions

Functions that throw if a condition is false, narrowing the type for subsequent
code:

```typescript
function assertDefined<T>(value: T | null | undefined, msg?: string): asserts value is T {
  if (value == null) {
    throw new Error(msg ?? "Expected value to be defined");
  }
}

function process(user: User | null) {
  assertDefined(user, "User must exist");
  // user is narrowed to User here — no optional chaining needed
  console.log(user.name);
}
```

Use assertion functions at system boundaries where invalid data should halt
execution. Prefer type guards when you want to handle both branches.

## The `satisfies` Operator

Checks that a value conforms to a type without widening its inferred type:

```typescript
type Route = { path: string; exact?: boolean };

// Type annotation widens:
const home: Route = { path: "/", exact: true };
home.exact; // type: boolean | undefined

// satisfies preserves:
const home = { path: "/", exact: true } satisfies Route;
home.exact; // type: true (literal)
```

Use `satisfies` when you need both type validation and precise inference — for
config objects, route maps, and theme definitions.

## Non-Null Assertion (`!`)

The `!` postfix operator asserts that a value is not `null` or `undefined`:

```typescript
const el = document.getElementById("app")!; // asserts non-null
```

**Avoid this.** It is a compile-time-only assertion with no runtime check. If the
value is actually `null`, you get a runtime error. Prefer explicit checks:

```typescript
const el = document.getElementById("app");
if (!el) throw new Error("Missing #app element");
// el is narrowed to HTMLElement
```

## Exhaustiveness Checking

Ensure all union members are handled using `never`:

```typescript
type Shape =
  | { kind: "circle"; radius: number }
  | { kind: "square"; side: number }
  | { kind: "triangle"; base: number; height: number };

function area(shape: Shape): number {
  switch (shape.kind) {
    case "circle": return Math.PI * shape.radius ** 2;
    case "square": return shape.side ** 2;
    case "triangle": return (shape.base * shape.height) / 2;
    default: {
      const _exhaustive: never = shape;
      throw new Error(`Unhandled shape: ${_exhaustive}`);
    }
  }
}
```

If a new variant is added to `Shape`, the `default` branch will produce a
compile error because the new variant is not assignable to `never`.

## Best Practices

- **Prefer narrowing over type assertions** — `as` bypasses the type checker
- **Use `satisfies` over `as`** when validating config and literal objects
- **Keep type guards simple** — complex guards are hard to verify as correct
- **Avoid `!` non-null assertions** — use explicit null checks or assertion functions
- **Always add exhaustiveness checks** on discriminated union switches
- **Narrow early** — check and narrow at the top of a function, then work with the safe type

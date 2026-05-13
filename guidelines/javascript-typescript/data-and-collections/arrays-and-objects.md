# Arrays and Objects

## Array Methods

### Transformation

```typescript
// map — transform each element:
const names = users.map((u) => u.name);

// flatMap — map + flatten one level:
const allTags = posts.flatMap((p) => p.tags);

// filter — keep matching elements:
const active = users.filter((u) => u.isActive);

// reduce — accumulate to a single value:
const total = items.reduce((sum, item) => sum + item.price, 0);
```

### Searching

```typescript
// find — first match (returns element or undefined):
const admin = users.find((u) => u.role === "admin");

// findIndex — first match index (-1 if not found):
const idx = users.findIndex((u) => u.id === targetId);

// some — at least one match (boolean):
const hasErrors = results.some((r) => r.status === "error");

// every — all match (boolean):
const allValid = inputs.every((i) => i.isValid);

// includes — value membership (uses ===):
const hasBanana = fruits.includes("banana");
```

### Grouping (ES2024+)

```typescript
// Object.groupBy — groups into a plain object:
const byRole = Object.groupBy(users, (u) => u.role);
// { admin: [...], user: [...], guest: [...] }

// Map.groupBy — groups into a Map (better for non-string keys):
const byAge = Map.groupBy(users, (u) => Math.floor(u.age / 10) * 10);
```

### Sorting

`Array.sort` mutates the original array. Use `toSorted` (ES2023+) for an
immutable version:

```typescript
// Mutates:
const sorted = [...items].sort((a, b) => a.price - b.price);

// Immutable (ES2023+):
const sorted = items.toSorted((a, b) => a.price - b.price);

// Other immutable array methods (ES2023+):
items.toReversed();               // immutable reverse
items.toSpliced(1, 2, newItem);   // immutable splice
items.with(0, newFirst);          // immutable index assignment
```

### `Array.from` for Construction

```typescript
// Create array from iterable:
const arr = Array.from(map.values());

// Create array with initializer:
const zeros = Array.from({ length: 10 }, () => 0);
const indices = Array.from({ length: 5 }, (_, i) => i);
```

## Object Patterns

### Spread for Shallow Copies and Merges

```typescript
// Shallow copy:
const copy = { ...original };

// Merge (later properties override):
const config = { ...defaults, ...userConfig };

// Add/override a property:
const updated = { ...user, name: "Bob" };

// Remove a property:
const { password, ...safeUser } = user;
```

### `Object.entries` / `Object.fromEntries`

Transform objects via entries:

```typescript
// Filter object keys:
const filtered = Object.fromEntries(
  Object.entries(config).filter(([key]) => key.startsWith("api_")),
);

// Map object values:
const doubled = Object.fromEntries(
  Object.entries(scores).map(([key, value]) => [key, value * 2]),
);
```

### `Object.keys` / `Object.values`

```typescript
const keys = Object.keys(user);     // string[]
const values = Object.values(user);  // unknown[] (in practice, the union of value types)
```

**TypeScript caveat**: `Object.keys` returns `string[]`, not `(keyof T)[]`,
because objects can have extra properties at runtime. Use a type assertion or
iterate with `for...in` + `hasOwn` if you need typed keys.

## Deep Cloning

### `structuredClone` (Recommended)

Built-in deep clone — no library needed:

```typescript
const clone = structuredClone(original);
```

Handles nested objects, arrays, Maps, Sets, Dates, RegExps, ArrayBuffers.

**Limitations**: does not clone functions, DOM nodes, or symbols. Does not
preserve prototype chains.

### When `structuredClone` Isn't Enough

For objects with class instances or functions, use a library or write custom
clone logic.

## Immutability

### Type-Level Immutability

Use `Readonly<T>` and `as const` to prevent mutations at compile time:

```typescript
// Readonly type:
function process(items: readonly string[]) {
  items.push("x"); // Error: push does not exist on readonly string[]
}

// as const for literal objects:
const config = { mode: "dark", version: 3 } as const;
config.mode = "light"; // Error: readonly
```

### Runtime Immutability

`Object.freeze` is **shallow** — nested objects are still mutable:

```typescript
const obj = Object.freeze({ a: 1, nested: { b: 2 } });
obj.a = 10;         // silently fails (or throws in strict mode)
obj.nested.b = 20;  // succeeds — nested is not frozen
```

For deep freezing, use `structuredClone` + `Object.freeze` or a library.
In practice, **prefer type-level immutability** (`Readonly<T>`, `as const`)
over runtime freezing.

## JSON Handling

### Safe Parsing

```typescript
// Parse with validation (recommended):
const data = JSON.parse(raw);
const user = UserSchema.parse(data); // validate with Zod

// Parse with reviver:
const data = JSON.parse(raw, (key, value) => {
  if (key === "createdAt") return new Date(value);
  return value;
});
```

### Serialization

```typescript
// With replacer for filtering:
JSON.stringify(user, ["name", "email"]); // only include listed keys

// With replacer function:
JSON.stringify(data, (key, value) => {
  if (key === "password") return undefined; // omit sensitive fields
  return value;
});
```

## Best Practices

- **Use immutable methods** (`toSorted`, `toReversed`, `toSpliced`) over mutating ones
- **Use `structuredClone`** for deep copies — no lodash needed
- **Prefer `Readonly<T>` and `as const`** over `Object.freeze`
- **Use `Object.groupBy`** for grouping — cleaner than manual reduce
- **Validate JSON** with Zod after parsing — never trust raw `JSON.parse`
- **Spread for shallow operations** — copies, merges, property removal

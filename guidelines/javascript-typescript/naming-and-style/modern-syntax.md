# Modern Syntax

## Variable Declarations

```typescript
// Always const by default:
const user = await fetchUser(id);
const items = [1, 2, 3];

// let only when reassignment is needed:
let retries = 0;
while (retries < MAX_RETRIES) { retries++; }

// Never var:
var name = "Alice"; // hoisted, function-scoped — source of bugs
```

## Destructuring

### Object Destructuring

```typescript
// Extract specific properties:
const { name, email, role } = user;

// With rename:
const { name: userName, email: userEmail } = user;

// With defaults:
const { retries = 3, timeout = 5000 } = config;

// In function parameters:
function greet({ name, greeting = "Hello" }: { name: string; greeting?: string }) {
  return `${greeting}, ${name}!`;
}
```

### Array Destructuring

```typescript
const [first, second, ...rest] = items;
const [, , third] = items; // skip elements

// Swap values:
[a, b] = [b, a];
```

### When Not to Destructure

Don't destructure when the property name carries useful context:

```typescript
// Destructuring loses context:
const { x, y, z } = getCoordinates();
// What are x, y, z? Could be anything.

// Keeping the object is clearer:
const position = getCoordinates();
drawAt(position.x, position.y);
```

## Optional Chaining (`?.`)

Safely access nested properties that might be null/undefined:

```typescript
// Before:
const city = user && user.address && user.address.city;

// After:
const city = user?.address?.city;

// With method calls:
const result = obj?.method?.();

// With array access:
const first = arr?.[0];
```

## Nullish Coalescing (`??`)

Provide defaults for `null` and `undefined` only — unlike `||` which also
triggers on `0`, `""`, and `false`:

```typescript
// Bug — || treats 0 and "" as falsy:
const port = config.port || 3000;     // if port is 0, uses 3000 (wrong!)
const name = user.name || "Unknown";  // if name is "", uses "Unknown" (wrong!)

// Correct — ?? only triggers on null/undefined:
const port = config.port ?? 3000;     // 0 is preserved
const name = user.name ?? "Unknown";  // "" is preserved
```

### Nullish Assignment (`??=`)

```typescript
// Assign only if null/undefined:
user.preferences ??= getDefaultPreferences();
```

## Template Literals

```typescript
// String interpolation:
const message = `Hello, ${name}! You have ${count} items.`;

// Multi-line strings:
const html = `
  <div class="card">
    <h2>${title}</h2>
    <p>${description}</p>
  </div>
`;

// Tagged templates for safe interpolation:
const query = sql`SELECT * FROM users WHERE id = ${id}`;
```

## Spread and Rest

### Spread (...)

```typescript
// Copy arrays:
const copy = [...original];

// Merge objects (shallow):
const merged = { ...defaults, ...overrides };

// Add to arrays:
const extended = [...items, newItem];
```

### Rest Parameters

```typescript
function log(message: string, ...args: unknown[]) {
  console.log(message, ...args);
}
```

## Logical Assignment

```typescript
// OR assignment — assign if falsy:
x ||= defaultValue;

// AND assignment — assign if truthy:
x &&= transform(x);

// Nullish assignment — assign if null/undefined:
x ??= computeDefault();
```

## Array/Object Methods

Prefer modern methods over manual loops:

```typescript
// Array.prototype methods:
const names = users.map((u) => u.name);
const admins = users.filter((u) => u.role === "admin");
const total = items.reduce((sum, item) => sum + item.price, 0);
const found = items.find((item) => item.id === targetId);
const hasAdmin = users.some((u) => u.role === "admin");
const allActive = users.every((u) => u.isActive);
const flat = nested.flatMap((group) => group.items);

// Object methods:
const entries = Object.entries(config);
const keys = Object.keys(config);
const fromEntries = Object.fromEntries(entries.filter(([k]) => k !== "secret"));

// Deep clone (no libraries needed):
const clone = structuredClone(original);
```

### `for...of` for Imperative Loops

Use `for...of` when you need `break`, `continue`, or `await`:

```typescript
for (const item of items) {
  if (item.skip) continue;
  await processItem(item);
  if (item.isLast) break;
}
```

**Avoid `for...in` for arrays** — it iterates over keys (strings), not values,
and includes prototype properties.

## Optional `catch` Binding

When you don't need the error variable:

```typescript
try {
  JSON.parse(input);
} catch {
  return null; // error variable omitted
}
```

## Best Practices

- **`const` by default** — `let` only for reassignment, never `var`
- **Use `??` not `||`** for defaults — preserves `0`, `""`, `false`
- **Use `?.` for optional access** — replaces verbose null checks
- **Destructure at the right level** — don't lose context for clarity
- **Use `structuredClone`** for deep copies — no library needed
- **Prefer methods over loops** — `map`, `filter`, `find`, `some`, `every`
- **Use `for...of` when you need control flow** — `break`, `continue`, `await`

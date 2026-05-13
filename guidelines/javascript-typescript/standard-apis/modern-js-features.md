# Modern JavaScript Features

## Intl API — Internationalization

The `Intl` API provides language-sensitive formatting without external libraries.
It replaces `moment.js`, `numeral.js`, and similar formatting libraries.

### Number Formatting

```typescript
// Currency:
new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" })
  .format(1234.56); // "$1,234.56"

// Compact notation:
new Intl.NumberFormat("en-US", { notation: "compact" })
  .format(1500000); // "1.5M"

// Percentage:
new Intl.NumberFormat("en-US", { style: "percent", minimumFractionDigits: 1 })
  .format(0.856); // "85.6%"

// Units:
new Intl.NumberFormat("en-US", { style: "unit", unit: "kilometer-per-hour" })
  .format(120); // "120 km/h"
```

### Date and Time Formatting

```typescript
const date = new Date("2025-03-15T14:30:00Z");

// Short date:
new Intl.DateTimeFormat("en-US").format(date); // "3/15/2025"

// Custom format:
new Intl.DateTimeFormat("en-US", {
  weekday: "long",
  year: "numeric",
  month: "long",
  day: "numeric",
}).format(date); // "Saturday, March 15, 2025"

// Time only:
new Intl.DateTimeFormat("en-US", {
  hour: "numeric",
  minute: "2-digit",
  timeZoneName: "short",
}).format(date); // "2:30 PM EST"
```

### Relative Time

```typescript
const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

rtf.format(-1, "day");    // "yesterday"
rtf.format(3, "hour");    // "in 3 hours"
rtf.format(-2, "week");   // "2 weeks ago"
```

### List Formatting

```typescript
new Intl.ListFormat("en", { style: "long", type: "conjunction" })
  .format(["Alice", "Bob", "Charlie"]); // "Alice, Bob, and Charlie"

new Intl.ListFormat("en", { style: "long", type: "disjunction" })
  .format(["red", "blue", "green"]); // "red, blue, or green"
```

### Collation (Sorting)

```typescript
const collator = new Intl.Collator("de", { sensitivity: "base" });
const sorted = ["Zürich", "Aachen", "Ölten"].sort(collator.compare);
// ["Aachen", "Ölten", "Zürich"]
```

## structuredClone

Built-in deep clone — replaces `JSON.parse(JSON.stringify())` and lodash
`cloneDeep`:

```typescript
const original = {
  name: "Alice",
  date: new Date(),
  items: [1, 2, 3],
  nested: { a: new Map([["key", "value"]]) },
};

const clone = structuredClone(original);
// Deep copy — modifying clone doesn't affect original
// Correctly clones Date, Map, Set, ArrayBuffer, etc.
```

**Limitations**: cannot clone functions, DOM nodes, or Error objects. Does not
preserve prototype chains of custom classes.

## crypto.randomUUID()

Generate RFC 4122 UUIDs without external libraries:

```typescript
const id = crypto.randomUUID(); // "3b241101-e2bb-4d7a-8613-e4b3f0c34752"
```

Available in browsers, Node.js 19+, and Deno. Replaces the `uuid` package for
most use cases.

## Decorators (Stage 3 / TypeScript 5.0+)

The TC39 Stage 3 decorator proposal is supported in TypeScript 5.0+ with
`"experimentalDecorators": false` (the new standard, not the legacy proposal):

```typescript
function log(originalMethod: Function, context: ClassMethodDecoratorContext) {
  return function (this: unknown, ...args: unknown[]) {
    console.log(`Calling ${String(context.name)} with`, args);
    return originalMethod.apply(this, args);
  };
}

class UserService {
  @log
  async getUser(id: string): Promise<User> {
    return db.users.findById(id);
  }
}
```

**Note**: TypeScript's legacy decorators (`experimentalDecorators: true`) have
different semantics. New code should use the standard proposal.

## Explicit Resource Management (`using`)

The `using` declaration (TC39 Stage 3) automatically cleans up resources:

```typescript
// With Symbol.dispose:
class DatabaseConnection {
  [Symbol.dispose]() {
    this.close(); // called automatically when scope exits
  }
}

{
  using db = new DatabaseConnection();
  await db.query("SELECT ...");
} // db[Symbol.dispose]() called here

// With Symbol.asyncDispose:
class AsyncResource {
  async [Symbol.asyncDispose]() {
    await this.cleanup();
  }
}

{
  await using resource = new AsyncResource();
  await resource.process();
} // await resource[Symbol.asyncDispose]() called here
```

Requires TypeScript 5.2+ with `"lib": ["esnext.disposable"]`.

## Regular Expression Features

### Named Groups

```typescript
const pattern = /(?<year>\d{4})-(?<month>\d{2})-(?<day>\d{2})/;
const match = "2025-03-15".match(pattern);
match?.groups?.year;  // "2025"
match?.groups?.month; // "03"
```

### `matchAll`

```typescript
const text = "price: $10, tax: $2, total: $12";
const matches = text.matchAll(/\$(\d+)/g);

for (const match of matches) {
  console.log(match[1]); // "10", "2", "12"
}
```

### Lookbehind

```typescript
// Positive lookbehind:
"$100".match(/(?<=\$)\d+/);  // ["100"]

// Negative lookbehind:
"€100".match(/(?<!\$)\d+/);  // ["100"]
```

## Best Practices

- **Use `Intl` for formatting** — dates, numbers, currencies, lists, relative time
- **Use `structuredClone`** for deep copies — no library needed
- **Use `crypto.randomUUID()`** — no `uuid` package needed
- **Use standard decorators** (`experimentalDecorators: false`) for new code
- **Use `using` for resource cleanup** — TypeScript 5.2+ with esnext.disposable
- **Use named regex groups** — more readable than positional captures
- **Check browser/Node.js compatibility** before using bleeding-edge features

# Unit Testing

## Framework: Vitest (Recommended)

Vitest is the standard for modern TypeScript projects. It provides native ESM
and TypeScript support with zero configuration, and a Jest-compatible API:

```typescript
// vitest.config.ts
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,             // optional: enables describe/it/expect without imports
    environment: "node",       // or "jsdom" for browser-like environment
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
    },
  },
});
```

### When to Use Jest

- React Native projects (Jest is mandatory)
- Large legacy codebases where migration cost is prohibitive
- Jest 30+ has improved ESM support, but Vitest remains faster

## Test Structure

### describe/it Blocks

```typescript
describe("UserService", () => {
  describe("createUser", () => {
    it("creates a user with valid input", async () => {
      const user = await createUser({ name: "Alice", email: "alice@example.com" });
      expect(user.id).toBeDefined();
      expect(user.name).toBe("Alice");
    });

    it("throws ValidationError for missing email", async () => {
      await expect(createUser({ name: "Alice" })).rejects.toThrow(ValidationError);
    });
  });
});
```

### Naming Convention

Use descriptive names that read as sentences:
- `it("returns null when user is not found")`
- `it("throws ValidationError for invalid email format")`
- `it("retries on transient network failure")`

Avoid vague names like `it("works")` or `it("should handle edge case")`.

## Assertions

### Equality

```typescript
// Reference equality — same object in memory:
expect(result).toBe(expected);

// Deep equality — same structure and values:
expect(result).toEqual(expected);

// Strict deep equality — also checks undefined properties and array sparseness:
expect(result).toStrictEqual(expected);
```

**Rule**: use `toStrictEqual` for objects and arrays, `toBe` for primitives.

### Common Matchers

```typescript
expect(value).toBeDefined();
expect(value).toBeNull();
expect(value).toBeTruthy();
expect(array).toHaveLength(3);
expect(array).toContain(item);
expect(object).toHaveProperty("key", "value");
expect(fn).toThrow(Error);
expect(fn).toThrow("specific message");
expect(string).toMatch(/pattern/);
```

### Async Assertions

```typescript
// Resolved value:
await expect(fetchUser("1")).resolves.toEqual({ id: "1", name: "Alice" });

// Rejected with specific error:
await expect(fetchUser("bad")).rejects.toThrow(NotFoundError);
```

## Test Setup and Teardown

```typescript
describe("DatabaseTests", () => {
  let db: Database;

  beforeEach(async () => {
    db = await createTestDatabase();
  });

  afterEach(async () => {
    await db.close();
  });

  it("inserts a record", async () => {
    await db.insert({ id: "1", name: "test" });
    expect(await db.get("1")).toEqual({ id: "1", name: "test" });
  });
});
```

**Prefer `beforeEach` over `beforeAll`** — ensures each test starts with a
clean state.

## Fake Timers

For testing code that depends on `setTimeout`, `setInterval`, or `Date.now()`:

```typescript
describe("debounce", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("calls the function after the delay", () => {
    const fn = vi.fn();
    const debounced = debounce(fn, 300);

    debounced();
    expect(fn).not.toHaveBeenCalled();

    vi.advanceTimersByTime(300);
    expect(fn).toHaveBeenCalledOnce();
  });
});
```

## Snapshot Testing

Use sparingly and only for stable outputs (serialized data, error messages):

```typescript
it("serializes user for API response", () => {
  const output = serializeUser(testUser);
  expect(output).toMatchInlineSnapshot(`
    {
      "email": "alice@example.com",
      "id": "1",
      "name": "Alice",
    }
  `);
});
```

**Avoid** snapshot testing for component HTML — it produces large, fragile
snapshots that get blindly updated. Use assertion-based component tests instead.

## Coverage

Configure meaningful coverage targets:

```typescript
// vitest.config.ts
test: {
  coverage: {
    thresholds: {
      statements: 80,
      branches: 75,
      functions: 80,
      lines: 80,
    },
  },
}
```

**Don't chase 100%** — diminishing returns past ~80%. Focus coverage on
business logic and edge cases, not boilerplate.

## Best Practices

- **One assertion per behavior** — test one thing per `it` block
- **Use `toStrictEqual`** for objects — catches subtle differences `toEqual` misses
- **Name tests as sentences** — "returns X when Y" format
- **Use fake timers** — never `await new Promise(r => setTimeout(r, 100))` in tests
- **Avoid snapshot testing for UI** — too fragile, leads to blind updates
- **Run with `--reporter=verbose`** in CI for clear failure output

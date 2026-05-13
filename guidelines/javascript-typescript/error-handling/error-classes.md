# Custom Error Classes

## Always Throw Error Instances

Never throw raw strings or plain objects — they lack stack traces and are
impossible to catch selectively:

```typescript
// Bad:
throw "Something went wrong";
throw { code: 404, message: "Not found" };

// Good:
throw new Error("Something went wrong");
throw new NotFoundError("User not found");
```

## Extending Error

Create domain-specific error classes by extending `Error`:

```typescript
class AppError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "AppError";
  }
}

class NotFoundError extends AppError {
  constructor(
    public readonly resource: string,
    public readonly id: string,
    options?: ErrorOptions,
  ) {
    super(`${resource} "${id}" not found`, options);
    this.name = "NotFoundError";
  }
}

class ValidationError extends AppError {
  constructor(
    public readonly field: string,
    public readonly reason: string,
    options?: ErrorOptions,
  ) {
    super(`Validation failed for ${field}: ${reason}`, options);
    this.name = "ValidationError";
  }
}
```

### Important: Set `this.name`

Always set `this.name` in the constructor. This makes `error.name` match your
class name in logs and error reports, rather than showing "Error".

## Error.cause (ES2022+)

Chain errors to preserve the original cause while adding context:

```typescript
async function loadUser(id: string): Promise<User> {
  try {
    const response = await fetch(`/api/users/${id}`);
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    return await response.json();
  } catch (error) {
    throw new NotFoundError("User", id, { cause: error });
  }
}
```

The original error is accessible via `error.cause`, preserving the full chain
for debugging. This replaces ad-hoc patterns like `error.originalError` or
`error.inner`.

### Accessing the Cause Chain

```typescript
try {
  await loadUser("123");
} catch (error) {
  console.error(error.message);        // "User "123" not found"
  console.error(error.cause?.message);  // "HTTP 404"
}
```

## Error Hierarchies

Keep hierarchies shallow — typically two levels:

```
Error
  └── AppError (base for all application errors)
        ├── NotFoundError
        ├── ValidationError
        ├── AuthenticationError
        └── ConflictError
```

Deep hierarchies add complexity without proportional benefit. Use properties
(like `code`, `statusCode`, `field`) on a flat set of error classes instead of
deep inheritance.

## Catching Errors in TypeScript

The `catch` clause receives `unknown` (with `useUnknownInCatchVariables`).
Always narrow explicitly:

```typescript
try {
  await riskyOperation();
} catch (error) {
  if (error instanceof NotFoundError) {
    // handle specifically
    return fallback(error.resource);
  }
  if (error instanceof Error) {
    // re-throw with context
    throw new AppError("Operation failed", { cause: error });
  }
  // unknown throw — wrap it
  throw new AppError("Unknown error", { cause: new Error(String(error)) });
}
```

## Structured Error Properties

Add structured fields for programmatic handling — don't encode context in the
message string:

```typescript
class HttpError extends AppError {
  constructor(
    public readonly statusCode: number,
    message: string,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "HttpError";
  }
}

// Programmatic handling:
if (error instanceof HttpError && error.statusCode === 429) {
  await delay(retryAfter);
  return retry();
}
```

## Compatibility

`Error.cause` requires ES2022+ runtime (Node.js 16.9+, all modern browsers).
In TypeScript, set `"target": "es2022"` or include `"lib": ["es2022"]` in
`tsconfig.json` to get the correct types for `ErrorOptions`.

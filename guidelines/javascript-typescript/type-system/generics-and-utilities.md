# Generics and Utility Types

## Generic Basics

Generics capture type relationships that `any` or `unknown` cannot express:

```typescript
// Without generics — loses type information:
function first(arr: unknown[]): unknown { return arr[0]; }

// With generics — preserves the element type:
function first<T>(arr: T[]): T | undefined { return arr[0]; }

const n = first([1, 2, 3]);    // type: number | undefined
const s = first(["a", "b"]);   // type: string | undefined
```

### Naming Conventions

- Single-letter (`T`, `U`, `K`, `V`) for simple generics
- Descriptive prefixed names for complex generics: `TSource`, `TResult`, `TKey`
- Always ask: "Would a concrete type work here?" — skip the generic if yes

## Constraints with `extends`

Restrict type parameters to ensure required properties exist:

```typescript
function getProperty<T, K extends keyof T>(obj: T, key: K): T[K] {
  return obj[key];
}

const user = { name: "Alice", age: 30 };
getProperty(user, "name");  // OK, returns string
getProperty(user, "email"); // Error: "email" is not a key of user
```

## Conditional Types

Conditional types follow the pattern `T extends U ? X : Y`:

```typescript
type IsString<T> = T extends string ? true : false;

type A = IsString<"hello">; // true
type B = IsString<42>;      // false
```

### Distributive Behavior

When applied to union types, conditional types distribute over each member:

```typescript
type ToArray<T> = T extends unknown ? T[] : never;
type Result = ToArray<string | number>; // string[] | number[]
```

Prevent distribution by wrapping in a tuple:

```typescript
type ToArrayNoDist<T> = [T] extends [unknown] ? T[] : never;
type Result = ToArrayNoDist<string | number>; // (string | number)[]
```

## The `infer` Keyword

Extracts types from within other types during conditional type evaluation:

```typescript
// Extract return type of a function:
type Return<T> = T extends (...args: any[]) => infer R ? R : never;

// Extract element type of an array:
type Elem<T> = T extends (infer U)[] ? U : T;

// Unwrap a Promise:
type Unwrap<T> = T extends Promise<infer U> ? U : T;

// Extract first argument:
type FirstArg<T> = T extends (first: infer F, ...rest: any[]) => any ? F : never;
```

## Mapped Types

Transform properties of an existing type:

```typescript
// Make all properties optional:
type MyPartial<T> = { [K in keyof T]?: T[K] };

// Make all properties readonly:
type MyReadonly<T> = { readonly [K in keyof T]: T[K] };

// Remap keys with `as`:
type Getters<T> = {
  [K in keyof T as `get${Capitalize<string & K>}`]: () => T[K];
};

interface User { name: string; age: number }
type UserGetters = Getters<User>;
// { getName: () => string; getAge: () => number }
```

## Built-in Utility Types

Use the standard utilities before writing custom type-level code:

| Utility | Purpose |
|---------|---------|
| `Partial<T>` | All properties optional |
| `Required<T>` | All properties required |
| `Readonly<T>` | All properties readonly |
| `Pick<T, K>` | Subset of properties |
| `Omit<T, K>` | All properties except K |
| `Record<K, V>` | Object type with keys K and values V |
| `NonNullable<T>` | Excludes `null` and `undefined` |
| `ReturnType<T>` | Return type of a function type |
| `Parameters<T>` | Tuple of parameter types |
| `Awaited<T>` | Unwraps Promise types recursively |
| `Extract<T, U>` | Members of T assignable to U |
| `Exclude<T, U>` | Members of T not assignable to U |

## Template Literal Types

Build string types from components:

```typescript
type HttpMethod = "GET" | "POST" | "PUT" | "DELETE";
type ApiRoute = `/api/${string}`;
type Endpoint = `${HttpMethod} ${ApiRoute}`;

// "GET /api/users" is valid; "PATCH /api/users" is not
```

## Best Practices

- **Start simple** — use a concrete type or `unknown` before reaching for generics
- **Constrain early** — use `extends` to restrict type parameters
- **Use built-in utilities** — don't rewrite `Partial`, `Pick`, `Omit`, etc.
- **Limit depth** — deeply nested conditional types hurt readability and compiler performance
- **Test complex types** — use `Expect<Equal<A, B>>` patterns or `@ts-expect-error` to verify
- **Document non-obvious types** — a comment explaining `infer` usage saves future readers

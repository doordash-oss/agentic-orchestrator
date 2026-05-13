# Naming Conventions

## Variable and Function Names

Use `camelCase` for variables, functions, and method names:

```typescript
const userName = "Alice";
const itemCount = 42;
function calculateTotal(items: Item[]): number { /* ... */ }
```

## Type and Interface Names

Use `PascalCase` for types, interfaces, classes, and enums:

```typescript
interface UserProfile { /* ... */ }
type HttpMethod = "GET" | "POST" | "PUT" | "DELETE";
class UserService { /* ... */ }
```

**Don't prefix interfaces with `I`** — this is a C#/.NET convention that doesn't
belong in TypeScript:

```typescript
// Avoid:
interface IUserService { /* ... */ }

// Prefer:
interface UserService { /* ... */ }
```

## Constants

Use `SCREAMING_SNAKE_CASE` for true constants — values known at compile time
that never change:

```typescript
const MAX_RETRY_COUNT = 3;
const API_BASE_URL = "https://api.example.com";
const DEFAULT_TIMEOUT_MS = 5000;
```

Use `camelCase` for derived or runtime values, even if declared with `const`:

```typescript
const currentUser = await fetchUser(id); // runtime value, not a constant
const config = loadConfig();
```

## Boolean Names

Name booleans as yes/no questions with `is`, `has`, `can`, `should`, or `was`:

```typescript
const isLoading = true;
const hasPermission = user.role === "admin";
const canEdit = hasPermission && !isLocked;
const shouldRetry = attempts < MAX_RETRY_COUNT;
```

Avoid negated names — they create double negatives in conditions:

```typescript
// Bad — double negative:
if (!isNotReady) { /* ... */ }

// Good:
if (isReady) { /* ... */ }
```

## Function Names

Use verb phrases that describe the action:

```typescript
function fetchUser(id: string): Promise<User> { /* ... */ }
function calculateTax(amount: number): number { /* ... */ }
function validateEmail(email: string): boolean { /* ... */ }
function formatCurrency(amount: number, currency: string): string { /* ... */ }
```

### Event Handlers

Prefix with `handle` or `on`:

```typescript
function handleClick(event: MouseEvent) { /* ... */ }
function handleSubmit(data: FormData) { /* ... */ }

// In React — props use on*, handlers use handle*:
<Button onClick={handleClick} />
```

## File Names

Match the primary export:

| Export | File Name |
|--------|-----------|
| `class UserService` | `UserService.ts` |
| `function parseConfig()` | `parseConfig.ts` |
| React component `Dashboard` | `Dashboard.tsx` |
| Types only | `types.ts` |
| Constants only | `constants.ts` |
| Utilities | `utils.ts` (but prefer descriptive names like `date-utils.ts`) |

### Use kebab-case for Non-Component Files

Many teams use `kebab-case` for non-component files and `PascalCase` for React
component files:

```
src/
  components/
    UserProfile.tsx    # PascalCase — React component
    Button.tsx
  utils/
    date-utils.ts      # kebab-case — utility module
    parse-config.ts
  types/
    user.ts            # kebab-case — type definitions
```

## Generic Type Parameters

Use descriptive names for complex generics, single letters for simple ones:

```typescript
// Simple — single letter is fine:
function first<T>(arr: T[]): T | undefined { /* ... */ }
function map<T, U>(arr: T[], fn: (item: T) => U): U[] { /* ... */ }

// Complex — use descriptive names:
function merge<TSource, TOverride>(
  source: TSource,
  override: TOverride,
): TSource & TOverride { /* ... */ }
```

## Acronyms

Treat acronyms as words in `camelCase` and `PascalCase`:

```typescript
// Correct:
const httpClient = createClient();
const apiUrl = "https://api.example.com";
class HttpError extends Error { /* ... */ }
type JsonResponse = { /* ... */ };

// Avoid — all-caps in camelCase looks wrong:
const HTTPClient = createClient();
const APIUrl = "https://api.example.com";
```

## Avoid Noise Words

Don't add redundant suffixes or prefixes:

```typescript
// Avoid:
const userData = fetchUser(id);
const userInfo = getUserInfo(id);
const userObj = new User();

// Prefer:
const user = fetchUser(id);
const profile = getUserProfile(id);
```

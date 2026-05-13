# Mocking Patterns

## Principle: Mock at Boundaries

Mock external dependencies (APIs, databases, file systems), not internal modules.
Tests should exercise real application logic and only fake what crosses system
boundaries.

```typescript
// Bad — mocking internal module:
vi.mock("./calculateTotal"); // now you're not testing the actual logic

// Good — mocking the external API call:
// Use MSW to intercept HTTP requests at the network level
```

## Module Mocking (vi.mock / jest.mock)

When you must mock a module (e.g., a third-party SDK):

```typescript
import { vi, describe, it, expect } from "vitest";
import { sendEmail } from "./email-service";

vi.mock("@sendgrid/mail", () => ({
  setApiKey: vi.fn(),
  send: vi.fn().mockResolvedValue([{ statusCode: 202 }]),
}));

it("sends a welcome email", async () => {
  await sendEmail({ to: "alice@example.com", template: "welcome" });

  const { send } = await import("@sendgrid/mail");
  expect(send).toHaveBeenCalledWith(
    expect.objectContaining({ to: "alice@example.com" }),
  );
});
```

### Reset Between Tests

```typescript
afterEach(() => {
  vi.restoreAllMocks(); // restores original implementations
});
```

**Always restore mocks** — leaked mock state is the most common source of
flaky tests.

## Spies

Observe calls without replacing implementation:

```typescript
it("logs analytics event on signup", async () => {
  const spy = vi.spyOn(analytics, "track");

  await signUp({ name: "Alice", email: "alice@example.com" });

  expect(spy).toHaveBeenCalledWith("user_signed_up", {
    email: "alice@example.com",
  });
});
```

## MSW — Mock Service Worker

MSW intercepts HTTP requests at the network level. It works with any HTTP
client (fetch, axios, etc.) without modifying application code:

### Setup

```typescript
// mocks/handlers.ts
import { http, HttpResponse } from "msw";

export const handlers = [
  http.get("/api/users/:id", ({ params }) => {
    return HttpResponse.json({
      id: params.id,
      name: "Alice",
      email: "alice@example.com",
    });
  }),

  http.post("/api/users", async ({ request }) => {
    const body = await request.json();
    return HttpResponse.json({ id: "new-id", ...body }, { status: 201 });
  }),
];
```

```typescript
// mocks/server.ts
import { setupServer } from "msw/node";
import { handlers } from "./handlers";

export const server = setupServer(...handlers);
```

```typescript
// vitest.setup.ts (or test setup file)
import { server } from "./mocks/server";

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
```

### Override Handlers Per Test

```typescript
it("shows error state when API returns 500", async () => {
  server.use(
    http.get("/api/users/:id", () => {
      return new HttpResponse(null, { status: 500 });
    }),
  );

  const result = await fetchUser("1");
  expect(result).toBeNull();
});
```

### Why MSW Over vi.mock(fetch)

- Tests the actual fetch/HTTP client code path
- Reusable across unit tests, integration tests, and Storybook
- Supports REST, GraphQL, and WebSocket APIs
- No application code modifications needed
- Closer to production behavior

## Dependency Injection for Testability

Design functions to accept dependencies as parameters:

```typescript
// Hard to test — uses global fetch:
async function loadUsers() {
  const response = await fetch("/api/users");
  return response.json();
}

// Easy to test — dependency is injectable:
async function loadUsers(
  fetcher: typeof fetch = fetch,
): Promise<User[]> {
  const response = await fetcher("/api/users");
  return response.json();
}

// In tests:
it("returns parsed users", async () => {
  const mockFetch = vi.fn().mockResolvedValue({
    json: () => Promise.resolve([{ id: "1", name: "Alice" }]),
  });
  const users = await loadUsers(mockFetch);
  expect(users).toHaveLength(1);
});
```

For complex dependency graphs, use constructor injection with interfaces:

```typescript
interface UserRepository {
  findById(id: string): Promise<User | null>;
}

class UserService {
  constructor(private repo: UserRepository) {}

  async getUser(id: string): Promise<User> {
    const user = await this.repo.findById(id);
    if (!user) throw new NotFoundError("User", id);
    return user;
  }
}

// In tests — pass a simple object that satisfies the interface:
const mockRepo: UserRepository = {
  findById: vi.fn().mockResolvedValue({ id: "1", name: "Alice" }),
};
const service = new UserService(mockRepo);
```

## Best Practices

- **Mock at the boundary** — APIs, databases, third-party SDKs, not internal logic
- **Use MSW for HTTP** — more realistic than mocking fetch
- **Always restore mocks** — `vi.restoreAllMocks()` in `afterEach`
- **Use `onUnhandledRequest: "error"`** in MSW — catches unhandled API calls
- **Prefer dependency injection** over module mocking for complex services
- **Don't over-mock** — if you're mocking more than the external boundary, your test isn't testing much

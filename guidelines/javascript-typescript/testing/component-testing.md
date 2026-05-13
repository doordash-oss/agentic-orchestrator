# Component Testing

## Testing Library

Testing Library is the standard for testing UI components. Its core principle:
**test the way users interact with your application**, not internal implementation.

### Query Priority

Use queries in this order of preference:

| Priority | Query | Use When |
|----------|-------|----------|
| 1 | `getByRole` | Almost everything — buttons, inputs, headings, links |
| 2 | `getByLabelText` | Form inputs (how users find form fields) |
| 3 | `getByPlaceholderText` | Inputs when no label exists |
| 4 | `getByText` | Non-interactive elements (paragraphs, spans) |
| 5 | `getByTestId` | Last resort — when no accessible query works |

```typescript
// Preferred — accessible query:
screen.getByRole("button", { name: /submit/i });
screen.getByRole("textbox", { name: /email/i });
screen.getByRole("heading", { name: /dashboard/i });

// Avoid — implementation-coupled:
screen.getByTestId("submit-button");
container.querySelector(".btn-primary");
```

### Use `screen` Over Destructuring

```typescript
// Preferred:
render(<LoginForm />);
const input = screen.getByRole("textbox", { name: /email/i });

// Avoid — needs updating when queries change:
const { getByRole } = render(<LoginForm />);
```

## user-event Over fireEvent

`@testing-library/user-event` simulates real user interactions (keyboard events,
focus, blur) rather than just dispatching DOM events:

```typescript
import userEvent from "@testing-library/user-event";

it("submits the form with user input", async () => {
  const user = userEvent.setup();
  render(<LoginForm onSubmit={handleSubmit} />);

  await user.type(screen.getByRole("textbox", { name: /email/i }), "alice@example.com");
  await user.type(screen.getByLabelText(/password/i), "secret123");
  await user.click(screen.getByRole("button", { name: /sign in/i }));

  expect(handleSubmit).toHaveBeenCalledWith({
    email: "alice@example.com",
    password: "secret123",
  });
});
```

### Why Not fireEvent

```typescript
// fireEvent — only dispatches one event:
fireEvent.change(input, { target: { value: "hello" } });

// user-event — dispatches full sequence (focus, keyDown, input, keyUp per char):
await user.type(input, "hello");
```

`fireEvent` skips intermediate events that real users trigger. This can miss
bugs in event handlers that rely on the full event sequence.

## Async Queries

### `findBy` for Async Content

Use `findBy` (not `waitFor` + `getBy`) to wait for elements to appear:

```typescript
// Preferred:
const message = await screen.findByText(/welcome/i);

// Avoid — unnecessarily verbose:
await waitFor(() => {
  expect(screen.getByText(/welcome/i)).toBeInTheDocument();
});
```

### `queryBy` for Absence

Use `queryBy` only to assert that an element does **not** exist:

```typescript
expect(screen.queryByText(/error/i)).not.toBeInTheDocument();
```

Don't use `queryBy` for elements you expect to find — `getBy` throws a clear
error if the element is missing.

## Assertions with jest-dom

```typescript
import "@testing-library/jest-dom";

expect(button).toBeEnabled();
expect(button).toBeDisabled();
expect(element).toBeVisible();
expect(element).toHaveTextContent("Hello");
expect(input).toHaveValue("alice@example.com");
expect(element).toHaveAttribute("aria-expanded", "true");
expect(element).toHaveClass("active");
```

## Common Patterns

### Testing Loading States

```typescript
it("shows loading spinner then content", async () => {
  render(<UserProfile userId="1" />);

  // Loading state:
  expect(screen.getByRole("progressbar")).toBeInTheDocument();

  // Loaded state:
  expect(await screen.findByText("Alice")).toBeInTheDocument();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
});
```

### Testing Error States

```typescript
it("shows error message when fetch fails", async () => {
  server.use(
    http.get("/api/user", () => new HttpResponse(null, { status: 500 })),
  );

  render(<UserProfile userId="1" />);

  expect(await screen.findByRole("alert")).toHaveTextContent(/failed to load/i);
});
```

### Testing Forms

```typescript
it("shows validation error for invalid email", async () => {
  const user = userEvent.setup();
  render(<SignUpForm />);

  await user.type(screen.getByRole("textbox", { name: /email/i }), "not-an-email");
  await user.click(screen.getByRole("button", { name: /submit/i }));

  expect(await screen.findByText(/invalid email/i)).toBeInTheDocument();
});
```

## waitFor — Use Correctly

Only put assertions inside `waitFor`, not side effects:

```typescript
// Correct:
await user.click(button);
await waitFor(() => {
  expect(screen.getByText("Updated")).toBeInTheDocument();
});

// Wrong — side effect inside waitFor:
await waitFor(() => {
  user.click(button); // will be called multiple times!
  expect(screen.getByText("Updated")).toBeInTheDocument();
});
```

## Best Practices

- **Use `getByRole` for everything** — it validates accessibility as a side effect
- **Use `user-event`, not `fireEvent`** — closer to real user behavior
- **Test user-visible behavior** — not component state or implementation details
- **Use `findBy` for async** — cleaner than `waitFor` + `getBy`
- **Use `queryBy` only for non-existence** — `getBy` gives better error messages
- **Don't test styling** — test behavior and content, not CSS classes
- **Keep component tests focused** — one behavior per test

# End-to-End Testing

## Framework: Playwright (Recommended)

Playwright is the standard for modern E2E testing — fast, reliable, and
supports Chromium, Firefox, and WebKit:

```typescript
// playwright.config.ts
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30000,
  retries: process.env.CI ? 2 : 0,
  use: {
    baseURL: "http://localhost:3000",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  webServer: {
    command: "npm run dev",
    port: 3000,
    reuseExistingServer: !process.env.CI,
  },
});
```

## Test User-Visible Behavior

E2E tests should validate what users see and do, not DOM structure:

```typescript
import { test, expect } from "@playwright/test";

test("user can sign up and see dashboard", async ({ page }) => {
  await page.goto("/signup");

  await page.getByLabel("Email").fill("alice@example.com");
  await page.getByLabel("Password").fill("secure-password-123");
  await page.getByRole("button", { name: "Create account" }).click();

  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByText("Welcome, alice@example.com")).toBeVisible();
});
```

## Test Isolation

Each test runs in its own browser context — no shared state:

```typescript
test("logged-in user sees profile", async ({ page }) => {
  // Each test starts fresh — no cookies, no localStorage from other tests
  await page.goto("/login");
  await page.getByLabel("Email").fill("alice@example.com");
  await page.getByLabel("Password").fill("password");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByText("Alice")).toBeVisible();
});
```

### Reuse Authentication State

For tests that all need a logged-in user, use storage state:

```typescript
// auth.setup.ts — runs once before all tests
import { test as setup } from "@playwright/test";

setup("authenticate", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Email").fill("alice@example.com");
  await page.getByLabel("Password").fill("password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.waitForURL("/dashboard");

  await page.context().storageState({ path: ".auth/user.json" });
});
```

```typescript
// playwright.config.ts
projects: [
  { name: "setup", testMatch: /.*\.setup\.ts/ },
  {
    name: "tests",
    dependencies: ["setup"],
    use: { storageState: ".auth/user.json" },
  },
],
```

## Resilient Locators

Use accessible locators — they survive UI refactors and validate accessibility:

```typescript
// Preferred — accessible locators:
page.getByRole("button", { name: /submit/i });
page.getByLabel("Email");
page.getByText("Welcome back");
page.getByPlaceholder("Search...");

// Avoid — brittle CSS selectors:
page.locator(".btn-primary > span:nth-child(2)");
page.locator("#submit-btn");
```

Use `data-testid` only as a last resort:

```typescript
page.getByTestId("complex-chart"); // when no accessible query works
```

## Auto-Waiting

Playwright auto-waits for elements to be actionable. Never use hard-coded waits:

```typescript
// Bad — arbitrary wait:
await page.waitForTimeout(3000);

// Good — wait for specific condition:
await expect(page.getByText("Loaded")).toBeVisible();
await page.waitForResponse("**/api/users");
```

## Page Object Model

Encapsulate page interactions in reusable classes:

```typescript
class LoginPage {
  constructor(private page: Page) {}

  async login(email: string, password: string) {
    await this.page.getByLabel("Email").fill(email);
    await this.page.getByLabel("Password").fill(password);
    await this.page.getByRole("button", { name: "Sign in" }).click();
  }

  async expectError(message: string) {
    await expect(this.page.getByRole("alert")).toHaveText(message);
  }
}
```

### Keep Page Objects Focused

Don't create "god objects" with 50 methods. Split by user intent or feature:

```typescript
// Good — focused objects:
class CartPage { /* add, remove, getTotal */ }
class CheckoutPage { /* fillShipping, fillPayment, submit */ }

// Bad — everything in one class:
class AppPage { /* login, addToCart, checkout, editProfile, ... */ }
```

## Testing API Interactions

### Wait for Network

```typescript
test("submits form and shows confirmation", async ({ page }) => {
  await page.goto("/contact");

  await page.getByLabel("Message").fill("Hello!");

  const responsePromise = page.waitForResponse("**/api/contact");
  await page.getByRole("button", { name: "Send" }).click();
  const response = await responsePromise;

  expect(response.status()).toBe(201);
  await expect(page.getByText("Message sent")).toBeVisible();
});
```

## CI Integration

Run E2E tests on every PR in CI:

```yaml
# .github/workflows/e2e.yml
- name: Run Playwright tests
  run: npx playwright test
  env:
    CI: true

- name: Upload test artifacts
  if: failure()
  uses: actions/upload-artifact@v4
  with:
    name: playwright-report
    path: playwright-report/
```

### Start Small

Don't write 200 E2E tests at once. Start with 5-10 critical user flows and
expand gradually. E2E tests are expensive to maintain — cover happy paths and
the most critical error scenarios.

## Best Practices

- **Test user flows, not units** — E2E tests cover journeys, not individual functions
- **Use accessible locators** — `getByRole`, `getByLabel`, `getByText`
- **Never use hard-coded waits** — rely on auto-waiting and explicit conditions
- **Isolate tests** — no shared state between tests
- **Reuse auth state** — don't log in for every test
- **Run in CI** — with retries and artifact collection on failure
- **Keep the suite small** — 5-20 critical flows, not hundreds of tests
- **Use trace on retry** — `trace: "on-first-retry"` for debugging flakes

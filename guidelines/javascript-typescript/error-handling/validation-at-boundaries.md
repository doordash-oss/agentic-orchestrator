# Validation at Boundaries

TypeScript type-checks at compile time only. When data enters your system from
external sources — API responses, user input, environment variables, config
files — it is untyped at runtime. Validate at these boundaries.

## Why Runtime Validation Matters

```typescript
// TypeScript trusts your type annotation — but the API might return anything:
const user: User = await response.json(); // type-safe at compile time, UNSAFE at runtime

// If the API returns { name: 123 } instead of { name: "Alice" },
// you'll get a runtime crash far from where the data entered.
```

## Zod — Schema Validation

Zod is the standard library for TypeScript runtime validation. Define a schema
once, get both validation and type inference:

```typescript
import { z } from "zod";

const UserSchema = z.object({
  id: z.string().uuid(),
  name: z.string().min(1),
  email: z.string().email(),
  role: z.enum(["admin", "user", "guest"]),
  createdAt: z.coerce.date(),
});

// Infer the TypeScript type from the schema — don't define it separately:
type User = z.infer<typeof UserSchema>;
```

### `safeParse` vs `parse`

```typescript
// parse() throws on failure — use in trusted internal code:
const user = UserSchema.parse(data);

// safeParse() returns a discriminated union — use at boundaries:
const result = UserSchema.safeParse(data);
if (!result.success) {
  logger.warn("Invalid user data", { errors: result.error.flatten() });
  return res.status(400).json({ errors: result.error.flatten().fieldErrors });
}
const user = result.data; // type-safe User
```

**Prefer `safeParse`** at system boundaries for graceful error handling.

### Advanced Patterns

```typescript
// Refinements for custom validation:
const PasswordSchema = z.string()
  .min(8)
  .refine((s) => /[A-Z]/.test(s), "Must contain uppercase letter")
  .refine((s) => /[0-9]/.test(s), "Must contain a number");

// Transforms for normalization:
const EmailSchema = z.string().email().transform((s) => s.toLowerCase().trim());

// Default values:
const ConfigSchema = z.object({
  port: z.number().default(3000),
  host: z.string().default("localhost"),
  debug: z.boolean().default(false),
});

// Composing schemas:
const CreateUserSchema = UserSchema.omit({ id: true, createdAt: true });
const UpdateUserSchema = UserSchema.partial().required({ id: true });
```

## Where to Validate

### API Responses

```typescript
async function fetchUser(id: string): Promise<User> {
  const response = await fetch(`/api/users/${id}`);
  if (!response.ok) throw new HttpError(response.status);
  const data = await response.json();
  return UserSchema.parse(data); // validate before use
}
```

### Request Handlers (Express / Fastify)

```typescript
app.post("/users", (req, res) => {
  const result = CreateUserSchema.safeParse(req.body);
  if (!result.success) {
    return res.status(400).json({ errors: result.error.flatten().fieldErrors });
  }
  const user = await createUser(result.data);
  return res.status(201).json(user);
});
```

### Environment Variables

```typescript
const EnvSchema = z.object({
  DATABASE_URL: z.string().url(),
  API_KEY: z.string().min(1),
  PORT: z.coerce.number().int().positive().default(3000),
  NODE_ENV: z.enum(["development", "production", "test"]).default("development"),
});

// Validate at startup — fail fast if config is wrong:
export const env = EnvSchema.parse(process.env);
```

### Form Input (with react-hook-form)

```typescript
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";

const form = useForm<z.infer<typeof CreateUserSchema>>({
  resolver: zodResolver(CreateUserSchema),
});
```

## Alternatives to Zod

| Library | Trade-off |
|---------|-----------|
| **Valibot** | Smaller bundle (~1KB vs Zod's ~13KB), tree-shakeable API |
| **ArkType** | Closest to native TS syntax, strong inference |
| **superstruct** | Lightweight, good for simple schemas |
| **io-ts** | FP-oriented, integrates with fp-ts ecosystem |

Choose based on bundle size constraints and team familiarity. Zod is the
safe default for most projects.

## Best Practices

- **Define schemas once, infer types** — `z.infer<typeof Schema>` avoids duplication
- **Validate at the boundary, trust internally** — once data passes validation, use the inferred type freely
- **Use `safeParse` at API boundaries** — graceful error responses, not crashes
- **Use `parse` for internal assertions** — when invalid data means a programming error
- **Fail fast on startup** — validate env vars and config before the server starts
- **Coerce where appropriate** — `z.coerce.number()` handles string-to-number from query params

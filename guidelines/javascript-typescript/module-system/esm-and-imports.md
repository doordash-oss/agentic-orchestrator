# ESM and Import Patterns

## ESM vs CommonJS

ESM is the JavaScript standard. CJS is Node.js legacy:

```typescript
// ESM (standard):
import { readFile } from "node:fs/promises";
export function parse(data: string) { /* ... */ }

// CJS (legacy):
const { readFile } = require("fs/promises");
module.exports = { parse };
```

### Key Differences

| Feature | ESM | CJS |
|---------|-----|-----|
| Syntax | `import`/`export` | `require()`/`module.exports` |
| Evaluation | Static, at parse time | Dynamic, at runtime |
| Tree-shaking | Yes (static analysis) | No |
| Top-level await | Yes | No |
| Circular deps | Live bindings (works) | Snapshot (often broken) |
| `__dirname` | Not available (use `import.meta.url`) | Available |

### Migrating `__dirname` and `__filename`

```typescript
// CJS:
const configPath = path.join(__dirname, "config.json");

// ESM:
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const configPath = join(__dirname, "config.json");
```

## Named vs Default Exports

Prefer named exports:

```typescript
// Prefer — named exports:
export function createUser(data: UserInput): User { /* ... */ }
export function deleteUser(id: string): void { /* ... */ }

// Avoid — default exports:
export default class UserService { /* ... */ }
```

**Why**: named exports provide better auto-import support, refactoring safety
(renaming works across the codebase), and prevent naming inconsistencies across
files that import the same module.

**Exception**: framework conventions may require default exports (e.g., Next.js
page components, Storybook stories).

## Import Type

Use `import type` for type-only imports. With `verbatimModuleSyntax`, this is
enforced:

```typescript
// Type-only import — erased at runtime:
import type { User, Config } from "./types";

// Value import — present at runtime:
import { fetchUser } from "./api";

// Mixed — import value, type-import the type:
import { fetchUser, type UserInput } from "./api";
```

## Barrel Files

A barrel file (`index.ts`) re-exports from multiple modules:

```typescript
// components/index.ts
export { Button } from "./Button";
export { Input } from "./Input";
export { Modal } from "./Modal";
```

### When Barrels Hurt

Barrel files can **prevent tree-shaking** and cause large bundles when:

- They re-export from modules with side effects
- Consumers import one item but the bundler loads everything
- Deep barrel chains create import waterfalls

### When Barrels Help

- Public API surface of a library (one clean entry point)
- Small sets of closely related components

**Rule**: use barrels at package boundaries, avoid them within internal
application code.

## Dynamic Imports

Load modules on demand for code splitting:

```typescript
// Static import — loaded immediately:
import { HeavyChart } from "./HeavyChart";

// Dynamic import — loaded when needed:
const { HeavyChart } = await import("./HeavyChart");
```

### React Lazy Loading

```typescript
import { lazy, Suspense } from "react";

const HeavyChart = lazy(() => import("./HeavyChart"));

function Dashboard() {
  return (
    <Suspense fallback={<Spinner />}>
      <HeavyChart />
    </Suspense>
  );
}
```

## Import Organization

Keep imports ordered consistently. Most formatters (Biome, ESLint) can enforce
this automatically:

```typescript
// 1. Node.js built-ins (with node: prefix)
import { readFile } from "node:fs/promises";

// 2. External packages
import { z } from "zod";
import express from "express";

// 3. Internal aliases (@/ or ~/)
import { db } from "@/lib/database";

// 4. Relative imports
import { UserSchema } from "./schemas";
import type { User } from "./types";
```

## Side-Effect Imports

Some imports are only for their side effects (CSS, polyfills):

```typescript
import "./global.css";           // side effect: registers styles
import "core-js/stable";         // side effect: polyfills globals
```

Mark modules as side-effect-free for tree-shaking in `package.json`:

```json
{
  "sideEffects": false
}
```

Or list specific side-effect files:

```json
{
  "sideEffects": ["*.css", "./src/polyfills.ts"]
}
```

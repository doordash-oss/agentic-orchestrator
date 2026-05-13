# Strict TypeScript Configuration

## The `strict` Flag

`strict: true` is a meta-flag that enables multiple individual checks at once:
`strictNullChecks`, `strictFunctionTypes`, `strictBindCallApply`,
`strictPropertyInitialization`, `noImplicitAny`, `noImplicitThis`,
`useUnknownInCatchVariables`, and `alwaysStrict`.

**Always enable it.** Do not selectively disable individual strict checks.

## Recommended Base Configuration

```jsonc
{
  "compilerOptions": {
    // Emit & module
    "target": "es2022",
    "module": "ESNext",
    "moduleDetection": "force",
    "isolatedModules": true,
    "verbatimModuleSyntax": true,
    "skipLibCheck": true,
    "esModuleInterop": true,

    // Strictness
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noImplicitOverride": true,
    "exactOptionalPropertyTypes": true,
    "forceConsistentCasingInFileNames": true
  }
}
```

Extend this with environment-specific settings (see below).

## Key Flags Beyond `strict: true`

### `noUncheckedIndexedAccess`

Without this flag, array/object indexed access assumes the element exists:

```typescript
// Without noUncheckedIndexedAccess:
const arr = [1, 2, 3];
const val = arr[10]; // type: number (BUG — it's undefined at runtime)

// With noUncheckedIndexedAccess:
const val = arr[10]; // type: number | undefined — forces a check
```

**Always enable.** This prevents the most common class of runtime errors in
TypeScript.

### `exactOptionalPropertyTypes`

Distinguishes between a property being `undefined` vs missing entirely:

```typescript
interface Theme {
  color?: string;
}

// Without exactOptionalPropertyTypes:
const t: Theme = { color: undefined }; // OK (but semantically wrong)

// With exactOptionalPropertyTypes:
const t: Theme = { color: undefined }; // Error — use `delete t.color` instead
```

### `noImplicitOverride`

Requires the `override` keyword when overriding class methods, preventing
accidental overrides and catching renamed parent methods at compile time:

```typescript
class Base {
  greet() { return "hello"; }
}

class Sub extends Base {
  override greet() { return "hi"; } // Required — catches typos
}
```

### `verbatimModuleSyntax`

Enforces `import type` for type-only imports. Prevents TypeScript from
rewriting imports that don't exist at runtime:

```typescript
// Correct:
import type { User } from "./types";
import { fetchUser } from "./api";

// Error with verbatimModuleSyntax — User is only a type:
import { User, fetchUser } from "./api";
```

## Environment-Specific Extensions

### For Applications (Vite, Next.js, Node.js)

```jsonc
{
  "compilerOptions": {
    "moduleResolution": "bundler",  // or "node16" for Node.js without bundler
    "noEmit": true                  // bundler handles emit
  }
}
```

### For Libraries

```jsonc
{
  "compilerOptions": {
    "moduleResolution": "node16",
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true,
    "outDir": "dist"
  }
}
```

### For Monorepo Projects

```jsonc
{
  "compilerOptions": {
    "composite": true,
    "declarationMap": true
  },
  "references": [
    { "path": "../shared" }
  ]
}
```

## Performance Flags

For large codebases, these flags significantly speed up type checking:

- **`incremental: true`** — caches compilation info for faster rebuilds
- **`skipLibCheck: true`** — skips type checking `.d.ts` files (safe in practice)
- **`isolatedModules: true`** — enables per-file transpilation (required by most bundlers)

## Flags to Avoid in Linting

These flags are often better handled by ESLint/Biome than by the compiler:

- `noUnusedLocals` / `noUnusedParameters` — too noisy during development
- `noImplicitReturns` — debatable value, often creates unnecessary `undefined` returns
- `noFallthroughCasesInSwitch` — useful but better as a lint rule with auto-fix

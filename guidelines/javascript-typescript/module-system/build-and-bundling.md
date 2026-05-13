# Build and Bundling

## Applications vs Libraries

The build strategy differs fundamentally:

| | Applications | Libraries |
|-|-------------|-----------|
| **Goal** | Optimized bundle for browsers/Node.js | Distributable package for consumers |
| **Bundler** | Vite, webpack, Next.js built-in | tsup, unbuild, Rollup |
| **Output** | Bundled, minified, code-split | Separate modules, preserving imports |
| **Type declarations** | Not needed | Required (`.d.ts`) |
| **Tree-shaking** | Consumer-side (you are the consumer) | Author-side (make it tree-shakeable) |

## Application Bundling with Vite

Vite is the standard for modern web applications:

```typescript
// vite.config.ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    target: "es2022",
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ["react", "react-dom"],
        },
      },
    },
  },
});
```

### Code Splitting

Vite automatically code-splits on dynamic imports:

```typescript
// Each dynamic import creates a separate chunk:
const AdminPanel = lazy(() => import("./AdminPanel"));
const Analytics = lazy(() => import("./Analytics"));
```

## Library Bundling with tsup

tsup (powered by esbuild) is the fastest way to build TypeScript libraries:

```typescript
// tsup.config.ts
import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  splitting: false,
  treeshake: true,
});
```

### For Monorepo Packages

```typescript
export default defineConfig({
  entry: ["src/index.ts", "src/utils.ts"],
  format: ["esm"],
  dts: true,
  external: ["react", "react-dom"], // peer deps
});
```

## Tree-Shaking

Tree-shaking removes unused exports from the final bundle. It only works with
ESM and requires proper configuration:

### Author Side (Libraries)

1. **Use ESM** — CJS cannot be tree-shaken
2. **Use named exports** — default exports are harder to analyze
3. **Set `sideEffects: false`** in package.json
4. **Avoid module-level side effects** — initialization code in module scope prevents tree-shaking

```typescript
// Bad — side effect at module level:
const cache = initializeCache(); // runs on import
export function getItem(key: string) { return cache.get(key); }

// Good — lazy initialization:
let cache: Cache | null = null;
function getCache() { return cache ??= initializeCache(); }
export function getItem(key: string) { return getCache().get(key); }
```

### Consumer Side (Applications)

1. **Import only what you need**: `import { map } from "lodash-es"` not `import _ from "lodash"`
2. **Use tree-shakeable alternatives**: `date-fns` instead of `moment`, `lodash-es` instead of `lodash`
3. **Analyze your bundle**: use `rollup-plugin-visualizer` or `webpack-bundle-analyzer`

## Source Maps

Always generate source maps in all environments:

```typescript
// Vite:
build: { sourcemap: true }

// tsup:
sourcemap: true

// tsc:
"sourceMap": true
```

Source maps are essential for debugging production errors. Serve them separately
(not inline) to avoid increasing bundle size for end users.

## Build Performance

- **esbuild** — fastest transpiler, used by Vite and tsup
- **SWC** — used by Next.js, very fast Rust-based compiler
- **tsc** — slowest, but authoritative for type checking; use only for type checking and declaration generation, not for bundling
- **Use `incremental: true`** in tsconfig for faster type-check iterations

## Bundle Size Budget

Track and enforce bundle size limits in CI:

```json
// package.json
{
  "scripts": {
    "size": "size-limit",
    "size:check": "size-limit --limit 200kB"
  }
}
```

**Targets**: keep initial JS payload under 200KB (gzipped) for critical pages.
Use `bundlephobia.com` to evaluate dependency costs before adding them.

## Best Practices

- **Use Vite for applications** — fast dev server, optimized production builds
- **Use tsup for libraries** — zero-config, fast, generates `.d.ts`
- **Always output ESM** — add CJS only if your consumers need it
- **Enable source maps everywhere** — essential for production debugging
- **Measure gzipped size** — it's what users actually download
- **Track bundle size in CI** — prevent regressions before they ship

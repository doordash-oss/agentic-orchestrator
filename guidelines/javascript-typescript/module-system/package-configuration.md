# Package Configuration

## `type: "module"` in package.json

Sets the default module format for `.js` files in the package:

```json
{
  "type": "module"
}
```

- With `"type": "module"`: `.js` files are ESM; use `.cjs` for CommonJS files
- Without it (default): `.js` files are CJS; use `.mjs` for ESM files
- **Recommendation**: always set `"type": "module"` for new projects

## The `exports` Field

Controls what consumers can import from your package. Replaces the top-level
`main` field for modern Node.js (v12.7+):

```json
{
  "name": "my-lib",
  "exports": {
    ".": {
      "types": "./dist/index.d.ts",
      "import": "./dist/index.js",
      "require": "./dist/index.cjs"
    },
    "./utils": {
      "types": "./dist/utils.d.ts",
      "import": "./dist/utils.js",
      "require": "./dist/utils.cjs"
    }
  }
}
```

### Key Rules

1. **`types` must come first** — TypeScript ignores it if placed after `default`
2. **`default` goes last** — acts as the fallback condition
3. **Order matters** — Node.js uses the first matching condition
4. **Encapsulation** — any path not listed in `exports` is inaccessible to consumers

### Subpath Patterns

Expose multiple files with a pattern:

```json
{
  "exports": {
    ".": "./dist/index.js",
    "./components/*": "./dist/components/*.js"
  }
}
```

## Dual Package Publishing (ESM + CJS)

For libraries that need to support both module systems:

```json
{
  "type": "module",
  "exports": {
    ".": {
      "types": "./dist/index.d.ts",
      "import": "./dist/index.js",
      "require": "./dist/index.cjs"
    }
  }
}
```

### The Dual Package Hazard

If both ESM and CJS versions of your package can be loaded in the same process,
they create two separate module instances. This breaks `instanceof` checks and
singleton patterns. Mitigations:

- Use a stateless API where possible
- Export a shared state wrapper that both versions reference
- Document that consumers should use one format consistently

## TypeScript Declaration Files

Match `.d.ts` extensions to the module format they describe:

- `.d.ts` — ambiguous (interpreted based on package `type`)
- `.d.cts` — describes a CJS module
- `.d.mts` — describes an ESM module

For dual packages, generate separate declaration files:

```json
{
  "exports": {
    ".": {
      "types": {
        "import": "./dist/index.d.mts",
        "require": "./dist/index.d.cts"
      },
      "import": "./dist/index.mjs",
      "require": "./dist/index.cjs"
    }
  }
}
```

### Consumer Configuration

Consumers must use `moduleResolution: "node16"`, `"nodenext"`, or `"bundler"`
in their `tsconfig.json` to resolve conditional exports correctly.

## Essential package.json Fields

```json
{
  "name": "my-package",
  "version": "1.0.0",
  "type": "module",
  "exports": { ".": "./dist/index.js" },
  "types": "./dist/index.d.ts",
  "files": ["dist"],
  "engines": { "node": ">=18" },
  "sideEffects": false,
  "scripts": {
    "build": "tsup src/index.ts --format esm,cjs --dts",
    "test": "vitest",
    "lint": "biome check .",
    "prepublishOnly": "npm run build"
  }
}
```

### `files` Field

Explicitly list files to publish. This acts as an allowlist — only listed
files are included in the npm package:

```json
{
  "files": ["dist", "README.md", "LICENSE"]
}
```

### `engines` Field

Declare minimum Node.js version:

```json
{
  "engines": { "node": ">=18" }
}
```

## Best Practices

- **Always set `"type": "module"`** for new packages
- **Use the `exports` field** — it replaces `main` and provides encapsulation
- **Put `types` first** in conditional exports for TypeScript compatibility
- **Use `files` to control what's published** — avoid shipping source, tests, configs
- **Set `engines`** to document minimum Node.js version
- **Mark `sideEffects: false`** for tree-shakeable packages
- **Test your package with `npm pack`** before publishing to verify contents

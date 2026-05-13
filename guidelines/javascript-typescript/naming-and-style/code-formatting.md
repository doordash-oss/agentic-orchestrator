# Code Formatting

## Choose One Tool, Enforce It

Formatting should be automated and non-negotiable. Pick one approach:

| Option | Speed | Setup Effort | TypeScript Linting |
|--------|-------|-------------|-------------------|
| **Biome** | Very fast (Rust-based) | Low — single tool | Good (~85% of typescript-eslint) |
| **ESLint + Prettier** | Moderate | Higher — two tools | Full (typescript-eslint) |
| **ESLint (with style rules)** | Moderate | Moderate | Full |

### Biome (Recommended for New Projects)

Single tool for both formatting and linting, 15-50x faster than ESLint + Prettier:

```json
// biome.json
{
  "$schema": "https://biomejs.dev/schemas/2.0/schema.json",
  "formatter": {
    "indentStyle": "space",
    "indentWidth": 2,
    "lineWidth": 100
  },
  "linter": {
    "rules": {
      "recommended": true
    }
  }
}
```

```json
// package.json
{
  "scripts": {
    "format": "biome format --write .",
    "lint": "biome check .",
    "lint:fix": "biome check --fix ."
  }
}
```

### Migrating to Biome

Biome provides automated migration from ESLint and Prettier:

```bash
npx biome migrate eslint --write    # migrate ESLint config
npx biome migrate prettier --write  # migrate Prettier config
```

### ESLint Flat Config (v9+)

ESLint v9 uses flat config (`eslint.config.js`) by default:

```typescript
// eslint.config.js
import eslint from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  eslint.configs.recommended,
  ...tseslint.configs.strict,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", {
        argsIgnorePattern: "^_",
      }],
    },
  },
  {
    ignores: ["dist/", "node_modules/"],
  },
);
```

### Prettier (If Not Using Biome)

```json
// .prettierrc
{
  "semi": true,
  "singleQuote": false,
  "tabWidth": 2,
  "trailingComma": "all",
  "printWidth": 100
}
```

When using ESLint + Prettier together, add `eslint-config-prettier` to disable
conflicting ESLint rules:

```typescript
// eslint.config.js
import prettier from "eslint-config-prettier";

export default [
  // ... other configs
  prettier, // must be last to override conflicting rules
];
```

## Editor Integration

### Format on Save

Configure your editor to format on save. In VS Code:

```json
// .vscode/settings.json
{
  "editor.formatOnSave": true,
  "editor.defaultFormatter": "biomejs.biome",
  "editor.codeActionsOnSave": {
    "source.organizeImports.biome": "explicit"
  }
}
```

## CI Enforcement

Run formatting checks in CI to catch unformatted code:

```yaml
# GitHub Actions:
- name: Check formatting
  run: npx biome check --no-errors-on-unmatched .

# Or with Prettier:
- name: Check formatting
  run: npx prettier --check .
```

**Never auto-fix in CI** — CI should only check. Developers fix locally.

## Import Organization

Automate import sorting to avoid bikeshedding:

```typescript
// Biome sorts imports automatically with biome check --fix
// ESLint: use eslint-plugin-import or typescript-eslint rules

// Recommended order:
// 1. Node.js built-ins
// 2. External packages
// 3. Internal aliases
// 4. Relative imports
// 5. Type-only imports last within each group
```

## Best Practices

- **Automate everything** — no manual formatting, ever
- **Use one tool** — Biome alone or ESLint + Prettier, not a mix of everything
- **Format on save** — configure editors, not just CI
- **Check in CI** — fail the build on formatting violations
- **Don't debate style** — let the tool decide, focus on logic
- **Share editor config** — commit `.vscode/settings.json` or equivalent

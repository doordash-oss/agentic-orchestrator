# Type Checker Configuration

## mypy

### Basic Configuration

```toml
# pyproject.toml
[tool.mypy]
python_version = "3.11"
strict = true
warn_return_any = true
warn_unused_configs = true
disallow_untyped_defs = true
disallow_any_generics = true
check_untyped_defs = true
no_implicit_optional = true
warn_redundant_casts = true
warn_unused_ignores = true
```

### Per-Module Overrides

Relax strictness for third-party libraries or legacy code:

```toml
[[tool.mypy.overrides]]
module = "tests.*"
disallow_untyped_defs = false

[[tool.mypy.overrides]]
module = "third_party_lib.*"
ignore_missing_imports = true
```

### Common Flags

| Flag | Effect |
|------|--------|
| `--strict` | Enables all optional strictness checks |
| `--disallow-untyped-defs` | Functions must have type annotations |
| `--no-implicit-optional` | `def f(x: int = None)` is an error (must be `int \| None`) |
| `--warn-return-any` | Warn when returning `Any` from typed function |
| `--ignore-missing-imports` | Suppress errors for untyped third-party libs |

## pyright

### Basic Configuration

```toml
# pyproject.toml
[tool.pyright]
pythonVersion = "3.11"
typeCheckingMode = "strict"
reportMissingTypeStubs = "warning"
reportUnusedImport = "error"
reportUnusedVariable = "warning"
```

### Strictness Levels

| Mode | Description |
|------|-------------|
| `off` | No type checking |
| `basic` | Catches obvious errors (undefined variables, wrong arg types) |
| `standard` | Stricter; flags missing return types, implicit `Any` |
| `strict` | Full strictness; all functions must be annotated |

### pyright vs mypy

- **pyright** — faster, better IDE integration (powers Pylance in VS Code),
  stricter out of the box
- **mypy** — more mature, wider plugin ecosystem, better for complex codebases

Both are valid choices. Pick one and configure it to `strict` mode.

## Inline Type Ignores

```python
# Suppress a specific error with a code
x = some_untyped_function()  # type: ignore[no-untyped-call]

# pyright-specific
x = some_untyped_function()  # pyright: ignore[reportGeneralClassIssues]

# Anti-pattern: bare type: ignore with no code
x = some_function()  # type: ignore    # which error? why?
```

Always specify the error code — bare `type: ignore` suppresses all errors on
that line and can hide real bugs.

## Typing Third-Party Libraries

```python
# Install type stubs when available
# pip install types-requests types-PyYAML

# For libraries without stubs, create a py.typed marker or stub file
# mypackage/py.typed  (empty file — signals package is typed)

# Or create a stub file
# mypackage/external.pyi
def external_function(x: int) -> str: ...
```

## Gradual Typing Strategy

1. Start with `basic` mode and annotate new code
2. Enable `--disallow-untyped-defs` for new modules
3. Gradually annotate existing modules
4. Move to `strict` when coverage is high enough
5. Use per-module overrides to exclude legacy code

Never add `# type: ignore` to make the type checker pass without understanding
the underlying issue.

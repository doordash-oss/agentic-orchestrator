# Project Layout

## src Layout (Recommended for Libraries)

```
mypackage/
├── pyproject.toml
├── src/
│   └── mypackage/
│       ├── __init__.py
│       ├── core.py
│       └── utils.py
├── tests/
│   ├── conftest.py
│   └── test_core.py
└── README.md
```

Benefits:
- Tests import the installed package, not the source directory
- Catches packaging bugs (missing files, bad imports) during development
- No accidental imports from the project root

## Flat Layout (Acceptable for Applications)

```
myapp/
├── pyproject.toml
├── myapp/
│   ├── __init__.py
│   ├── main.py
│   └── config.py
├── tests/
│   └── test_main.py
└── README.md
```

Simpler, but tests can accidentally import local source instead of the
installed package.

## `__init__.py` Patterns

### Minimal (Preferred)

```python
# mypackage/__init__.py
"""Public API for mypackage."""

from .core import process, transform
from .models import User, Config

__all__ = ["process", "transform", "User", "Config"]
```

### Lazy Loading (For Large Packages)

```python
# mypackage/__init__.py
def __getattr__(name: str):
    if name == "HeavyModule":
        from .heavy import HeavyModule
        return HeavyModule
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
```

## Entry Points (CLI Tools)

```python
# mypackage/cli.py
def main() -> None:
    """CLI entry point."""
    import sys
    args = parse_args(sys.argv[1:])
    run(args)
```

```toml
# pyproject.toml
[project.scripts]
mycommand = "mypackage.cli:main"
```

After installation, `mycommand` is available on the PATH.

### `__main__.py`

```python
# mypackage/__main__.py
from .cli import main
main()
```

Enables `python -m mypackage` — useful during development.

## Internal vs Public Modules

```
mypackage/
├── __init__.py          # public API re-exports
├── _internal.py         # underscore prefix = internal
├── core.py              # public module
└── helpers/
    ├── __init__.py
    └── _parsing.py      # internal submodule
```

Use `__all__` in `__init__.py` to define the explicit public API. Anything
not in `__all__` is considered internal.

## Namespace Packages (Monorepo)

For splitting a package across multiple directories:

```
# No __init__.py — implicit namespace package (PEP 420)
libs/
├── mynamespace-core/
│   └── mynamespace/
│       └── core.py
└── mynamespace-utils/
    └── mynamespace/
        └── utils.py
```

Both `mynamespace.core` and `mynamespace.utils` work after installing both
sub-packages.

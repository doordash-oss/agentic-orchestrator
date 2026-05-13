# pyproject.toml

## Minimal Configuration

```toml
[project]
name = "mypackage"
version = "0.1.0"
description = "A short description"
requires-python = ">=3.11"
dependencies = [
    "httpx>=0.24",
    "pydantic>=2.0",
]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"
```

## Build Backends

| Backend | Use Case |
|---------|----------|
| `hatchling` | Modern default — simple, fast, good defaults |
| `setuptools` | Legacy projects, C extensions |
| `flit_core` | Pure Python packages, minimal config |
| `pdm-backend` | PEP 517, monorepo support |

## Optional Dependencies

```toml
[project.optional-dependencies]
dev = [
    "pytest>=7.0",
    "pytest-cov>=4.0",
    "ruff>=0.4",
    "mypy>=1.0",
]
test = [
    "pytest>=7.0",
    "pytest-asyncio>=0.21",
    "hypothesis>=6.0",
]
docs = [
    "sphinx>=7.0",
    "sphinx-rtd-theme>=2.0",
]
```

Install with: `uv pip install -e ".[dev]"` or `pip install -e ".[dev,test]"`

## Tool Configuration

Centralize all tool config in `pyproject.toml`:

```toml
# Ruff
[tool.ruff]
line-length = 88
target-version = "py311"

[tool.ruff.lint]
select = ["E", "F", "I", "UP", "B", "SIM"]

# mypy
[tool.mypy]
python_version = "3.11"
strict = true

# pytest
[tool.pytest.ini_options]
testpaths = ["tests"]
addopts = "-ra -q --strict-markers"
markers = [
    "slow: marks tests as slow",
    "integration: requires external services",
]

# Coverage
[tool.coverage.run]
branch = true
source = ["mypackage"]
```

## Entry Points

```toml
# CLI commands
[project.scripts]
mycommand = "mypackage.cli:main"

# GUI entry points
[project.gui-scripts]
myapp = "mypackage.gui:main"

# Plugin entry points
[project.entry-points."myapp.plugins"]
csv = "mypackage.plugins.csv:CSVPlugin"
json = "mypackage.plugins.json:JSONPlugin"
```

## Versioning

### Static (in pyproject.toml)

```toml
[project]
version = "1.2.3"
```

### Dynamic (from source code)

```toml
[project]
dynamic = ["version"]

[tool.hatch.version]
path = "src/mypackage/__init__.py"
```

```python
# src/mypackage/__init__.py
__version__ = "1.2.3"
```

## Project Metadata

```toml
[project]
name = "mypackage"
version = "0.1.0"
description = "Short description"
readme = "README.md"
license = {text = "MIT"}
requires-python = ">=3.11"
authors = [
    {name = "Your Name", email = "you@example.com"},
]
classifiers = [
    "Programming Language :: Python :: 3",
    "License :: OSI Approved :: MIT License",
]
```

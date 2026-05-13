# Dependency Management

## Virtual Environments

Never install packages globally. Always use a virtual environment:

```bash
# Using uv (recommended — 10-100x faster than pip)
uv venv
source .venv/bin/activate

# Using stdlib venv
python -m venv .venv
source .venv/bin/activate

# Deactivate
deactivate
```

## uv (Modern Tool)

uv replaces pip, pip-tools, and virtualenv:

```bash
# Create venv and install
uv venv
uv pip install -e ".[dev]"

# Install from requirements
uv pip install -r requirements.txt

# Compile lock file (like pip-compile)
uv pip compile pyproject.toml -o requirements.lock

# Sync environment to lock file
uv pip sync requirements.lock
```

## Pinning Strategy

### Applications — Pin Exact Versions

```toml
# pyproject.toml — broad constraints
dependencies = [
    "httpx>=0.24",
    "pydantic>=2.0,<3",
]
```

```
# requirements.lock — exact pins (generated)
httpx==0.27.0
pydantic==2.6.1
certifi==2024.2.2
# ... all transitive dependencies
```

Lock files ensure reproducible builds across environments.

### Libraries — Use Ranges

```toml
dependencies = [
    "httpx>=0.24",           # minimum version
    "pydantic>=2.0,<3",     # compatible range
]
```

Libraries should specify the widest range that works to avoid version conflicts
for consumers.

## `.python-version`

Pin the Python version for the project:

```
3.11.8
```

Tools like `pyenv` and `uv` respect this file.

## Development Workflow

```bash
# Clone and setup
git clone <repo>
cd <repo>
uv venv
uv pip install -e ".[dev]"

# Add a dependency
# 1. Add to pyproject.toml
# 2. Re-compile lock file
uv pip compile pyproject.toml -o requirements.lock
# 3. Sync
uv pip sync requirements.lock

# Upgrade a dependency
uv pip compile --upgrade-package httpx pyproject.toml -o requirements.lock
```

## Anti-Patterns

```bash
# Never install globally
pip install requests          # pollutes system Python

# Never use sudo pip
sudo pip install requests     # can break OS packages

# Don't commit .venv/
# Add to .gitignore:
# .venv/

# Don't use requirements.txt as the source of truth
# Use pyproject.toml — requirements.txt is a lock file output
```

## Monorepo with uv Workspaces

```toml
# Root pyproject.toml
[tool.uv.workspace]
members = ["packages/*"]

# packages/core/pyproject.toml
[project]
name = "myapp-core"
dependencies = []

# packages/api/pyproject.toml
[project]
name = "myapp-api"
dependencies = ["myapp-core"]
```

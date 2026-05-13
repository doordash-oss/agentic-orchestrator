# Formatting and Imports

## Ruff Configuration

Use ruff for both linting and formatting — it replaces black, isort, and flake8:

```toml
# pyproject.toml
[tool.ruff]
line-length = 88
indent-width = 4
target-version = "py311"

[tool.ruff.lint]
select = ["E", "F", "I", "UP", "B", "SIM"]
ignore = ["E501"]                    # let formatter handle line length

[tool.ruff.lint.per-file-ignores]
"tests/**" = ["S101"]               # allow assert in tests
"**/__init__.py" = ["F401"]         # allow re-exports without local use

[tool.ruff.lint.isort]
known-first-party = ["mypackage"]

[tool.ruff.format]
quote-style = "double"
indent-style = "space"
skip-magic-trailing-comma = false    # respect intentional trailing commas
```

## Import Ordering (PEP 8)

Four groups, separated by blank lines:

```python
# 1. Future imports
from __future__ import annotations

# 2. Standard library
import os
import sys
from pathlib import Path

# 3. Third-party
import httpx
import numpy as np

# 4. Local application
from mypackage.data_utils import DataProcessor
```

Prefer absolute imports. Use relative imports only within a package:
```python
from mypackage.utils import helper   # absolute — preferred
from .utils import helper            # relative — acceptable inside package
```

Never use wildcard imports (`from os.path import *`).

## Avoiding Circular Imports

```python
# Strategy 1: TYPE_CHECKING guard (zero runtime cost)
from typing import TYPE_CHECKING
if TYPE_CHECKING:
    from mypackage.models import UserModel

def process(user: "UserModel") -> None: ...

# Strategy 2: from __future__ import annotations
from __future__ import annotations
from mypackage.models import UserModel  # evaluated lazily

# Strategy 3: local import (last resort)
def get_users():
    from mypackage.models import UserModel
    return UserModel.all()
```

## Trailing Commas

Use trailing commas to force multi-line formatting and cleaner diffs:

```python
# Trailing comma — ruff keeps each argument on its own line
result = some_function(
    arg_one,
    arg_two,
    arg_three,
)

# No trailing comma — ruff may collapse to one line if it fits
result = some_function(arg_one, arg_two, arg_three)
```

## Blank Lines

```python
# Two blank lines around top-level definitions
CONSTANT = 42


def top_level_function():
    pass


class MyClass:

    # One blank line between methods
    def method_one(self):
        pass

    def method_two(self):
        pass
```

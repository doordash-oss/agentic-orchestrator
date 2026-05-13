# Advanced Testing

## Property-Based Testing with Hypothesis

Instead of specific examples, define properties that must always hold:

```python
from hypothesis import given, strategies as st

@given(st.lists(st.integers()))
def test_sort_preserves_length(xs):
    assert len(sorted(xs)) == len(xs)

@given(st.lists(st.integers(), min_size=1))
def test_sort_is_ordered(xs):
    result = sorted(xs)
    assert all(a <= b for a, b in zip(result, result[1:]))
```

### Custom Strategies

```python
from hypothesis import given, strategies as st
from dataclasses import dataclass

@dataclass
class User:
    name: str
    age: int

users = st.builds(
    User,
    name=st.text(min_size=1, max_size=50),
    age=st.integers(min_value=0, max_value=150),
)

@given(users)
def test_user_serialization_roundtrip(user):
    data = serialize(user)
    restored = deserialize(data)
    assert restored == user
```

### `@example` for Specific Cases

```python
from hypothesis import given, example, strategies as st

@given(st.text())
@example("")           # always test empty string
@example("a" * 10000)  # always test very long string
def test_process_string(s):
    result = process(s)
    assert isinstance(result, str)
```

## Async Testing

```python
# pip install pytest-asyncio
import pytest

@pytest.mark.asyncio
async def test_async_fetch():
    result = await fetch_data("https://example.com")
    assert len(result) > 0

# Async fixtures
@pytest.fixture
async def async_client():
    client = AsyncHTTPClient()
    yield client
    await client.close()

@pytest.mark.asyncio
async def test_with_client(async_client):
    resp = await async_client.get("/health")
    assert resp.status == 200
```

Configure in `pyproject.toml`:
```toml
[tool.pytest.ini_options]
asyncio_mode = "auto"    # auto-detect async tests (no @pytest.mark.asyncio needed)
```

## Snapshot Testing

Verify complex outputs haven't changed unexpectedly:

```python
# pip install syrupy
def test_api_response(snapshot):
    result = generate_report(data)
    assert result == snapshot

# First run: creates snapshot file
# Subsequent runs: compares against saved snapshot
# Update snapshots: pytest --snapshot-update
```

## Coverage

```toml
# pyproject.toml
[tool.pytest.ini_options]
addopts = "--cov=mypackage --cov-report=term-missing --cov-fail-under=80"

[tool.coverage.run]
branch = true
source = ["mypackage"]

[tool.coverage.report]
exclude_lines = [
    "pragma: no cover",
    "if TYPE_CHECKING:",
    "if __name__ == .__main__.",
]
```

Run: `pytest --cov=mypackage`

Focus on meaningful coverage — don't chase 100%. Aim for:
- 80%+ line coverage for application code
- 90%+ for libraries
- Branch coverage is more valuable than line coverage

## Test Categories

Separate tests by speed and scope:

```toml
# pyproject.toml
[tool.pytest.ini_options]
markers = [
    "slow: marks tests as slow (deselect with '-m \"not slow\"')",
    "integration: requires external services",
    "e2e: end-to-end tests",
]
```

```python
@pytest.mark.integration
def test_database_connection():
    ...

@pytest.mark.slow
def test_full_pipeline():
    ...
```

Run fast tests only: `pytest -m "not slow and not integration"`

# pytest Fundamentals

## Test Discovery

pytest finds tests automatically by convention:

- Files named `test_*.py` or `*_test.py`
- Functions prefixed with `test_`
- Classes prefixed with `Test` (no `__init__`)

```
tests/
    conftest.py          # shared fixtures
    test_auth.py
    test_users.py
    integration/
        conftest.py      # integration-specific fixtures
        test_database.py
```

## Assertions

pytest uses plain `assert` — no special assertion methods needed:

```python
def test_addition():
    assert 1 + 1 == 2

def test_string_contains():
    assert "hello" in "hello world"

def test_dict_subset():
    result = {"name": "Alice", "age": 30, "role": "admin"}
    assert result["name"] == "Alice"
    assert "role" in result
```

pytest rewrites assertions to show detailed failure messages automatically.

## Testing Exceptions

```python
import pytest

def test_invalid_age():
    with pytest.raises(ValueError, match="must be non-negative"):
        parse_age("-5")

def test_key_error():
    with pytest.raises(KeyError) as exc_info:
        {}["missing"]
    assert exc_info.value.args[0] == "missing"
```

## Testing Warnings

```python
def test_deprecation_warning():
    with pytest.warns(DeprecationWarning, match="retries.*deprecated"):
        fetch_data(url, retries=3)
```

## Built-in Fixtures

```python
def test_file_output(tmp_path):
    """tmp_path provides a unique temporary directory per test."""
    output = tmp_path / "result.txt"
    output.write_text("hello")
    assert output.read_text() == "hello"

def test_stdout_capture(capsys):
    """capsys captures stdout and stderr."""
    print("hello")
    captured = capsys.readouterr()
    assert captured.out == "hello\n"

def test_log_capture(caplog):
    """caplog captures log records."""
    import logging
    logger = logging.getLogger(__name__)
    with caplog.at_level(logging.WARNING):
        logger.warning("disk full")
    assert "disk full" in caplog.text
```

## Test Organization

```python
# Group related tests with classes (no __init__ needed)
class TestUserCreation:
    def test_valid_user(self):
        user = create_user("Alice", 30)
        assert user.name == "Alice"

    def test_missing_name(self):
        with pytest.raises(ValueError):
            create_user("", 30)

# Or use simple functions with descriptive names
def test_create_user_with_valid_data():
    user = create_user("Alice", 30)
    assert user.name == "Alice"

def test_create_user_rejects_empty_name():
    with pytest.raises(ValueError):
        create_user("", 30)
```

## Marks

```python
@pytest.mark.slow
def test_full_pipeline():
    """Run with: pytest -m slow"""
    ...

@pytest.mark.skip(reason="pending API migration")
def test_legacy_endpoint(): ...

@pytest.mark.skipif(sys.platform == "win32", reason="Unix only")
def test_file_permissions(): ...

@pytest.mark.xfail(reason="known bug #123")
def test_edge_case(): ...
```

# Mocking

## `monkeypatch` (pytest built-in)

Preferred for simple cases — no imports, automatic cleanup:

```python
def test_env_variable(monkeypatch):
    monkeypatch.setenv("API_KEY", "test-key-123")
    assert os.environ["API_KEY"] == "test-key-123"

def test_override_attribute(monkeypatch):
    monkeypatch.setattr("myapp.config.DEBUG", True)
    assert myapp.config.DEBUG is True

def test_override_function(monkeypatch):
    monkeypatch.setattr("myapp.client.fetch", lambda url: b"mocked")
    result = myapp.client.fetch("https://example.com")
    assert result == b"mocked"

def test_delete_attribute(monkeypatch):
    monkeypatch.delattr("myapp.config.OPTIONAL_SETTING")
```

## `unittest.mock.patch`

For more complex scenarios — argument verification, call tracking:

```python
from unittest.mock import patch, MagicMock

def test_sends_email():
    with patch("myapp.email.send") as mock_send:
        process_order(order)
        mock_send.assert_called_once_with(
            to="alice@example.com",
            subject="Order confirmed",
        )

# As a decorator
@patch("myapp.db.get_connection")
def test_database_query(mock_conn):
    mock_conn.return_value.execute.return_value = [{"id": 1}]
    result = fetch_users()
    assert result == [{"id": 1}]
```

## `MagicMock` vs `Mock`

```python
from unittest.mock import Mock, MagicMock

# Mock — basic mock, raises AttributeError for undefined dunder methods
mock = Mock()
mock.method()            # ok — auto-creates attribute
len(mock)                # TypeError — __len__ not defined

# MagicMock — pre-defines common dunder methods
magic = MagicMock()
len(magic)               # returns 0
magic[0]                 # returns another MagicMock
```

Use `Mock` by default; use `MagicMock` when the code under test calls dunder
methods on the mocked object.

## `spec` for Type Safety

```python
from unittest.mock import create_autospec

# create_autospec makes the mock match the real object's interface
mock_client = create_autospec(HTTPClient)
mock_client.get("url")         # ok
mock_client.nonexistent()      # AttributeError!
mock_client.get(1, 2, 3)       # TypeError — wrong signature
```

Always use `spec` or `create_autospec` — mocks without spec can't catch
typos in method names or wrong argument counts.

## When to Mock vs Use Fakes

```python
# MOCK: when you need to verify interactions
def test_logs_error():
    with patch("myapp.logger.error") as mock_log:
        process_invalid_data()
        mock_log.assert_called_once()

# FAKE: when you need realistic behavior
class FakeDatabase:
    def __init__(self):
        self._data: dict[int, dict] = {}

    def insert(self, id: int, data: dict) -> None:
        self._data[id] = data

    def get(self, id: int) -> dict | None:
        return self._data.get(id)

def test_user_creation(fake_db):
    service = UserService(db=fake_db)
    service.create_user("Alice")
    assert fake_db.get(1)["name"] == "Alice"
```

Guidelines:
- **Mock at boundaries** (HTTP, databases, external services)
- **Don't mock internal code** — test the real implementation
- **Prefer fakes over mocks** when the mock setup is complex
- **Use `create_autospec`** to catch interface mismatches

## Anti-Patterns

```python
# Over-mocking — test is testing the mocks, not the code
@patch("myapp.parse")
@patch("myapp.validate")
@patch("myapp.transform")
@patch("myapp.save")
def test_process(mock_save, mock_transform, mock_validate, mock_parse):
    # This test verifies call order, not actual behavior
    ...

# Mocking what you own — mock the boundary, not internals
# Instead of mocking your own UserService, mock the DB it calls
```

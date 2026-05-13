# Fixtures and Parametrize

## Fixtures

Fixtures replace `setUp`/`tearDown` with composable, scoped dependency injection:

```python
import pytest

@pytest.fixture
def db_connection():
    conn = create_connection()
    yield conn                     # test runs here
    conn.close()                   # teardown

def test_query(db_connection):
    result = db_connection.execute("SELECT 1")
    assert result == 1
```

## Fixture Scopes

```python
@pytest.fixture(scope="function")   # default — new per test
def fresh_user(): ...

@pytest.fixture(scope="class")      # shared within a test class
def class_db(): ...

@pytest.fixture(scope="module")     # shared within a module
def module_db(): ...

@pytest.fixture(scope="session")    # shared across all tests
def session_db():
    db = create_database()
    yield db
    db.drop()
```

Use narrower scopes by default. Session scope is for expensive resources
(database connections, Docker containers).

## `conftest.py`

Fixtures in `conftest.py` are available to all tests in that directory and below:

```
tests/
    conftest.py          # fixtures available to all tests
    test_auth.py
    integration/
        conftest.py      # additional fixtures for integration tests
        test_database.py
```

No import needed — pytest discovers `conftest.py` automatically.

## Fixture Composition

Fixtures can depend on other fixtures:

```python
@pytest.fixture
def db():
    return create_database()

@pytest.fixture
def user(db):
    return db.create_user("Alice")

@pytest.fixture
def admin_user(db):
    user = db.create_user("Admin")
    db.grant_admin(user)
    return user

def test_admin_access(admin_user, db):
    assert db.check_admin(admin_user)
```

## `autouse` Fixtures

Apply to all tests automatically:

```python
@pytest.fixture(autouse=True)
def reset_env(monkeypatch):
    """Ensure clean environment for every test."""
    monkeypatch.delenv("API_KEY", raising=False)
```

## Parametrize

Table-driven tests — the Python equivalent of Go's `t.Run()` subtests:

```python
@pytest.mark.parametrize("input,expected", [
    ("hello", 5),
    ("", 0),
    ("  spaces  ", 10),
    ("unicode: ñ", 10),
])
def test_string_length(input, expected):
    assert len(input) == expected
```

### Multiple Parameters

```python
@pytest.mark.parametrize("x", [1, 2, 3])
@pytest.mark.parametrize("y", [10, 20])
def test_multiplication(x, y):
    # Runs 6 tests: all combinations of x and y
    assert x * y > 0
```

### Parametrize with IDs

```python
@pytest.mark.parametrize("user,expected_role", [
    pytest.param({"name": "Alice", "admin": True}, "admin", id="admin-user"),
    pytest.param({"name": "Bob", "admin": False}, "viewer", id="regular-user"),
])
def test_role_assignment(user, expected_role):
    assert assign_role(user) == expected_role
```

### Indirect Parametrize

Pass parameters to a fixture instead of the test function:

```python
@pytest.fixture
def user(request):
    return create_user(role=request.param)

@pytest.mark.parametrize("user", ["admin", "viewer"], indirect=True)
def test_permissions(user):
    ...
```

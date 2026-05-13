# Mocking and Fixtures

## Trait-Based Mocking (Preferred)

Design code with traits for testability — then provide test implementations:

```rust
trait UserRepository {
    fn find_by_id(&self, id: u64) -> Result<Option<User>>;
    fn save(&mut self, user: &User) -> Result<()>;
}

// Production implementation
struct PostgresUserRepo { pool: PgPool }
impl UserRepository for PostgresUserRepo { ... }

// Test implementation — no external dependencies
struct MockUserRepo {
    users: HashMap<u64, User>,
}

impl UserRepository for MockUserRepo {
    fn find_by_id(&self, id: u64) -> Result<Option<User>> {
        Ok(self.users.get(&id).cloned())
    }

    fn save(&mut self, user: &User) -> Result<()> {
        self.users.insert(user.id, user.clone());
        Ok(())
    }
}

#[test]
fn test_user_service() {
    let mut repo = MockUserRepo { users: HashMap::new() };
    let service = UserService::new(&mut repo);
    // test with the mock
}
```

## mockall for Complex Mocking

When manual mocks are tedious, use `mockall`:

```rust
use mockall::automock;

#[automock]
trait HttpClient {
    fn get(&self, url: &str) -> Result<Response>;
    fn post(&self, url: &str, body: &[u8]) -> Result<Response>;
}

#[test]
fn test_api_client() {
    let mut mock = MockHttpClient::new();

    mock.expect_get()
        .with(eq("https://api.example.com/users"))
        .times(1)
        .returning(|_| Ok(Response::new(200, b"[]")));

    let client = ApiClient::new(mock);
    let users = client.list_users().unwrap();
    assert!(users.is_empty());
}
```

### mockall Expectations

```rust
mock.expect_method()
    .with(eq(arg))           // argument matcher
    .times(1)                // exact call count
    .times(1..=3)            // range of call counts
    .returning(|arg| Ok(()))  // return value
    .once()                  // shorthand for times(1)
    .never()                 // must not be called
    ;
```

## rstest Fixtures

`rstest` provides fixture-based testing:

```rust
use rstest::*;

#[fixture]
fn test_config() -> Config {
    Config {
        host: "localhost".into(),
        port: 8080,
        ..Config::default()
    }
}

#[fixture]
fn test_db(test_config: Config) -> TestDb {
    TestDb::new(&test_config.database_url)
}

#[rstest]
fn test_connection(test_db: TestDb) {
    assert!(test_db.ping().is_ok());
}
```

### rstest Parameterized Tests

```rust
#[rstest]
#[case("80", 80)]
#[case("443", 443)]
#[case("8080", 8080)]
fn test_parse_port(#[case] input: &str, #[case] expected: u16) {
    assert_eq!(parse_port(input).unwrap(), expected);
}

#[rstest]
#[case("", "empty input")]
#[case("abc", "invalid number")]
#[case("99999", "out of range")]
fn test_parse_port_errors(#[case] input: &str, #[case] desc: &str) {
    assert!(parse_port(input).is_err(), "expected error for: {desc}");
}
```

## tempfile for Filesystem Tests

```rust
use tempfile::{tempdir, NamedTempFile};

#[test]
fn test_config_save_and_load() {
    let dir = tempdir().unwrap();
    let path = dir.path().join("config.toml");

    let config = Config::default();
    config.save(&path).unwrap();

    let loaded = Config::load(&path).unwrap();
    assert_eq!(config, loaded);
}

#[test]
fn test_log_rotation() {
    let file = NamedTempFile::new().unwrap();
    let logger = FileLogger::new(file.path());
    logger.write("test message").unwrap();

    let contents = std::fs::read_to_string(file.path()).unwrap();
    assert!(contents.contains("test message"));
}
```

## Test Helpers

Mark test helpers with a `#[cfg(test)]` module or a test utility crate:

```rust
#[cfg(test)]
mod test_helpers {
    use super::*;

    pub fn make_test_user(name: &str) -> User {
        User {
            id: rand::random(),
            name: name.to_string(),
            email: format!("{name}@test.com"),
            ..User::default()
        }
    }
}
```

For integration tests, share helpers via `tests/common/mod.rs`.

## Prefer Real Dependencies Over Mocks

**Mock only external services** — databases, HTTP APIs, message queues.
For internal code, use real implementations:

```rust
// Prefer: test with real parser
#[test]
fn test_pipeline() {
    let parser = Parser::new();
    let formatter = Formatter::new();
    let result = formatter.format(&parser.parse("input").unwrap());
    assert_eq!(result, "expected");
}

// Avoid: mocking internal components
#[test]
fn test_pipeline_over_mocked() {
    let mut mock_parser = MockParser::new();
    mock_parser.expect_parse().returning(|_| Ok(ast));  // brittle!
    // ...
}
```

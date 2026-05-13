# Result and Option

## When to Use Which

| Type | Meaning | Use When |
|------|---------|----------|
| `Result<T, E>` | Success or failure with error info | Operation can fail with a reason |
| `Option<T>` | Value present or absent | Absence is not an error (lookup, optional field) |

```rust
// Result: operation can fail
fn parse_port(s: &str) -> Result<u16, ParseIntError> {
    s.parse()
}

// Option: absence is expected
fn find_user(id: u64) -> Option<&User> {
    self.users.get(&id)
}
```

**Don't use `Option` for errors** — if the caller needs to know *why*
something failed, use `Result`.

## The ? Operator

Propagates errors (or `None`) to the caller. The most important error-handling
tool in Rust:

```rust
fn read_config(path: &Path) -> Result<Config, Box<dyn Error>> {
    let content = std::fs::read_to_string(path)?;  // returns Err early
    let config: Config = toml::from_str(&content)?;
    Ok(config)
}
```

`?` works on both `Result` and `Option`:
```rust
fn first_line(text: &str) -> Option<&str> {
    let line = text.lines().next()?;  // returns None early
    Some(line.trim())
}
```

`?` automatically converts error types via `From`:
```rust
// If io::Error implements From<CustomError>, ? converts automatically
fn process() -> Result<(), io::Error> {
    let data = my_lib_call()?;  // CustomError → io::Error via From
    Ok(())
}
```

## Option Combinators

Prefer combinators over `match` when only one arm matters:

```rust
// Instead of match
let display_name = match user.nickname {
    Some(nick) => nick,
    None => user.name.clone(),
};

// Use unwrap_or / unwrap_or_else
let display_name = user.nickname.unwrap_or_else(|| user.name.clone());

// Other useful combinators
opt.map(|x| x * 2)                    // transform inner value
opt.and_then(|x| x.checked_add(1))    // chain fallible operations
opt.filter(|x| x > &0)               // conditional keep
opt.unwrap_or_default()               // use Default trait
opt.ok_or(MyError::Missing)?          // Option → Result
```

## Result Combinators

```rust
result.map(|v| v.to_string())          // transform Ok value
result.map_err(|e| MyError::from(e))   // transform Err value
result.and_then(|v| validate(v))       // chain fallible operations
result.unwrap_or_default()             // Ok value or default
result.ok()                            // Result → Option (discards error)
```

## let-else for Early Returns

Rust 1.65+ — extract from `Option`/`Result` with a diverging else:

```rust
let Some(user) = find_user(id) else {
    return Err(AppError::UserNotFound(id));
};
// user is now unwrapped and available

let Ok(config) = load_config() else {
    eprintln!("failed to load config, using defaults");
    return Ok(Config::default());
};
```

## if-let Chains

For simple extraction without else:

```rust
if let Some(user) = find_user(id) {
    if let Some(email) = user.email() {
        send_notification(email);
    }
}
```

## Converting Between Result and Option

```rust
// Option → Result
let value = opt.ok_or(MyError::NotFound)?;
let value = opt.ok_or_else(|| MyError::not_found(key))?;

// Result → Option
let maybe = result.ok();   // discards the error
let maybe = result.err();  // discards the success
```

## Anti-Patterns

```rust
// Bad: using unwrap in production code
let value = map.get("key").unwrap();

// Bad: matching just to rewrap
match result {
    Ok(v) => Ok(v),
    Err(e) => Err(e.into()),
}
// Good: use ? with From
let v = result?;

// Bad: .is_some() followed by .unwrap()
if opt.is_some() {
    let val = opt.unwrap();
}
// Good: use if-let
if let Some(val) = opt {
    // use val
}
```

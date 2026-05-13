# std::expected and std::optional

## `std::expected<T, E>` (C++23)

Represents "the expected result T, or error information E." Use when failure
is a normal outcome and the caller needs to know why.

```cpp
enum class DbError { NotFound, ConnectionLost, PermissionDenied };

std::expected<User, DbError> findUser(UserId id) {
    if (!db_.connected()) return std::unexpected(DbError::ConnectionLost);
    auto row = db_.query(id);
    if (!row) return std::unexpected(DbError::NotFound);
    return User::fromRow(*row);
}

// Explicit check
auto result = findUser(42);
if (result) process(*result);
else handleError(result.error());

// value_or for default fallback
auto user = findUser(42).value_or(User::anonymous());
```

## Monadic Operations (C++23)

Chain operations that short-circuit on the first error:

| Operation | Applies to | Function returns | Result |
|-----------|-----------|-----------------|--------|
| `and_then(f)` | value | `expected<U,E>` | Chain that can fail |
| `transform(f)` | value | `U` (plain) | Infallible transform |
| `or_else(f)` | error | `expected<T,E2>` | Error recovery |
| `transform_error(f)` | error | `E2` | Error mapping |

```cpp
std::expected<Report, ReportError>
generateReport(std::string_view input) {
    return parseInput(input)
        .and_then(validateData)
        .and_then(enrichWithMetadata)
        .transform(formatAsReport)
        .transform_error([](auto e) {
            return ReportError{e.code, "Report: " + e.msg};
        });
}
```

## `std::optional<T>` (C++17)

Represents "a value or nothing." Use when absence is semantically correct
with no error information needed.

```cpp
std::optional<User> findByEmail(std::string_view email) {
    auto it = db_.find(email);
    if (it == db_.end()) return std::nullopt;
    return it->second;
}

auto user = findByEmail("alice@example.com");
if (user) std::cout << user->name;

// value_or
std::string name = findByEmail("bob").value_or(User{"anon"}).name;

// C++23 monadic chaining
std::optional<std::string> getAddress(UserId id) {
    return findUser(id)
        .and_then(getUserAddress)
        .transform(formatAddress);
}
```

### Optional as Member

```cpp
// CORRECT: genuinely optional information
struct UserProfile {
    std::string name;
    std::optional<std::string> bio;
    std::optional<PhoneNumber> phone;
};
```

## Error Propagation Without Exceptions

For exception-free codebases, `std::expected` provides complete error propagation:

```cpp
// Monadic chaining — cleaner than nested if-checks
std::expected<CompleteData, Error>
processAll(std::string_view input) {
    return parseInput(input)
        .and_then(validate)
        .and_then(enrich)
        .and_then(transform);
}
```

## Error Type Design

```cpp
// Rich error with context
struct ParseError {
    enum class Code { InvalidSyntax, UnexpectedEOF, Overflow };
    Code code;
    std::string message;
    size_t line = 0;
};

// System errors: use std::error_code
std::expected<FileContent, std::error_code> readFile(fs::path p) {
    std::ifstream f(p);
    if (!f) return std::unexpected(
        std::make_error_code(std::errc::no_such_file_or_directory));
    return std::string(std::istreambuf_iterator<char>(f), {});
}
```

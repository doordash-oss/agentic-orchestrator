# Exceptions vs Error Codes

## The Decision Framework

| Mechanism | Use when... |
|-----------|-------------|
| Exceptions | Constructors/operators must signal failure; errors propagate deep call chains; failure is rare (<0.1%) |
| `std::expected<T,E>` | Failure is normal and expected; caller must know *why*; exception-free codebase; performance on failure path matters |
| `std::optional<T>` | Absence is semantically correct with no error information needed |
| Error codes | Legacy APIs; C interop; hard real-time/safety-critical contexts |

## When to Use Exceptions

### Constructors — No Alternative Exists

```cpp
// CORRECT: Constructor failure must use exceptions
class NetworkConnection {
public:
    explicit NetworkConnection(std::string_view host, uint16_t port) {
        socket_ = ::connect(host, port);
        if (socket_ < 0)
            throw std::runtime_error("Failed to connect");
    }
};

// WRONG: "Zombie object" anti-pattern
class NetworkConnection {
    bool valid_ = false;
public:
    NetworkConnection(...) { valid_ = (connect() >= 0); }
    bool IsValid() const { return valid_; }  // Callers can forget to check
};
```

### Deep Call Chains

Exceptions propagate automatically — no error-code plumbing needed:

```cpp
void processRequest(Request& req) {
    auto data = parseBody(req);     // may throw ParseError
    auto result = compute(data);    // may throw ComputeError
    sendResponse(result);           // may throw NetworkError
}
```

## When NOT to Use Exceptions

### Expected Failures

```cpp
// WRONG: Parsing user input commonly fails
double val = std::stod(input);  // throws std::invalid_argument

// CORRECT: Use std::expected
std::expected<double, ParseError> parseDouble(std::string_view s);
```

### Hot Paths

```cpp
// WRONG: Exception-driven control flow in a loop
try {
    for (int k : keys) set.at(k);
} catch (const std::out_of_range&) { return false; }

// CORRECT: Use return-value API
return std::ranges::all_of(keys, [&](int k) { return set.contains(k); });
```

## The Exception Cost Model

- **Happy path**: zero runtime cost (metadata in side tables)
- **Sad path**: ~1000ns per throw/catch (100x slower than return-based)
- **Binary size**: exception tables always present, increasing cache pressure
- **Implication**: appropriate when exception rate is very low

## Exception Type Hierarchy

```cpp
class AppError : public std::runtime_error {
public:
    explicit AppError(std::string msg, std::error_code code)
        : std::runtime_error(std::move(msg)), code_(code) {}
    std::error_code code() const noexcept { return code_; }
private:
    std::error_code code_;
};

class NetworkError : public AppError { /* ... */ };
class ParseError   : public AppError { /* ... */ };

// Catch specific first, then base
try { doWork(); }
catch (const NetworkError& e) { reconnect(); }
catch (const AppError& e) { reportError(e); }
```

Never throw built-in types (`throw 42`, `throw "error"`) — no context, no hierarchy.

## No-Exception Codebases (Google/LLVM Style)

When exceptions are banned, use `std::expected` or similar value-based error types.
Google bans exceptions due to legacy migration constraints, not because exceptions
are wrong in new code.

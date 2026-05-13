# Strong Typing

## The Problem: Primitive Obsession

Using raw `int`, `double`, `string` for domain concepts creates ambiguous,
fragile interfaces:

```cpp
// UNSAFE: parameters easily transposed, no compile-time protection
void create_user(std::string name, std::string email, int age);
void blink_led(int time_to_blink);  // What unit?
```

## `enum class` — Scoped Enumerations

Always use `enum class` over plain `enum`. Specify the underlying type:

```cpp
enum class HttpStatus : uint16_t {
    Ok = 200, BadRequest = 400, NotFound = 404, ServerError = 500
};

enum class Permissions : uint8_t {
    Execute = 1 << 0, Write = 1 << 1, Read = 1 << 2
};
```

### Convert to Underlying Type

```cpp
// C++23
auto raw = std::to_underlying(HttpStatus::Ok);

// Pre-C++23
auto raw = static_cast<std::underlying_type_t<HttpStatus>>(HttpStatus::Ok);

// C++20: using enum in switch
switch (status) {
    using enum HttpStatus;
    case Ok:       handle_ok(); break;
    case NotFound: handle_404(); break;
}
```

## Phantom Type / NamedType Pattern

Create distinct types from the same underlying type using a tag:

```cpp
template <typename T, typename Tag>
class NamedType {
public:
    explicit NamedType(T value) : value_(std::move(value)) {}
    T& get() { return value_; }
    const T& get() const { return value_; }
private:
    T value_;
};

using Width  = NamedType<double, struct WidthTag>;
using Height = NamedType<double, struct HeightTag>;

Rectangle r(Width{10.0}, Height{12.0});    // Safe: order enforced by types
// Rectangle bad(Height{12.0}, Width{10.0}); // COMPILE ERROR
```

Zero-cost abstraction at O1/O2 — compilers emit identical code to raw arithmetic.

## Opaque Integer Typedefs

Use empty `enum class` for domain-specific integer IDs:

```cpp
enum class UserId   : uint64_t {};
enum class ProductId: uint32_t {};

UserId uid{42};
// OrderId oid = uid;  // COMPILE ERROR: distinct types
```

## `std::byte` for Raw Memory

```cpp
void process_packet(std::span<const std::byte> buffer);

std::byte buf[256];
std::byte value = std::byte{0xAB};
```

Never use `char*` or `unsigned char*` for raw memory in new code.

## User-Defined Literals

```cpp
constexpr Milliseconds operator""_ms(unsigned long long v) {
    return Milliseconds{static_cast<long long>(v)};
}

blink_led(500_ms);  // Clear, type-safe
// blink_led(500);  // COMPILE ERROR
```

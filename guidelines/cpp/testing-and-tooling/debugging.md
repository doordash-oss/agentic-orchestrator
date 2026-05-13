# Debugging

## GDB / LLDB Essentials

GCC projects pair with GDB; Clang/LLVM projects pair with LLDB.

### Common GDB Commands

```
break file.cc:42        Set breakpoint at line
break Class::Method     Set breakpoint at method
condition 1 x == 5      Make breakpoint conditional
watch variable          Break on write
bt                      Print call stack
frame N                 Switch to frame
info locals             Show local variables
print expr              Evaluate expression
step / next / finish    Step into / over / to return
thread apply all bt     Backtraces for all threads
```

### Conditional Breakpoints

The highest-leverage debugging technique:
```
(gdb)  break process.cc:142 if items > 1000
(lldb) breakpoint set -f process.cc -l 142 -c "items > 1000"
```

## Core Dump Analysis

```bash
# Enable core dumps (Linux)
ulimit -c unlimited
echo "/tmp/core.%p" | sudo tee /proc/sys/kernel/core_pattern

# Analyze
gdb ./my_program /tmp/core.1234
(gdb) bt
(gdb) info locals
(gdb) frame 2
(gdb) print *ptr
```

The binary must have been compiled with `-g`. For production binaries,
maintain separate debug symbols:
```bash
objcopy --only-keep-debug program program.debug
objcopy --strip-debug --add-gnu-debuglink=program.debug program
```

## Debug vs Release Builds

| Aspect | Debug (`-O0 -g`) | Release (`-O2 -DNDEBUG`) |
|--------|-------------------|--------------------------|
| Symbols | Full | Stripped |
| Optimization | None | Aggressive |
| Assertions | Active | Disabled |
| Debuggability | Full | Variables may be optimized out |

Maintain three configurations:
1. **Debug** (`-O0 -g -DDEBUG`): interactive debugging
2. **Sanitize** (`-O1 -g -fsanitize=address,undefined`): automated testing
3. **Release** (`-O2 -DNDEBUG`): performance and production

## Assertion Strategies

### `assert` (Runtime, Debug-Only)

```cpp
void ProcessBuffer(const char* buf, size_t len) {
    assert(buf != nullptr && "Buffer must not be null");
    assert(len > 0 && "Length must be positive");
}
```

**Never put side effects inside `assert()`** — they disappear in release:
```cpp
// BAD: connection not established in release
assert(connect() == 0);

// GOOD: capture result
int rc = connect();
assert(rc == 0);
```

### `static_assert` (Compile-Time)

```cpp
static_assert(sizeof(int) == 4, "int must be 32-bit");
static_assert(std::is_trivially_copyable_v<T>);  // C++17: message optional
```

Never disabled — fires at compile time regardless of build. Zero runtime cost.

### When to Use Each

| Mechanism | When |
|-----------|------|
| `static_assert` | Compile-time invariants: sizes, alignments, type traits |
| `assert` | Runtime invariants in debug builds |
| Exceptions | User-facing errors, recoverable conditions |
| Return codes / `std::expected` | Expected operation failures |

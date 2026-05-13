# Null Annotations

## Why Use Null Annotations

Null annotations declare the null contract of your APIs at compile time,
enabling static analysis tools to catch null-related bugs before runtime:

```java
public @NonNull User findUser(@NonNull String id) { ... }
// Tools know: id must not be null, result is never null

public @Nullable Address getAddress(@NonNull String userId) { ... }
// Tools know: userId must not be null, result may be null
```

## Choosing an Annotation Library

| Library | Package | Recommended? |
|---------|---------|-------------|
| **JSpecify** | `org.jspecify.annotations` | Yes — new standard, endorsed by Google/JetBrains |
| JetBrains | `org.jetbrains.annotations` | Good for IntelliJ projects |
| Eclipse | `org.eclipse.jdt.annotation` | Good for Eclipse projects |
| Jakarta | `jakarta.annotation` | If using Jakarta EE |
| JSR 305 (FindBugs) | `javax.annotation` | Legacy — avoid for new projects |

**JSpecify** is the modern choice. It provides:
- `@Nullable` — this reference may be null
- `@NonNull` — this reference must not be null
- `@NullMarked` — class/package-level default: all references are non-null unless marked `@Nullable`

## Package-Level @NullMarked

Mark an entire package as non-null by default:

```java
// package-info.java
@NullMarked
package com.example.order;

import org.jspecify.annotations.NullMarked;
```

Now every reference in the package is non-null unless explicitly annotated
`@Nullable`. This is the most effective strategy — annotate the exception (null),
not the rule (non-null).

## Tool Integration

### Error Prone (Google)

Google's Error Prone compiler plugin enforces null safety with JSpecify:

```xml
<!-- Maven -->
<plugin>
    <groupId>com.google.errorprone</groupId>
    <artifactId>error_prone_core</artifactId>
    <version>...</version>
</plugin>
```

### IntelliJ IDEA

IntelliJ understands JetBrains, JSpecify, and Eclipse annotations natively.
Enable inspections under Settings > Inspections > Java > Probable bugs > Nullability.

### NullAway (Uber)

Fast null checker built on Error Prone:

```gradle
errorprone("com.uber.nullaway:nullaway:...")
```

## Best Practices

1. **Use `@NullMarked` at the package level** — declare the default, annotate exceptions
2. **Annotate public API boundaries** — method parameters, return types, fields
3. **Choose one annotation library** for the whole project
4. **Combine with Optional** — return `Optional` for methods where absence is expected;
   use `@Nullable` for internal code where Optional would be overhead
5. **Enable static analysis in CI** — Error Prone or NullAway catch bugs early

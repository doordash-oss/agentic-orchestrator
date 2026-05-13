# Javadoc

## When to Write Javadoc

- **All public classes and interfaces** — the first sentence is the summary
- **All public and protected methods** — describe contract, not implementation
- **Non-obvious method parameters and return values**
- **Exceptions thrown** — both checked and unchecked precondition violations

Javadoc is optional for:
- Private methods (use comments only if the logic is non-obvious)
- Self-documenting methods like `getName()` on a simple getter
- Overridden methods where the parent's Javadoc applies unchanged

## Format

```java
/**
 * Transfers funds between two accounts.
 *
 * <p>The transfer is atomic — either both accounts are updated or neither is.
 * The source account must have sufficient balance; otherwise an exception is thrown
 * and no changes are made.
 *
 * @param from   the source account (must not be closed)
 * @param to     the destination account (must not be closed)
 * @param amount the amount to transfer (must be positive)
 * @return the resulting transaction record
 * @throws InsufficientFundsException if {@code from} has insufficient balance
 * @throws AccountClosedException     if either account is closed
 * @throws IllegalArgumentException   if {@code amount} is not positive
 * @since 2.1
 */
public Transaction transfer(Account from, Account to, BigDecimal amount) { ... }
```

## The First Sentence

The first sentence (up to the first period) is the **summary fragment**. It
appears in method listings and search results. Make it count:

```java
// Good — starts with a verb, describes what the method does
/** Finds the user with the given email address. */

// Bad — starts with "This method"
/** This method finds the user with the given email address. */

// Bad — too vague
/** Processes the input. */
```

**Rules for the summary fragment**:
- Start with a third-person verb: "Returns", "Finds", "Creates", "Throws"
- Don't start with "This method" or "This class"
- End with a period
- Keep it on one line if possible

## @param, @return, @throws

- **@param**: describe each parameter, including constraints
- **@return**: describe what is returned, including edge cases
- **@throws**: document every exception with the condition that triggers it

```java
/**
 * @param id the user ID (must not be {@code null} or blank)
 * @return the user, or an empty Optional if no user exists with this ID
 * @throws IllegalArgumentException if {@code id} is null or blank
 */
public Optional<User> findById(String id) { ... }
```

## Inline Tags

| Tag | Use |
|-----|-----|
| `{@code text}` | Inline code — renders in monospace, escapes HTML |
| `{@link ClassName#method}` | Hyperlink to another type or method |
| `{@linkplain ClassName text}` | Hyperlink with custom display text |
| `{@literal <>&}` | Literal text, escapes HTML |
| `{@inheritDoc}` | Copy Javadoc from the overridden method |

```java
/**
 * Returns {@code true} if this collection contains the specified element.
 *
 * @see Collection#contains(Object)
 * @see #add(Object)
 */
```

## Package Documentation

Create `package-info.java` for package-level Javadoc:

```java
/**
 * Order management domain model.
 *
 * <p>This package contains the core order entities, value objects,
 * and repository interfaces. See {@link OrderService} for the primary
 * entry point.
 */
package com.example.order;
```

## Javadoc Anti-Patterns

```java
// Wrong — restates the method name
/** Gets the name. */
public String getName() { ... }

// Wrong — documents implementation, not contract
/** Uses a HashMap to cache results. */
public Result compute(Input input) { ... }

// Wrong — no useful information
/** The order. */
private Order order;

// Wrong — @param with no description
/** @param id */
public User findById(String id) { ... }
```

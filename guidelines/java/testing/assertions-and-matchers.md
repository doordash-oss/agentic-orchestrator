# Assertions and Matchers

## AssertJ — The Preferred Assertion Library

AssertJ provides fluent, readable assertions that produce clear failure messages.
Prefer it over JUnit's built-in `assertEquals`/`assertTrue`:

```java
import static org.assertj.core.api.Assertions.*;

// Basic assertions
assertThat(result).isEqualTo(expected);
assertThat(name).isNotNull().isNotBlank().startsWith("A");
assertThat(age).isPositive().isLessThan(150);
assertThat(flag).isTrue();
```

## Collection Assertions

```java
List<String> names = List.of("Alice", "Bob", "Carol");

assertThat(names)
    .hasSize(3)
    .contains("Alice", "Bob")
    .doesNotContain("Dave")
    .containsExactly("Alice", "Bob", "Carol")  // order matters
    .containsExactlyInAnyOrder("Carol", "Alice", "Bob");

// Extract and assert on properties
List<User> users = ...;
assertThat(users)
    .extracting(User::name)
    .containsExactly("Alice", "Bob");

assertThat(users)
    .extracting(User::name, User::age)
    .contains(tuple("Alice", 30), tuple("Bob", 25));

// Filtering
assertThat(users)
    .filteredOn(User::isActive)
    .hasSize(2)
    .extracting(User::name)
    .contains("Alice");
```

## Exception Assertions

```java
// Assert exception type and message
assertThatThrownBy(() -> service.process(null))
    .isInstanceOf(IllegalArgumentException.class)
    .hasMessage("input must not be null")
    .hasNoCause();

// Assert exception with cause chain
assertThatThrownBy(() -> service.loadConfig())
    .isInstanceOf(ConfigException.class)
    .hasMessageContaining("config.yaml")
    .hasCauseInstanceOf(IOException.class);

// Catch and inspect
Throwable thrown = catchThrowable(() -> service.process(null));
assertThat(thrown).isInstanceOf(IllegalArgumentException.class);

// Assert no exception is thrown
assertThatCode(() -> service.process(validInput)).doesNotThrowAnyException();
```

## Soft Assertions

Collect all failures before reporting (useful for validating multiple fields):

```java
@Test
void shouldReturnCompleteUser() {
    User user = service.getUser("123");

    SoftAssertions.assertSoftly(softly -> {
        softly.assertThat(user.name()).isEqualTo("Alice");
        softly.assertThat(user.email()).contains("@");
        softly.assertThat(user.age()).isPositive();
        softly.assertThat(user.isActive()).isTrue();
    });
    // All failures reported together, not just the first one
}
```

## String Assertions

```java
assertThat(response)
    .contains("success")
    .doesNotContain("error")
    .startsWith("{")
    .endsWith("}")
    .matches("\\{.*\"status\":\"ok\".*\\}");
```

## Optional Assertions

```java
assertThat(findUser("valid-id")).isPresent().hasValueSatisfying(user ->
    assertThat(user.name()).isEqualTo("Alice"));

assertThat(findUser("missing-id")).isEmpty();
```

## Map Assertions

```java
Map<String, Integer> scores = Map.of("Alice", 95, "Bob", 87);

assertThat(scores)
    .containsKey("Alice")
    .containsEntry("Alice", 95)
    .doesNotContainKey("Carol")
    .hasSize(2);
```

## Comparison Assertions

```java
// BigDecimal (value equality, not scale equality)
assertThat(total).isEqualByComparingTo(new BigDecimal("99.99"));

// Custom comparator
assertThat(users)
    .usingElementComparator(Comparator.comparing(User::name))
    .contains(expectedUser);

// Recursive comparison (ignoring implementation of equals)
assertThat(actual)
    .usingRecursiveComparison()
    .ignoringFields("id", "createdAt")
    .isEqualTo(expected);
```

## Why AssertJ Over JUnit Assertions

```java
// JUnit — cryptic failure message
assertEquals(expected, actual);  // which is expected, which is actual?

// AssertJ — clear, chainable, great failure messages
assertThat(actual).isEqualTo(expected);
// Failure: expected: "Alice" but was: "Bob"
```

AssertJ also provides IDE auto-completion for all assertion methods,
making it easy to discover available assertions.

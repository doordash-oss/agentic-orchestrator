# JUnit 5 and Assertions

JUnit 5 is the standard testing framework for Kotlin projects. Combined with
Kotlin's language features -- backtick method names, reified generics, and
concise lambdas -- it produces clean, readable tests.

## Basic Test Structure

Use `@Test` and backtick-wrapped names for human-readable descriptions:

```kotlin
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Assertions.assertEquals

class CalculatorTest {
    @Test
    fun `should add two numbers`() {
        val calc = Calculator()
        assertEquals(4, calc.add(2, 2))
    }

    @Test
    fun `should handle negative numbers`() {
        val calc = Calculator()
        assertEquals(-1, calc.add(1, -2))
    }
}
```

## Nested Tests for Hierarchical Organization

Use `@Nested` with `inner class` to group tests by context or scenario:

```kotlin
class UserServiceTest {
    private val repository = FakeUserRepository()
    private val service = UserService(repository)

    @Nested
    inner class WhenUserExists {
        @BeforeEach
        fun setUp() {
            repository.save(User("alice", "Alice"))
        }

        @Test
        fun `should return user`() {
            val user = service.findUser("alice")
            assertNotNull(user)
            assertEquals("Alice", user.name)
        }

        @Test
        fun `should update user`() {
            service.updateUser("alice", name = "Alice Updated")
            assertEquals("Alice Updated", repository.findById("alice")?.name)
        }
    }

    @Nested
    inner class WhenUserDoesNotExist {
        @Test
        fun `should throw NotFoundException`() {
            assertThrows<NotFoundException> {
                service.findUser("unknown")
            }
        }
    }
}
```

## Parameterized Tests

Use `@ParameterizedTest` for data-driven tests to avoid duplication:

```kotlin
@ParameterizedTest
@ValueSource(strings = ["", " ", "  "])
fun `should reject blank names`(name: String) {
    assertThrows<IllegalArgumentException> { User(name) }
}

@ParameterizedTest
@CsvSource(
    "1, 1, 2",
    "0, 0, 0",
    "-1, 1, 0"
)
fun `should add correctly`(a: Int, b: Int, expected: Int) {
    assertEquals(expected, Calculator().add(a, b))
}

@ParameterizedTest
@MethodSource("invalidEmails")
fun `should reject invalid emails`(email: String) {
    assertThrows<ValidationException> { Email(email) }
}

companion object {
    @JvmStatic
    fun invalidEmails() = listOf(
        "no-at-sign",
        "@missing-local",
        "missing-domain@",
        "spaces in@email.com"
    )
}
```

## Lifecycle Callbacks

```kotlin
class DatabaseTest {
    private lateinit var db: TestDatabase

    @BeforeEach
    fun setUp() {
        db = TestDatabase()
        db.migrate()
    }

    @AfterEach
    fun tearDown() {
        db.close()
    }

    companion object {
        @JvmStatic
        @BeforeAll
        fun globalSetUp() {
            // Runs once before all tests in this class
        }

        @JvmStatic
        @AfterAll
        fun globalTearDown() {
            // Runs once after all tests in this class
        }
    }
}
```

Use `@TempDir` for tests that need temporary file system access:

```kotlin
@Test
fun `should write report to file`(@TempDir tempDir: Path) {
    val outputFile = tempDir.resolve("report.txt")
    reporter.writeReport(outputFile)
    assertTrue(outputFile.exists())
    assertThat(outputFile.readText()).contains("Summary")
}
```

## Assertion Libraries

### JUnit 5 Built-in Assertions

```kotlin
assertEquals(expected, actual)
assertTrue(condition)
assertNotNull(value)
assertThrows<IllegalArgumentException> { riskyOperation() }

// Grouped assertions -- all run even if some fail
assertAll(
    { assertEquals("Alice", user.name) },
    { assertEquals("alice@example.com", user.email) },
    { assertTrue(user.isActive) }
)
```

### AssertJ -- Fluent Assertions

```kotlin
assertThat(result).isNotEmpty().hasSize(3)
assertThat(user.name).startsWith("A").endsWith("e")
assertThat(list).containsExactly("a", "b", "c")
assertThat(exception).hasMessageContaining("not found")
```

### Kotest Matchers -- Kotlin-Idiomatic

```kotlin
result shouldBe expected
list shouldHaveSize 3
name shouldStartWith "A"
value shouldBeInRange 1..100
user.email shouldMatch Regex(".+@.+\\..+")
```

### Google Truth

```kotlin
assertThat(result).isEqualTo(expected)
assertThat(list).containsExactly("a", "b", "c")
assertThat(user.isActive).isTrue()
```

## Exception Testing

```kotlin
@Test
fun `should throw with descriptive message`() {
    val exception = assertThrows<NotFoundException> {
        service.findUser("unknown")
    }
    assertThat(exception.message).contains("unknown")
}
```

## Test Fixtures

Prefer factory functions or `@BeforeEach` over test class inheritance:

```kotlin
// Good: factory function
private fun createUser(
    name: String = "Alice",
    email: String = "alice@example.com",
    active: Boolean = true
) = User(name, email, active)

@Test
fun `should deactivate user`() {
    val user = createUser(active = true)
    user.deactivate()
    assertFalse(user.isActive)
}
```

Avoid deep test class hierarchies. They obscure setup logic and make tests
harder to understand in isolation. Each test should clearly show its
preconditions, either inline or in a nearby `@BeforeEach`.

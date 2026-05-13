# JUnit 5 Fundamentals

## Test Lifecycle

```java
class OrderServiceTest {

    @BeforeAll   // once before all tests (must be static or @TestInstance(PER_CLASS))
    static void setupAll() { }

    @BeforeEach  // before each test method
    void setup() { }

    @Test
    void shouldCreateOrder() { }

    @AfterEach   // after each test method
    void teardown() { }

    @AfterAll    // once after all tests
    static void teardownAll() { }
}
```

## Test Naming

Name tests to describe the behavior, not the method:

```java
// Good — describes behavior and scenario
@Test
void shouldReturnEmptyWhenUserNotFound() { }

@Test
void shouldThrowWhenAmountIsNegative() { }

@Test
void shouldApplyDiscountForPremiumCustomers() { }

// Bad — describes the method being tested
@Test
void testGetUser() { }

@Test
void testProcess() { }
```

Use `@DisplayName` for human-readable test names when method names aren't enough:

```java
@Test
@DisplayName("Transfer fails atomically when source has insufficient funds")
void transferFailsAtomically() { }
```

## Parameterized Tests

Data-driven testing — run the same test logic with different inputs:

```java
@ParameterizedTest
@ValueSource(strings = {"", " ", "\t", "\n"})
void shouldRejectBlankInput(String input) {
    assertThatThrownBy(() -> validate(input))
        .isInstanceOf(IllegalArgumentException.class);
}

@ParameterizedTest
@CsvSource({
    "1, 1, 2",
    "0, 0, 0",
    "-1, 1, 0",
    "100, 200, 300"
})
void shouldAddNumbers(int a, int b, int expected) {
    assertThat(calculator.add(a, b)).isEqualTo(expected);
}

@ParameterizedTest
@MethodSource("orderTestCases")
void shouldCalculateTotal(List<Item> items, BigDecimal expectedTotal) {
    var order = new Order(items);
    assertThat(order.total()).isEqualByComparingTo(expectedTotal);
}

static Stream<Arguments> orderTestCases() {
    return Stream.of(
        Arguments.of(List.of(), BigDecimal.ZERO),
        Arguments.of(List.of(new Item("A", 10)), BigDecimal.TEN),
        Arguments.of(List.of(new Item("A", 10), new Item("B", 20)), BigDecimal.valueOf(30))
    );
}

@ParameterizedTest
@EnumSource(value = DayOfWeek.class, names = {"SATURDAY", "SUNDAY"})
void shouldBeWeekend(DayOfWeek day) {
    assertThat(isWeekend(day)).isTrue();
}
```

## Nested Tests

Organize related tests hierarchically:

```java
class OrderServiceTest {

    @Nested
    class WhenCreatingOrder {
        @Test
        void shouldCreateWithValidItems() { }

        @Test
        void shouldRejectEmptyItemList() { }
    }

    @Nested
    class WhenCancellingOrder {
        @Test
        void shouldCancelPendingOrder() { }

        @Test
        void shouldRejectCancellationOfShippedOrder() { }
    }
}
```

## Temporary Files

```java
@Test
void shouldWriteToFile(@TempDir Path tempDir) {
    Path file = tempDir.resolve("output.txt");
    service.writeTo(file);
    assertThat(file).exists().hasContent("expected");
}
```

## Test Assertions with assertThrows

```java
@Test
void shouldThrowOnInvalidInput() {
    var exception = assertThrows(IllegalArgumentException.class,
        () -> service.process(null));
    assertThat(exception.getMessage()).contains("must not be null");
}
```

## Test Instance Lifecycle

By default, JUnit creates a new test instance per method. Use `@TestInstance`
to share state:

```java
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class ExpensiveSetupTest {
    @BeforeAll  // no longer needs to be static
    void setup() { /* expensive one-time setup */ }
}
```

## Disabling and Conditional Tests

```java
@Disabled("Waiting for API fix — JIRA-123")
@Test
void shouldCallExternalApi() { }

@EnabledOnOs(OS.LINUX)
@Test
void shouldUseLinuxFeature() { }

@EnabledIfEnvironmentVariable(named = "CI", matches = "true")
@Test
void shouldRunOnlyInCI() { }
```

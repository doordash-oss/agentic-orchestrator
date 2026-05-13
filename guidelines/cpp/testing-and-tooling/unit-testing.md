# Unit Testing

## Google Test (GTest)

### ASSERT vs EXPECT

Use `EXPECT_*` by default — it allows multiple failures to surface in one run.
Use `ASSERT_*` only when continuing after failure would crash or make subsequent
assertions meaningless:

```cpp
TEST(ListTest, PopFront) {
    List<int> list = {1, 2, 3};
    ASSERT_FALSE(list.empty());   // Abort if empty — next line would crash
    EXPECT_EQ(list.front(), 1);   // Non-fatal: other checks still run
}
```

All assertion macros support `<<` for custom diagnostics:
```cpp
EXPECT_EQ(result, expected) << "Failed for input: " << input;
```

### TEST, TEST_F, TEST_P

- **`TEST(Suite, Name)`**: standalone test, no shared state
- **`TEST_F(Fixture, Name)`**: test attached to a fixture class
- **`TEST_P(Fixture, Name)`**: parameterized test

### Fixtures

Use `SetUp()`/`TearDown()` when setup can fail with a fatal assertion.
Use constructor/destructor when setup cannot fail:

```cpp
class DatabaseTest : public testing::Test {
protected:
    void SetUp() override {
        db_ = Database::Connect("test_db");
        ASSERT_TRUE(db_.IsConnected());
    }
    void TearDown() override { db_.Disconnect(); }
    Database db_;
};

TEST_F(DatabaseTest, InsertRow) {
    EXPECT_TRUE(db_.Insert({1, "Alice"}));
}
```

### Parameterized Tests

```cpp
class PrimeTest : public testing::TestWithParam<int> {};

TEST_P(PrimeTest, IsPrime) {
    EXPECT_TRUE(IsPrime(GetParam()));
}

INSTANTIATE_TEST_SUITE_P(SmallPrimes, PrimeTest,
    testing::Values(2, 3, 5, 7, 11, 13));
```

### Death Tests

Name suites ending in `"DeathTest"`:
```cpp
TEST(MathDeathTest, DivideByZero) {
    EXPECT_DEATH(Divide(5, 0), "Division by zero");
}
```

## Google Mock (GMock)

### Defining Mocks

```cpp
class MockPrinter : public Printer {
public:
    MOCK_METHOD(void, Print, (const std::string& msg), (override));
    MOCK_METHOD(int, GetPageCount, (), (const, override));
};
```

### Setting Expectations

```cpp
EXPECT_CALL(printer, Print("hello")).Times(1);
EXPECT_CALL(printer, GetPageCount()).WillRepeatedly(testing::Return(42));

// Default action without call-count expectation
ON_CALL(mock, IsReady()).WillByDefault(Return(true));
```

### Matchers

```cpp
using testing::_;
using testing::Gt;
using testing::HasSubstr;

EXPECT_CALL(mock, Foo(_, Gt(5)));
EXPECT_THAT(result, testing::AllOf(Gt(0), Lt(100)));
EXPECT_THAT(vec, testing::ElementsAre(1, 2, 3));
```

## Catch2

### Tests and Sections

Sections re-execute the test case from the top, one section per pass:

```cpp
TEST_CASE("Vector operations", "[vector]") {
    std::vector<int> v;
    v.push_back(1);          // Setup: runs before each section

    SECTION("size increases") {
        v.push_back(2);
        REQUIRE(v.size() == 2);
    }
    SECTION("element accessible") {
        REQUIRE(v[0] == 1);
    }
}
```

### REQUIRE vs CHECK

- `REQUIRE`: aborts test on failure (precondition checks)
- `CHECK`: records failure, continues (independent assertions)

### Tags

```cpp
TEST_CASE("HTTP client", "[http][integration]") { ... }
// Run: ./test "[http]"     Exclude: ./test "~[slow]"
```

## Test Doubles

| Type | Has Logic | Verifies | Primary Use |
|------|-----------|----------|-------------|
| Dummy | No | No | Fill unused parameters |
| Stub | Minimal | No | Control indirect inputs |
| Spy | Minimal | After execution | Record calls |
| Mock | Minimal | Before execution | Pre-specify expected interactions |
| Fake | Yes | No | Lightweight working replacement |

Mock at module/layer boundaries, not internal implementation details.

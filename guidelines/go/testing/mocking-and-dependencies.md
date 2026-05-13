# Mocking and Dependencies

## Prefer Real Dependencies Over Mocks

Mock only external services (network, databases, third-party APIs). For
in-process dependencies, use the real implementation whenever practical.

> More than 3 mocks in a test is a red flag — consider refactoring.

## Interface-Based Dependency Injection

Define small interfaces at the point of use (consumer-side):

```go
// In the package that needs the dependency:
type UserFinder interface {
    FindUser(ctx context.Context, id string) (*User, error)
}

type Handler struct {
    users UserFinder
}
```

The concrete implementation doesn't need to know about the interface. Go's
implicit interface satisfaction handles the wiring.

## Hand-Written Mocks (Simplest)

No dependencies needed. Best for small interfaces:

```go
type mockUserFinder struct {
    findUserFn func(ctx context.Context, id string) (*User, error)
}

func (m *mockUserFinder) FindUser(ctx context.Context, id string) (*User, error) {
    return m.findUserFn(ctx, id)
}

func TestHandler(t *testing.T) {
    mock := &mockUserFinder{
        findUserFn: func(ctx context.Context, id string) (*User, error) {
            return &User{ID: id, Name: "Test"}, nil
        },
    }
    h := NewHandler(mock)
    // test h
}
```

## testify/mock

Richer assertion API for larger test suites:

```go
type MockStore struct{ mock.Mock }

func (m *MockStore) FindUser(ctx context.Context, id string) (*User, error) {
    args := m.Called(ctx, id)
    return args.Get(0).(*User), args.Error(1)
}

func TestService(t *testing.T) {
    store := new(MockStore)
    store.On("FindUser", mock.Anything, "123").
        Return(&User{ID: "123"}, nil)

    svc := NewService(store)
    user, err := svc.GetUser(context.Background(), "123")

    assert.NoError(t, err)
    assert.Equal(t, "123", user.ID)
    store.AssertExpectations(t)
}
```

## gomock (go.uber.org/mock)

Code-generated mocks with strict expectations:

```bash
mockgen -source=store.go -destination=mock_store_test.go
```

```go
func TestService(t *testing.T) {
    ctrl := gomock.NewController(t)
    store := NewMockStore(ctrl)

    store.EXPECT().
        FindUser(gomock.Any(), "123").
        Return(&User{ID: "123"}, nil)

    svc := NewService(store)
    // test svc
}
```

Auto-generated mocks catch interface changes at compile time.

## testify/assert and testify/require

`assert.*` returns bool (test continues). `require.*` calls `t.FailNow()`
(test stops). Use `require` for preconditions, `assert` for multiple checks:

```go
// require for setup
user, err := svc.GetUser(ctx, "123")
require.NoError(t, err)
require.NotNil(t, user)

// assert for checks
assert.Equal(t, "Alice", user.Name)
assert.Equal(t, 30, user.Age)
```

## When to Use What

| Situation | Approach |
|-----------|----------|
| Simple interface, 1-2 methods | Hand-written mock |
| Many tests, complex expectations | testify/mock or gomock |
| In-process, fast dependency | Use the real implementation |
| External service (HTTP, DB) | Mock the interface or use httptest/testcontainers |
| File system | Use `testing/fstest.MapFS` |
| I/O edge cases | Use `testing/iotest` adversarial readers |

## Don't Mock Types You Don't Own

If a third-party library doesn't expose interfaces, wrap it:

```go
// Wrapper with your own interface
type EmailSender interface {
    Send(to, subject, body string) error
}

type mailgunSender struct {
    client *mailgun.Client
}

func (m *mailgunSender) Send(to, subject, body string) error {
    // delegate to mailgun client
}
```

Now mock `EmailSender` in tests, not the mailgun client directly.

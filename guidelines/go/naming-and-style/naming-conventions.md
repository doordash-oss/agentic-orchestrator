# Naming Conventions

## The Core Rule

Variable name length is proportional to scope distance. The further from its
declaration a name is used, the more descriptive it must be.

| Scope | Style | Examples |
|-------|-------|---------|
| Loop index | Single letter | `i`, `j`, `k` |
| Method receiver | 1-2 letters | `c` for Client, `s` for Server |
| Local variable | Short | `buf`, `err`, `ctx`, `req` |
| Function parameter | Descriptive if ambiguous | `timeout`, `path`, `userID` |
| Package-level | Descriptive | `maxRetries`, `defaultTimeout` |
| Exported | Self-documenting | `ErrNotFound`, `DefaultClient` |

## Package Names

- **Lowercase, single-word, no underscores, no mixedCaps**: `http`, `json`, `sync`
- **Short, concise, evocative**: the name should suggest what the package does
- **Never repeat the package name in exported identifiers**:
  - `http.Server` not `http.HTTPServer`
  - `ring.New()` not `ring.NewRing()`
  - `bytes.Buffer` not `bytes.ByteBuffer`

**Banned names**: `util`, `common`, `misc`, `base`, `helper`, `api`, `types`,
`interfaces`. If you can't name a package with a meaningful noun, the package
boundary is wrong.

**Transformation example:**
```go
// Bad:
package util
func NewStringSet(...string) map[string]bool

// Good:
package stringset
type Set map[string]bool
func New(...string) Set
```

## Getter/Setter Names

No `Get` prefix on getters. The getter for a field `owner` is `Owner()`:

```go
obj.Owner()       // getter — no Get prefix
obj.SetOwner(u)   // setter — Set prefix
```

## Receiver Names

- 1-2 letter abbreviation of the type: `c` for `Client`, `b` for `Buffer`
- **Never** `this`, `self`, or `me` — those give the receiver special OOP meaning
- **Be consistent**: if one method uses `c`, all methods use `c`

```go
func (c *Client) Do(req *Request) (*Response, error) { ... }
func (c *Client) Close() error { ... }
```

## Interface Names

Single-method interfaces append `-er` to the method name:

| Method | Interface |
|--------|-----------|
| `Read` | `Reader` |
| `Write` | `Writer` |
| `Close` | `Closer` |
| `String` | `Stringer` |
| `Format` | `Formatter` |

Honor canonical signatures from the standard library — if your method is called
`Read`, it should have signature `Read(p []byte) (n int, err error)`.

## Initialisms

Initialisms and acronyms maintain consistent case throughout a name:

| Correct | Wrong |
|---------|-------|
| `URL`, `url` | `Url` |
| `ServeHTTP` | `ServeHttp` |
| `userID` | `userId` |
| `xmlHTTPRequest` | `xmlHttpRequest` |
| `appID` | `appId` |

## Constants

Use `MixedCaps`, named for their role:

```go
const maxRetries = 3           // unexported
const DefaultTimeout = 30      // exported
const MaxPacketSize = 65535    // exported
```

Never `MAX_RETRIES` or `kMaxRetries` — Go does not use ALL_CAPS or k-prefix.

## MixedCaps, Always

Go uses `MixedCaps` or `mixedCaps` for all multi-word names. Never underscores
in identifiers (except in test file names and generated code).

```go
lineCount    // not line_count
parseJSON    // not parse_json
HTTPClient   // not HTTP_Client
```

## Variable Declaration Style

```go
var s []string               // zero-value declaration
s := make([]string, 0, 10)  // when you know capacity
s := []string{"a", "b"}     // literal initialization
```

Prefer `var` for zero-value declarations at package level. Prefer `:=` inside
functions.

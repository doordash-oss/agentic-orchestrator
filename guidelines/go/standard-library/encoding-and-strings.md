# Encoding and Strings

## encoding/json

### Struct Tags

```go
type User struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email,omitempty"` // omit if zero value
    Internal  string    `json:"-"`               // never marshal
    CreatedAt time.Time `json:"created_at"`
}
```

### Marshal/Unmarshal

```go
data, err := json.Marshal(user)
var user User
err := json.Unmarshal(data, &user)
```

### Streaming (Large Payloads)

```go
// Decode from reader (no intermediate []byte):
dec := json.NewDecoder(resp.Body)
var result Result
if err := dec.Decode(&result); err != nil { ... }

// Encode to writer:
enc := json.NewEncoder(w)
enc.SetIndent("", "  ") // pretty print
if err := enc.Encode(result); err != nil { ... }
```

### Custom Marshaling

```go
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
    return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
    var s string
    if err := json.Unmarshal(b, &s); err != nil {
        return err
    }
    dur, err := time.ParseDuration(s)
    if err != nil {
        return err
    }
    *d = Duration(dur)
    return nil
}
```

### Nil Slice vs Empty Slice in JSON

```go
var s []int         // marshals to null
s := []int{}        // marshals to []
s := make([]int, 0) // marshals to []
```

Choose based on whether your API consumers expect `null` or `[]`.

## String Handling

### strings Package

```go
strings.Contains(s, "needle")
strings.HasPrefix(s, "http://")
strings.HasSuffix(s, ".go")
strings.TrimSpace(s)
strings.Split(s, ",")
strings.Join(parts, ", ")
strings.ReplaceAll(s, "old", "new")
strings.EqualFold(a, b)  // case-insensitive comparison
```

### strings.Builder (Efficient Concatenation)

```go
var b strings.Builder
b.Grow(estimatedSize) // pre-allocate
for _, s := range items {
    b.WriteString(s)
    b.WriteByte(',')
}
result := b.String()
```

### bytes Package

Mirror of `strings` but for `[]byte`. Use `bytes.Buffer` for building byte
output, `bytes.Reader` for reading from a byte slice.

### strconv (Fast Conversions)

```go
s := strconv.Itoa(42)                     // int to string
n, err := strconv.Atoi("42")              // string to int
f, err := strconv.ParseFloat("3.14", 64)  // string to float
b, err := strconv.ParseBool("true")       // string to bool
s := strconv.FormatFloat(3.14, 'f', 2, 64) // float to string
```

Prefer `strconv` over `fmt.Sprintf` for primitive conversions — faster and
allocates less.

## Regular Expressions

### Pre-Compile at Package Level

```go
var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

func ValidateEmail(email string) bool {
    return emailRe.MatchString(email)
}
```

- `MustCompile` — panics on invalid regex. Use at package level with known patterns.
- `Compile` — returns error. Use when the pattern comes from user input.
- Compiled `*Regexp` is safe for concurrent use.

### Performance Note

Go's regexp uses a guaranteed linear-time algorithm (no backtracking). This
means no catastrophic backtracking, but also no backreferences. If you need
backreferences, consider a different approach.

## filepath vs path

| Package | Use For |
|---------|---------|
| `path/filepath` | OS file system paths (uses OS separator) |
| `path` | URL paths, logical paths (always `/`) |

```go
filepath.Join("dir", "file.txt")  // "dir/file.txt" or "dir\file.txt"
path.Join("api", "v1", "users")   // "api/v1/users" (always /)
```

Never use `path` for file system operations — it doesn't handle Windows paths.

## Printing and Formatting

```go
fmt.Printf("%v\n", val)    // default format
fmt.Printf("%+v\n", val)   // struct with field names
fmt.Printf("%#v\n", val)   // Go syntax representation
fmt.Printf("%T\n", val)    // type name
fmt.Printf("%q\n", str)    // quoted string
```

Implement `fmt.Stringer` for custom default formatting:

```go
func (s Status) String() string {
    return statusNames[s]
}
```

**Trap**: if `String()` calls `fmt.Sprintf("%s", s)`, it recurses infinitely.
Convert to the underlying type: `fmt.Sprintf("%s", string(s))`.

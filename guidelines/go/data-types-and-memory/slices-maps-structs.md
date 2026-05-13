# Slices, Maps, and Structs

## Zero Values

Go initializes all variables to their zero value. Design types so the zero value
is immediately useful:

| Type | Zero Value |
|------|-----------|
| `bool` | `false` |
| `int`, `float64` | `0` |
| `string` | `""` |
| `pointer`, `slice`, `map`, `channel`, `func`, `interface` | `nil` |
| `struct` | All fields zero-valued |

**Examples of useful zero values**: `sync.Mutex` (unlocked), `bytes.Buffer`
(empty, ready to write), `strings.Builder` (empty, ready to write).

## new vs make

| | `new(T)` | `make(T, args)` |
|---|---|---|
| Returns | `*T` (pointer to zeroed memory) | `T` (initialized value) |
| For | Any type | Slices, maps, channels only |
| State | Zero value | Internal structures initialized |

```go
p := new(bytes.Buffer)       // *bytes.Buffer, zero value, ready to use
s := make([]int, 0, 100)    // initialized slice with capacity
m := make(map[string]int)   // initialized map, ready for writes
ch := make(chan int, 10)     // buffered channel
```

## Slices

### Nil vs Empty

```go
var s []int              // nil slice: s == nil, len(s) == 0
s := []int{}             // empty slice: s != nil, len(s) == 0
s := make([]int, 0)      // empty slice: same as above
```

Both work identically for `append`, `len`, `cap`, and `range`. The difference
matters for:
- **JSON**: nil encodes to `null`, empty encodes to `[]`
- **Reflection**: `reflect.DeepEqual(nil, []int{})` is `false`

**Prefer `var s []int`** unless you specifically need non-nil for JSON.

### Pre-allocation

When the size is known or estimatable:

```go
result := make([]Item, 0, len(input)) // avoids reallocation
for _, item := range input {
    if item.Valid {
        result = append(result, item)
    }
}
```

### Slice Aliasing

Slices share backing arrays — modifications through one slice are visible
through others:

```go
a := []int{1, 2, 3, 4, 5}
b := a[1:3] // b is [2, 3], shares a's backing array
b[0] = 99   // a is now [1, 99, 3, 4, 5]
```

To make an independent copy:

```go
b := make([]int, len(a))
copy(b, a)
// or in Go 1.21+:
b := slices.Clone(a)
```

### append Returns a New Slice

Always capture the return value:

```go
s = append(s, item)         // correct
append(s, item)             // BUG: result discarded
s = append(s, other...)     // append another slice
```

## Maps

### Initialization

```go
var m map[string]int        // nil map — reads return zero, writes panic
m := make(map[string]int)  // initialized — ready for reads and writes
m := map[string]int{       // literal
    "a": 1,
    "b": 2,
}
```

**Never write to a nil map** — it panics. Always initialize before use.

### Comma-Ok Idiom

Distinguish missing keys from zero values:

```go
val, ok := m[key]
if !ok {
    // key not present
}
```

### Deletion

```go
delete(m, key) // safe even if key is absent
```

### Map Pre-sizing

When you know the approximate size:

```go
m := make(map[string]int, expectedSize)
```

This reduces rehashing during growth.

## Structs

### Composite Literals

Always use field names in composite literals (positional is fragile):

```go
// Good: resilient to field reordering
return &Config{
    Addr:    ":8080",
    Timeout: 30 * time.Second,
}

// Bad: breaks if fields are reordered
return &Config{":8080", 30 * time.Second}
```

### Copying

Structs are values — assigning or passing copies all fields. This is safe unless
the struct contains:
- **Pointers**: the copy shares the pointed-to data
- **Slices/maps**: the copy shares the backing array/hash table
- **sync types**: `sync.Mutex`, `sync.WaitGroup` — must not be copied after use

**Rule**: do not copy a value of type `T` if its methods are on `*T`.

### Struct Tags

```go
type User struct {
    ID        string `json:"id" db:"user_id"`
    Name      string `json:"name"`
    CreatedAt time.Time `json:"created_at,omitempty"`
    Internal  string `json:"-"` // excluded from JSON
}
```

### Compile-Time Interface Satisfaction

```go
var _ fmt.Stringer = (*MyType)(nil)
```

Fails at compile time if `*MyType` doesn't implement `fmt.Stringer`.

# Channels and Patterns

## Channel Basics

```go
ch := make(chan T)      // unbuffered: sender blocks until receiver reads
ch := make(chan T, n)   // buffered: sender blocks only when buffer is full
```

- Unbuffered channels combine communication and synchronization.
- Closing a channel broadcasts a zero value to all receivers.
- Sending on a closed channel panics. Receiving from a closed channel returns
  the zero value immediately.

## Pipeline Pattern

Stages connected by channels — each stage is a goroutine that receives from
inbound, transforms, and sends to outbound:

```go
func gen(nums ...int) <-chan int {
    out := make(chan int)
    go func() {
        for _, n := range nums {
            out <- n
        }
        close(out)
    }()
    return out
}

func sq(in <-chan int) <-chan int {
    out := make(chan int)
    go func() {
        for n := range in {
            out <- n * n
        }
        close(out)
    }()
    return out
}

// Usage: pipeline composition
for v := range sq(sq(gen(2, 3))) {
    fmt.Println(v) // 16, 81
}
```

## Fan-Out / Fan-In

**Fan-out**: multiple goroutines read from the same channel to parallelize work.

**Fan-in**: merge multiple channels into one:

```go
func merge(done <-chan struct{}, cs ...<-chan int) <-chan int {
    var wg sync.WaitGroup
    out := make(chan int)
    output := func(c <-chan int) {
        defer wg.Done()
        for n := range c {
            select {
            case out <- n:
            case <-done:
                return
            }
        }
    }
    wg.Add(len(cs))
    for _, c := range cs {
        go output(c)
    }
    go func() { wg.Wait(); close(out) }()
    return out
}
```

## select Statement

`select` blocks until one case can proceed. If multiple are ready, it chooses
uniformly at random.

### Non-Blocking Operations

```go
select {
case msg := <-ch:
    process(msg)
default:
    // ch has nothing ready — do something else
}
```

### Timeout per Operation

```go
select {
case result := <-ch:
    return result
case <-time.After(5 * time.Second):
    return errors.New("timeout")
}
```

### Nil Channel Trick

A send or receive on a `nil` channel blocks forever. Setting a channel variable
to `nil` disables that case in `select`:

```go
var output chan<- Item
if len(pending) > 0 {
    output = outCh // enable send only when there's work
}
select {
case output <- pending[0]:
    pending = pending[1:]
case item := <-inCh:
    pending = append(pending, item)
}
```

## Semaphore with Buffered Channel

Limit concurrent operations:

```go
sem := make(chan struct{}, maxConcurrency)
for _, item := range items {
    sem <- struct{}{} // acquire
    go func(it Item) {
        defer func() { <-sem }() // release
        process(it)
    }(item)
}
// Wait for all:
for i := 0; i < cap(sem); i++ {
    sem <- struct{}{}
}
```

## Channel of Channels (RPC-Style)

Include a reply channel in the request for direct responses:

```go
type Request struct {
    Args       []int
    ResultChan chan int
}

// Client:
req := &Request{Args: args, ResultChan: make(chan int)}
requests <- req
result := <-req.ResultChan

// Server:
for req := range requests {
    req.ResultChan <- compute(req.Args)
}
```

## Channel vs Mutex Decision Guide

| Use Case | Mechanism |
|----------|-----------|
| Transfer ownership of data | Channel |
| Distribute work (fan-out) | Channel |
| Signal events (done, ready) | Channel |
| Protect shared data structure | Mutex |
| Simple counter | sync/atomic |
| Coordinate pipeline stages | Channel |
| Guard multiple related variables | Mutex |

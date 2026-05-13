# Exception Safety

## The Three Guarantees

Every public function should provide one of these:

| Guarantee | Meaning | How to Achieve |
|-----------|---------|---------------|
| **No-throw** | Never throws | `noexcept`, handle errors internally |
| **Strong** | If it throws, state unchanged (commit-or-rollback) | Copy-and-swap, operate on temporaries |
| **Basic** | If it throws, no leaks, invariants maintained | RAII for all resources |

## RAII: Foundation of the Basic Guarantee

```cpp
// WRONG: Leak on exception
void processFile(const std::string& path) {
    char* buf = new char[65536];
    FILE* f = fopen(path.c_str(), "rb");
    parse(buf, f);      // If this throws, buf and f leak
    fclose(f);
    delete[] buf;
}

// CORRECT: RAII handles cleanup on any exit path
void processFile(const std::string& path) {
    auto buf = std::make_unique<char[]>(65536);
    auto f = std::unique_ptr<FILE, decltype(&fclose)>(
        fopen(path.c_str(), "rb"), &fclose);
    parse(buf.get(), f.get());  // Cleanup happens automatically
}
```

**Never own a raw resource across a potentially-throwing call.**

## The Strong Guarantee: Copy-and-Swap

Do all work on a copy. Commit with a `noexcept` swap:

```cpp
class DataStore {
public:
    DataStore& operator=(const DataStore& other) {
        DataStore temp(other);  // Copy — can throw; this untouched
        swap(temp);             // noexcept — commit
        return *this;           // temp (old data) destroyed by RAII
    }

    void swap(DataStore& other) noexcept {
        using std::swap;
        swap(data_, other.data_);
        swap(size_, other.size_);
    }
};
```

### PImpl for Complex Mutations

```cpp
class Config {
    struct Impl { Database db; Cache cache; };
    std::unique_ptr<Impl> impl_;
public:
    void reconfigure(const Settings& s) {
        auto temp = std::make_unique<Impl>(*impl_);  // Deep copy
        temp->db.apply(s);       // May throw — impl_ untouched
        temp->cache.flush(s);    // May throw — impl_ untouched
        std::swap(impl_, temp);  // noexcept commit
    }
};
```

## Transaction Pattern

```cpp
// CORRECT: Prepare everything, then commit atomically
void transfer(Account& from, Account& to, Amount amount) {
    if (from.balance() < amount) throw InsufficientFundsError{};
    Amount new_from = from.balance() - amount;
    Amount new_to = to.balance() + amount;
    from.setBalance(new_from);  // noexcept
    to.setBalance(new_to);      // noexcept
}

// WRONG: Partial state change before potential failure
void transfer_bad(Account& from, Account& to, Amount amount) {
    from.debit(amount);  // Committed — can't undo if next line throws
    to.credit(amount);   // What if this throws?
}
```

## Destructors Must Never Throw

If a destructor throws during stack unwinding, `std::terminate` is called.

```cpp
// CORRECT: Swallow errors; provide separate close() for callers
class Connection {
public:
    ~Connection() noexcept {
        try { if (!closed_) doClose(); }
        catch (...) { /* log but never propagate */ }
    }
    void close() { doClose(); closed_ = true; }  // Can throw
};
```

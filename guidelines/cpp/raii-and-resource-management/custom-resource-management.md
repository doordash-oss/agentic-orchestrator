# Custom Resource Management

## RAII Wrapper Design

Acquire in the constructor, release in the destructor. The C++ runtime
guarantees destructors run on scope exit — whether by normal return or exception.

```cpp
class FileDescriptor {
    int fd_ = -1;
public:
    explicit FileDescriptor(int fd) : fd_(fd) {
        if (fd_ < 0) throw std::system_error(errno, std::generic_category());
    }
    ~FileDescriptor() noexcept { if (fd_ >= 0) ::close(fd_); }

    FileDescriptor(const FileDescriptor&)            = delete;
    FileDescriptor& operator=(const FileDescriptor&) = delete;
    FileDescriptor(FileDescriptor&& o) noexcept      : fd_(std::exchange(o.fd_, -1)) {}
    FileDescriptor& operator=(FileDescriptor&& o) noexcept {
        if (this != &o) { if (fd_ >= 0) ::close(fd_); fd_ = std::exchange(o.fd_, -1); }
        return *this;
    }

    int get() const noexcept { return fd_; }
};
```

**Key rules:**
- Destructor must be `noexcept`
- Delete copy operations for non-duplicable resources
- Move operations transfer ownership using `std::exchange`

## Database Transaction — Exception-Safe RAII

```cpp
class Transaction {
    DBConn* db_;
    bool committed_ = false;
public:
    explicit Transaction(DBConn& db) : db_(&db) { db_->execute("BEGIN"); }
    ~Transaction() noexcept {
        if (!committed_) {
            try { db_->execute("ROLLBACK"); }
            catch (...) { /* must not throw from destructor */ }
        }
    }

    Transaction(const Transaction&) = delete;
    Transaction& operator=(const Transaction&) = delete;

    void commit() { db_->execute("COMMIT"); committed_ = true; }
};

// Usage: automatic rollback on any exception
void transfer_funds(DBConn& db, int from, int to, double amount) {
    auto txn = db.begin_transaction();
    db.execute("UPDATE accounts SET balance = balance - ...");
    db.execute("UPDATE accounts SET balance = balance + ...");
    txn.commit();  // Only commits if both updates succeed
}
```

## `scope_exit` / Scope Guard Pattern

For ad-hoc cleanup where a full RAII class is overkill:

```cpp
template <typename F>
class scope_exit {
    F f_;
    bool active_ = true;
public:
    explicit scope_exit(F&& f) : f_(std::forward<F>(f)) {}
    ~scope_exit() noexcept { if (active_) f_(); }
    scope_exit(const scope_exit&) = delete;
    void release() noexcept { active_ = false; }
};

// Usage
bool write_config(const Config& cfg) {
    auto tmpfile = create_temp_file("config.tmp");
    auto cleanup = scope_exit([&] { remove(tmpfile.c_str()); });

    write_to_file(tmpfile, cfg);
    if (!validate_config(tmpfile)) return false;

    rename(tmpfile.c_str(), "config.yaml");
    cleanup.release();  // Disarm on success
    return true;
}
```

Prefer a named RAII class when the pattern appears more than once.

## Rule of Zero

Write classes that need no user-defined special members. Use RAII members
that manage their own resources:

```cpp
class Widget {
    std::string              name_;
    std::vector<Point>       points_;
    std::unique_ptr<Renderer> renderer_;
    // Compiler generates correct copy/move/destructor automatically
    // unique_ptr makes Widget non-copyable — intentional
};
```

## Rule of Five

When a class directly manages a raw resource, define all five special members:

```cpp
class ManagedBuffer {
    std::byte* data_ = nullptr;
    std::size_t size_ = 0;
public:
    explicit ManagedBuffer(std::size_t n)
        : data_(new std::byte[n]), size_(n) {}

    ~ManagedBuffer() noexcept { delete[] data_; }

    ManagedBuffer(const ManagedBuffer& o)
        : data_(new std::byte[o.size_]), size_(o.size_) {
        std::copy_n(o.data_, size_, data_);
    }

    ManagedBuffer& operator=(ManagedBuffer o) noexcept {  // copy-and-swap
        swap(*this, o);
        return *this;
    }

    ManagedBuffer(ManagedBuffer&& o) noexcept
        : data_(std::exchange(o.data_, nullptr))
        , size_(std::exchange(o.size_, 0)) {}

    friend void swap(ManagedBuffer& a, ManagedBuffer& b) noexcept {
        using std::swap;
        swap(a.data_, b.data_);
        swap(a.size_, b.size_);
    }
};
```

## Exception Safety in RAII

Members are destroyed in reverse construction order. If a constructor throws,
all fully-constructed members are cleaned up automatically:

```cpp
class CompositeResource {
    FileDescriptor log_fd_;   // acquired first
    Socket         conn_;     // acquired second
    ManagedBuffer  buf_;      // acquired third
public:
    CompositeResource(const char* log_path, const sockaddr_in& addr, size_t sz)
        : log_fd_(::open(log_path, O_WRONLY | O_CREAT, 0644))
        , conn_()
        , buf_(sz) {
        conn_.connect(addr);
        // If connect() throws: buf_ not yet constructed,
        // conn_ destructor runs, log_fd_ destructor runs
    }
};
```

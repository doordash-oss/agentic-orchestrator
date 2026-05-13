# Containers and Algorithms

## Container Selection

`std::vector` is the default (Core Guidelines SL.con.2). Use others only with
a concrete reason:

| Container | Choose when... |
|-----------|---------------|
| `vector` | Default; random access; fast iteration |
| `deque` | Frequent push/pop at both ends |
| `list` | Stable iterators; LRU cache; splice operations |
| `map` | Sorted key order; range iteration |
| `unordered_map` | O(1) average lookup; large datasets |
| `set`/`unordered_set` | Unique elements; membership testing |

### `std::array` Over C Arrays

```cpp
std::array<int, 5> arr = {1, 2, 3, 4, 5};
std::sort(arr.begin(), arr.end());
arr.at(2);  // Bounds-checked
static_assert(sizeof(std::array<int,5>) == sizeof(int[5]));  // Zero overhead
```

### Reserve to Avoid Reallocations

```cpp
std::vector<int> v;
v.reserve(n);  // Single allocation
for (int i = 0; i < n; ++i) v.push_back(i);
```

## Prefer STL Algorithms Over Raw Loops

```cpp
// find_if
auto it = std::find_if(v.begin(), v.end(), [](int x) { return x > 3; });

// sort
std::sort(v.begin(), v.end());

// transform
std::transform(v.begin(), v.end(), std::back_inserter(out),
               [](int x) { return x * 2; });

// accumulate
int sum = std::accumulate(v.begin(), v.end(), 0);

// any_of / all_of / none_of
bool has_neg = std::any_of(v.begin(), v.end(), [](int x) { return x < 0; });
```

## Erase-Remove Idiom vs C++20 `std::erase`

```cpp
// Pre-C++20
v.erase(std::remove(v.begin(), v.end(), 2), v.end());
v.erase(std::remove_if(v.begin(), v.end(), pred), v.end());

// C++20: cleaner, single call
std::erase(v, 2);
std::erase_if(v, [](int x) { return x % 2 == 0; });

// Works on associative containers too
std::erase_if(map, [](const auto& p) { return p.second > 1; });
```

## Iterator Invalidation

- **vector**: all iterators invalidated on reallocation; past-insertion-point on insert
- **deque**: all iterators invalidated on push_front/push_back
- **list/map/set**: only erased-element iterator invalidated
- **unordered containers**: may invalidate all on rehash

## Inserter Iterators

```cpp
std::copy(src.begin(), src.end(), std::back_inserter(dst));

std::transform(src.begin(), src.end(), std::back_inserter(squared),
               [](int x) { return x * x; });
```

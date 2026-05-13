# Ranges (C++20)

## Views and Lazy Evaluation

Views are non-owning, lazy ranges. No work occurs until you iterate:

```cpp
auto pipeline = numbers
    | std::views::filter([](int n) { return n % 2 == 0; })
    | std::views::transform([](int n) { return n * n; })
    | std::views::take(3);

for (int n : pipeline) std::print("{} ", n);  // 4 16 36
// Stops after 3 — does not process the rest
```

## View Adaptors

```cpp
auto evens   = v | std::views::filter([](int n) { return n % 2 == 0; });
auto doubled = v | std::views::transform([](int n) { return n * 2; });
auto first5  = v | std::views::take(5);
auto skip3   = v | std::views::drop(3);
auto rev     = v | std::views::reverse;

// keys/values from maps
for (auto& k : m | std::views::keys) std::print("{} ", k);

// split strings
for (auto part : csv | std::views::split(','))
    std::print("{} ", std::string_view(part));

// iota: sequence generator
auto seq = std::views::iota(1, 11);  // 1..10
auto inf = std::views::iota(0);      // Infinite

// C++23: enumerate, zip
for (auto [i, val] : std::views::enumerate(v))
    std::println("[{}] = {}", i, val);
```

## Projections

Apply a transformation before the algorithm's comparator:

```cpp
struct Person { std::string name; int age; };
std::vector<Person> people;

// Sort by member (pointer-to-member as projection)
std::ranges::sort(people, {}, &Person::name);
std::ranges::sort(people, std::ranges::greater{}, &Person::age);

// find_if with projection
auto it = std::ranges::find_if(people,
    [](const std::string& n) { return n.starts_with("A"); },
    &Person::name);
```

## Range Concepts

```
input_range → forward_range → bidirectional_range → random_access_range → contiguous_range
```

```cpp
void print_all(std::ranges::input_range auto const& r) {
    for (const auto& e : r) std::print("{} ", e);
}

void sort_it(std::ranges::random_access_range auto& r) {
    std::ranges::sort(r);
}
```

## Materializing Views: `ranges::to` (C++23)

```cpp
auto result = numbers
    | std::views::filter([](int n) { return n % 2 == 0; })
    | std::views::transform([](int n) { return n * n; })
    | std::ranges::to<std::vector>();

auto unique = v | std::ranges::to<std::set>();
```

## Views vs Owning Ranges

| Views | Owning Ranges |
|-------|---------------|
| Non-owning, lazy | Own elements, eager |
| O(1) copy | O(n) copy |
| No allocation | Heap allocation |
| Must not outlive source | Independent lifetime |
| Ideal for pipelines | Ideal for storage |

Materialize (via `ranges::to`) when you need persistence, random access by index,
or multiple independent iterations.

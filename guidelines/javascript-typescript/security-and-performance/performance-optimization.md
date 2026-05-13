# Performance Optimization

## Bundle Size

Bundle size is the single biggest performance lever for web applications.
Every KB of JavaScript must be downloaded, parsed, and executed.

### Measuring

```bash
# Analyze bundle composition:
npx vite-bundle-visualizer     # Vite
npx webpack-bundle-analyzer    # webpack

# Check package size before installing:
npx bundlephobia lodash        # shows size + dependencies
```

**Target**: keep initial JavaScript payload under 200KB gzipped for critical
pages. Always measure gzipped size — it's what users download.

### Reducing Bundle Size

1. **Tree-shake** — use ESM, named exports, and `sideEffects: false`
2. **Code-split** — dynamic `import()` for non-critical routes/features
3. **Replace heavy dependencies**:
   - `moment` → `date-fns` or native `Intl`
   - `lodash` → `lodash-es` (or native methods)
   - `axios` → native `fetch`
   - `uuid` → `crypto.randomUUID()`
4. **Lazy-load below the fold** — images, charts, modals
5. **Remove unused code** — dead exports, unused polyfills, development-only code

### Track in CI

```json
{
  "scripts": {
    "size": "size-limit",
    "size:check": "size-limit --limit 200kB"
  }
}
```

Fail the build if the bundle exceeds the budget.

## Core Web Vitals

Google's performance metrics that affect search ranking:

| Metric | Target | What It Measures |
|--------|--------|-----------------|
| **LCP** (Largest Contentful Paint) | < 2.5s | How fast main content appears |
| **INP** (Interaction to Next Paint) | < 200ms | How responsive interactions feel |
| **CLS** (Cumulative Layout Shift) | < 0.1 | How much the layout shifts during load |

### Improving LCP

- Preload critical resources: `<link rel="preload" href="hero.webp" as="image">`
- Reduce render-blocking JavaScript — defer non-critical scripts
- Use `loading="eager"` on the LCP image
- Server-side render or pre-render the above-the-fold content

### Improving INP

- Keep main thread work under 50ms per task
- Use `requestAnimationFrame` for visual updates
- Defer non-essential work with `requestIdleCallback` or `scheduler.yield()`
- Move heavy computation to Web Workers

### Improving CLS

- Set explicit `width` and `height` on images and videos
- Reserve space for dynamic content (ads, embeds)
- Avoid inserting content above existing content after load
- Use CSS `content-visibility` for off-screen rendering

## Memory Management

### Preventing Leaks

Common sources of memory leaks in JavaScript:

```typescript
// Leak: event listener not cleaned up
element.addEventListener("click", handler);
// Fix: remove on cleanup
element.removeEventListener("click", handler);
// Or use AbortController:
element.addEventListener("click", handler, { signal });

// Leak: closures holding references
function setup() {
  const heavyData = loadLargeDataset();
  return () => heavyData.length; // heavyData can never be GC'd
}

// Leak: growing collections without bounds
const cache = new Map();
// Fix: use LRU cache or WeakMap
```

### React-Specific

```typescript
useEffect(() => {
  const controller = new AbortController();
  fetchData({ signal: controller.signal });

  return () => controller.abort(); // cleanup on unmount
}, []);
```

## Rendering Performance

### Avoid Layout Thrashing

```typescript
// Bad — read/write interleaved forces multiple layouts:
for (const el of elements) {
  const height = el.offsetHeight;    // read (forces layout)
  el.style.height = `${height}px`;   // write (invalidates layout)
}

// Good — batch reads, then batch writes:
const heights = elements.map((el) => el.offsetHeight);
elements.forEach((el, i) => { el.style.height = `${heights[i]}px`; });
```

### Use `requestAnimationFrame` for Visual Updates

```typescript
function animate() {
  element.style.transform = `translateX(${position}px)`;
  position += speed;
  requestAnimationFrame(animate);
}
requestAnimationFrame(animate);
```

### IntersectionObserver for Lazy Loading

```typescript
const observer = new IntersectionObserver((entries) => {
  for (const entry of entries) {
    if (entry.isIntersecting) {
      loadContent(entry.target);
      observer.unobserve(entry.target);
    }
  }
});

document.querySelectorAll("[data-lazy]").forEach((el) => observer.observe(el));
```

## JavaScript Engine Optimization

### Keep Objects Monomorphic

V8 optimizes for consistent object shapes:

```typescript
// Good — consistent shape:
function createPoint(x: number, y: number) {
  return { x, y }; // always same shape
}

// Bad — varying shapes:
function createPoint(x: number, y?: number) {
  const point: any = { x };
  if (y !== undefined) point.y = y; // different shapes
  return point;
}
```

### Avoid Megamorphic Call Sites

```typescript
// Monomorphic — fast:
function process(items: User[]) {
  return items.map((item) => item.name); // always User type
}

// Megamorphic — slow (after many different types):
function process(items: any[]) {
  return items.map((item) => item.name); // any type, IC becomes megamorphic
}
```

## Best Practices

- **Measure first** — profile before optimizing, use Lighthouse and DevTools
- **Bundle size is the #1 lever** — track it in CI, keep under 200KB gzipped
- **Code-split aggressively** — lazy-load routes, modals, charts, and below-the-fold content
- **Target Web Vitals** — LCP < 2.5s, INP < 200ms, CLS < 0.1
- **Clean up resources** — event listeners, timers, subscriptions, fetch requests
- **Use Web Workers** for CPU-intensive tasks — keep the main thread free
- **Avoid layout thrashing** — batch DOM reads and writes

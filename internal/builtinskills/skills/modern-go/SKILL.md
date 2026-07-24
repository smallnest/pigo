---
name: modern-go
description: Modernize Go code by applying version-appropriate idioms and APIs (gofix-style transformations). Scans go.mod for the Go version, then transforms Go source files to use modern patterns—from Go 1.0 through 1.26+. Use when the user says "现代化","现代Go语言", "地道的", "idiomatic",  "modernize", "modern-go", "update Go code", "gofix", or wants to upgrade Go idioms.
---

# modern-go

Modernize Go source code by applying version-appropriate idioms, APIs, and language features. Works like `go fix` plus additional transformations curated from the Go team's modernize analysis passes and community best practices.

## Usage

Invoke this skill when the user asks to modernize Go code. By default, modernize the entire project; the user may specify a file or directory instead.

When invoked:
1. Detect the project's Go version from `go.mod` (the `go` directive).
2. Find all `.go` files in the target scope (excluding `vendor/`, `.git/`, `testdata/`).
3. For each file, apply **all transformations for versions ≤ the project's Go version**, starting from the oldest to the newest.
4. After all transformations, print a summary of what was changed and what was skipped.

If the user specifies a file or directory, limit the scope to that path.

## Transformation Catalog

Each transformation includes a **Go version** gate—only apply when the project's `go.mod` version ≥ that version. Never apply a transformation that requires a version higher than the project declares.

### Go 1.0+ — `time.Since`

| Before | After |
|---|---|
| `time.Now().Sub(start)` | `time.Since(start)` |

```go
// before
elapsed := time.Now().Sub(start)
// after
elapsed := time.Since(start)
```

### Go 1.8+ — `time.Until`

| Before | After |
|---|---|
| `deadline.Sub(time.Now())` | `time.Until(deadline)` |

```go
// before
remaining := deadline.Sub(time.Now())
// after
remaining := time.Until(deadline)
```

### Go 1.10+ — `strings.Builder` (loop concatenation)

| Before | After |
|---|---|
| `s += item` in a loop | `var b strings.Builder; b.WriteString(item)` |

```go
// before
s := ""
for _, item := range items {
    s += item
}
// after
var b strings.Builder
for _, item := range items {
    b.WriteString(item)
}
s := b.String()
```

Only when `+=` concatenation happens inside a loop.

### Go 1.13+ — `errors.Is`

| Before | After |
|---|---|
| `err == io.EOF` | `errors.Is(err, io.EOF)` |

```go
// before
if err == io.EOF {
    return
}
// after
if errors.Is(err, io.EOF) {
    return
}
```

### Go 1.17+ — `//go:build` constraints (plusbuild)

| Before | After |
|---|---|
| `// +build linux` + `//go:build linux` (both present) | keep only `//go:build linux` |

```go
// before
//go:build linux && amd64
// +build linux,amd64

package foo
// after
//go:build linux && amd64

package foo
```

The `plusbuild` modernizer removes obsolete `// +build` constraint lines once the equivalent `//go:build` line is present (the `//go:build` syntax landed in Go 1.17). Only strip the old line when a matching `//go:build` already exists — never drop the sole constraint.

### Go 1.17+ — `unsafe.Add` / `unsafe.Slice` (unsafefuncs)

| Before | After |
|---|---|
| `unsafe.Pointer(uintptr(ptr) + uintptr(n))` | `unsafe.Add(ptr, n)` |
| `(*[n]T)(unsafe.Pointer(p))[:]` slice construction | `unsafe.Slice(p, n)` |

```go
// before — pointer arithmetic via uintptr
p2 := unsafe.Pointer(uintptr(ptr) + uintptr(offset))
// after
p2 := unsafe.Add(ptr, offset)
```

```go
// before — building a slice from a base pointer
s := (*[1 << 30]byte)(unsafe.Pointer(p))[:n:n]
// after
s := unsafe.Slice(p, n)
```

The `unsafefuncs` modernizer (gopls v0.22.0) rewrites error-prone `uintptr` pointer math into `unsafe.Add` / `unsafe.Slice`, which the compiler and `go vet` understand as GC-safe.

### Go 1.18+ — `any`

| Before | After |
|---|---|
| `interface{}` | `any` |

```go
// before
func decode(v interface{}) error { ... }
// after
func decode(v any) error { ... }
```

### Go 1.18+ — `strings.Cut`

| Before | After |
|---|---|
| `i := strings.Index(s, sep); ... s[:i], s[i+len(sep):]` | `key, val, found := strings.Cut(s, sep)` |

```go
// before
if i := strings.Index(s, "="); i >= 0 {
    key, val := s[:i], s[i+1:]
}
// after
if key, val, found := strings.Cut(s, "="); found {
    ...
}
```

### Go 1.18+ — `bytes.Cut`

| Before | After |
|---|---|
| `i := bytes.Index(b, sep); ... b[:i], b[i+len(sep):]` | `before, after, found := bytes.Cut(b, sep)` |

```go
// before
if i := bytes.Index(b, sep); i >= 0 {
    before, after := b[:i], b[i+len(sep):]
}
// after
before, after, found := bytes.Cut(b, sep)
```

### Go 1.19+ — `fmt.Appendf`

| Before | After |
|---|---|
| `buf = append(buf, fmt.Sprintf(...)...)` | `buf = fmt.Appendf(buf, ...)` |

```go
// before
buf = append(buf, fmt.Sprintf("x=%d", x)...)
// after
buf = fmt.Appendf(buf, "x=%d", x)
```

### Go 1.19+ — Type-safe atomics (atomictypes)

| Before | After |
|---|---|
| `atomic.StoreInt32(&v, 1)` / `atomic.LoadInt32(&v)` | `var v atomic.Int32; v.Store(1); v.Load()` |
| `atomic.AddInt64(&v, 1)` | `var v atomic.Int64; v.Add(1)` |
| `atomic.Value` + type assertion | `atomic.Pointer[T]` |

```go
// before
var ready int32
atomic.StoreInt32(&ready, 1)
if atomic.LoadInt32(&ready) == 1 { ... }

// after
var ready atomic.Int32
ready.Store(1)
if ready.Load() == 1 { ... }
```

```go
// before
var cache atomic.Value
cache.Store(&Config{})
cfg := cache.Load().(*Config)

// after
var cache atomic.Pointer[Config]
cache.Store(&Config{})
cfg := cache.Load()
```

The `atomictypes` modernizer (gopls v0.22.0, `AtomicTypesAnalyzer`) rewrites both the variable declaration and every call site. Typed wrappers (`atomic.Int32/Int64/Uint32/Uint64/Bool/Pointer[T]`) have identical performance but prevent accidental non-atomic access and fix 64-bit alignment crashes on 32-bit architectures.

### Go 1.20+ — `strings.Clone`

| Before | After |
|---|---|
| `string([]byte(s))` | `strings.Clone(s)` |

```go
// before
s2 := string([]byte(s)) // force copy
// after
s2 := strings.Clone(s)
```

### Go 1.20+ — `bytes.Clone`

| Before | After |
|---|---|
| `make([]byte, len(src)); copy(dst, src)` | `bytes.Clone(src)` |

```go
// before
dst := make([]byte, len(src))
copy(dst, src)
// after
dst := bytes.Clone(src)
```

### Go 1.20+ — `strings.CutPrefix` / `strings.CutSuffix`

| Before | After |
|---|---|
| `if strings.HasPrefix(s, p) { s = s[len(p):] }` | `if rest, ok := strings.CutPrefix(s, p); ok { s = rest }` |
| `if strings.HasSuffix(s, sf) { s = s[:len(s)-len(sf)] }` | `if rest, ok := strings.CutSuffix(s, sf); ok { s = rest }` |

```go
// before
if strings.HasPrefix(s, "pre_") {
    s = s[len("pre_"):]
}
// after
if rest, ok := strings.CutPrefix(s, "pre_"); ok {
    s = rest
}
```

```go
// before
if strings.HasSuffix(s, ".txt") {
    s = s[:len(s)-len(".txt")]
}
// after
if rest, ok := strings.CutSuffix(s, ".txt"); ok {
    s = rest
}
```

### Go 1.20+ — `errors.Join`

| Before | After |
|---|---|
| `fmt.Errorf("...: %w: %w", err1, err2)` | `errors.Join(err1, err2)` |

```go
// before
return fmt.Errorf("load config: %w: %w", err1, err2)
// after
return errors.Join(fmt.Errorf("load config"), err1, err2)
```

### Go 1.20+ — `context.WithCancelCause`

| Before | After |
|---|---|
| `ctx, cancel := context.WithCancel(parent)` + bare `cancel()` | `ctx, cancel := context.WithCancelCause(parent)` + `cancel(err)` |

```go
// before
ctx, cancel := context.WithCancel(parent)
// ... somewhere ...
cancel()

// after
ctx, cancel := context.WithCancelCause(parent)
cancel(ErrShutdown)
// caller: context.Cause(ctx) → ErrShutdown
```

### Go 1.21+ — `min` / `max`

| Before | After |
|---|---|
| `if a < b { v = a } else { v = b }` | `v = min(a, b)` |
| `if a > b { v = a } else { v = b }` | `v = max(a, b)` |
| `if x < lo { x = lo }; if x > hi { x = hi }` | `x = min(max(x, lo), hi)` |

```go
// before
lo := a
if b < lo {
    lo = b
}
// after
lo := min(a, b)
```

```go
// before
if x < 0 {
    x = 0
}
if x > 100 {
    x = 100
}
// after
x = min(max(x, 0), 100)
```

### Go 1.21+ — `clear`

| Before | After |
|---|---|
| `for k := range m { delete(m, k) }` | `clear(m)` |
| `for i := range s { s[i] = zero }` | `clear(s)` |

```go
// before
for k := range m {
    delete(m, k)
}
// after
clear(m)
```

```go
// before
for i := range s {
    s[i] = 0
}
// after
clear(s)
```

### Go 1.21+ — `slices` package

| Before | After |
|---|---|
| Manual loop to find element | `slices.Contains(items, target)` |
| Loop returning index or -1 | `slices.Index(items, target)` |
| `sort.Slice(items, func(i,j int) bool { return items[i] < items[j] })` | `slices.SortFunc(items, cmp.Compare)` |
| Max/min finding loop | `slices.Max(items)` / `slices.Min(items)` |
| Reverse swap loop | `slices.Reverse(s)` |
| Remove consecutive duplicates loop | `slices.Compact(s)` |
| `s[:len(s):len(s)]` | `slices.Clip(s)` |
| `make([]T, len(src)); copy(dst, src)` | `slices.Clone(src)` |

```go
// before → after: slices.Contains(items, target)
found := false
for _, x := range items {
    if x == target {
        found = true
        break
    }
}
```

```go
// before → after: slices.Index(items, target)
for i, x := range items {
    if x == target {
        return i
    }
}
return -1
```

```go
// before → after: slices.SortFunc(items, cmp.Compare)
sort.Slice(items, func(i, j int) bool { return items[i] < items[j] })
```

```go
// before → after: slices.Max(items) / slices.Min(items)
max := items[0]
for _, v := range items[1:] {
    if v > max {
        max = v
    }
}
```

```go
// before → after: slices.Reverse(s)
for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
    s[i], s[j] = s[j], s[i]
}
```

```go
// before → after: slices.Compact(s)
i := 0
for j := 1; j < len(s); j++ {
    if s[j] != s[i] {
        i++
        s[i] = s[j]
    }
}
s = s[:i+1]
```

```go
// before → after: slices.Clip(s)
s = s[:len(s):len(s)]
```

```go
// before → after: slices.Clone(src)
dst := make([]T, len(src))
copy(dst, src)
```

Requires importing `"slices"` and `"cmp"` (for `SortFunc`).

### Go 1.21+ — `slices.Delete` / `slices.Insert`

| Before | After |
|---|---|
| `append(s[:i], s[i+1:]...)` | `slices.Delete(s, i, i+1)` |
| `append(s[:i:i], append([]T{x}, s[i:]...)...)` | `slices.Insert(s, i, x)` |

```go
// before — element removal (classic aliasing/leak footgun)
s = append(s[:i], s[i+1:]...)
// after
s = slices.Delete(s, i, i+1)
```

```go
// before — insert at index i
s = append(s[:i], append([]T{v}, s[i:]...)...)
// after
s = slices.Insert(s, i, v)
```

`slices.Delete` zeroes the tail elements to avoid retaining pointers (the manual `append` form leaks). Requires importing `"slices"`.

### Go 1.21+ — `slices.Equal` / `maps.Equal`

| Before | After |
|---|---|
| `reflect.DeepEqual(a, b)` for comparable slices | `slices.Equal(a, b)` |
| `reflect.DeepEqual(m1, m2)` for comparable maps | `maps.Equal(m1, m2)` |

```go
// before
if reflect.DeepEqual(got, want) { ... }   // got, want are []string
// after
if slices.Equal(got, want) { ... }
```

Faster and type-safe, with no reflection. Only for element types that are directly comparable (use `slices.EqualFunc` / `maps.EqualFunc` otherwise). Requires importing `"slices"` or `"maps"`.

### Go 1.21+ — `maps` package

| Before | After |
|---|---|
| Manual loop to copy a map | `maps.Clone(m)` |
| `for k, v := range src { dst[k] = v }` | `maps.Copy(dst, src)` |
| Loop + conditional delete | `maps.DeleteFunc(m, predicate)` |

```go
// before → after: maps.Clone(m)
dst := make(map[K]V)
for k, v := range src {
    dst[k] = v
}
```

```go
// before → after: maps.Copy(dst, src)
for k, v := range src {
    dst[k] = v
}
```

```go
// before → after: maps.DeleteFunc(m, func(k K, v V) bool { return v == 0 })
for k, v := range m {
    if v == 0 {
        delete(m, k)
    }
}
```

Requires importing `"maps"`.

### Go 1.22+ — `slices.Concat` (appendclipped)

| Before | After |
|---|---|
| `append(append([]T(nil), s1...), s2...)` | `slices.Concat(s1, s2)` |
| `append(slices.Clip(s1), s2...)` for a fresh result | `slices.Concat(s1, s2)` |

```go
// before
all := append(append([]int(nil), a...), b...)
// after
all := slices.Concat(a, b)
```

```go
// before — three-way concat
merged := append(append(append([]string(nil), x...), y...), z...)
// after
merged := slices.Concat(x, y, z)
```

The `appendclipped` modernizer replaces nested `append` concatenation of multiple slices with `slices.Concat`, which allocates a fresh, correctly-sized result. `slices.Concat` was added in Go 1.22. Requires importing `"slices"`. Only apply when the pattern builds a new slice (starts from `[]T(nil)` or a clipped base) — not when it appends in place to an existing slice.

### Go 1.21+ — `sync.OnceFunc` / `sync.OnceValue`

| Before | After |
|---|---|
| `var once sync.Once; once.Do(func() { ... })` | `f := sync.OnceFunc(func() { ... }); f()` |
| `sync.Once` + stored result variable | `sync.OnceValue(func() T { return val })` |

```go
// before
var once sync.Once
func init() { once.Do(func() { setup() }) }

// after
var initOnce = sync.OnceFunc(func() { setup() })
```

```go
// before
var once sync.Once
var cfg *Config
func getConfig() *Config {
    once.Do(func() { cfg = loadConfig() })
    return cfg
}
// after
var getConfig = sync.OnceValue(func() *Config { return loadConfig() })
```

### Go 1.21+ — `context.AfterFunc`

| Before | After |
|---|---|
| `go func() { <-ctx.Done(); cleanup() }()` | `stop := context.AfterFunc(ctx, cleanup)` |

```go
// before
go func() {
    <-ctx.Done()
    conn.Close()
}()
// after
stop := context.AfterFunc(ctx, func() { conn.Close() })
```

### Go 1.21+ — `context.WithTimeoutCause` / `WithDeadlineCause`

| Before | After |
|---|---|
| `context.WithTimeout(parent, d)` | `context.WithTimeoutCause(parent, d, err)` |

```go
// before
ctx, cancel := context.WithTimeout(parent, 5*time.Second)
// after
ctx, cancel := context.WithTimeoutCause(parent, 5*time.Second, ErrTimeout)
```

Only apply when a meaningful cause error is available.

### Go 1.22+ — Range over integer

| Before | After |
|---|---|
| `for i := 0; i < n; i++ { ... }` | `for i := range n { ... }` |
| `for i := 0; i < n; i++ { ... }` (i unused) | `for range n { ... }` |

```go
// before
for i := 0; i < len(items); i++ {
    process(i, items[i])
}
// after
for i := range len(items) {
    process(i, items[i])
}
```

```go
// before
for i := 0; i < n; i++ {
    doWork()
}
// after
for range n {
    doWork()
}
```

### Go 1.22+ — Loop variable shadowing removal

| Before | After |
|---|---|
| `for _, x := range items { x := x; ... }` | `for _, x := range items { ... }` |

```go
// before
for _, x := range items {
    x := x          // capture for goroutine
    go func() { use(x) }()
}
// after
for _, x := range items {
    go func() { use(x) }()
}
```

The `x := x` capture idiom is redundant since Go 1.22.

### Go 1.22+ — `cmp.Or`

| Before | After |
|---|---|
| Chain of `if v == "" { v = fallback }` | `v := cmp.Or(val, fallback1, fallback2, ...)` |

```go
// before
name := os.Getenv("NAME")
if name == "" {
    name = os.Getenv("USER")
}
if name == "" {
    name = "anonymous"
}
// after
name := cmp.Or(os.Getenv("NAME"), os.Getenv("USER"), "anonymous")
```

Requires importing `"cmp"`.

### Go 1.22+ — `reflect.TypeFor`

| Before | After |
|---|---|
| `reflect.TypeOf((*T)(nil)).Elem()` | `reflect.TypeFor[T]()` |

```go
// before
t := reflect.TypeOf((*MyType)(nil)).Elem()
// after
t := reflect.TypeFor[MyType]()
```

### Go 1.22+ — Enhanced `http.ServeMux`

| Before | After |
|---|---|
| `mux.HandleFunc("/api/", h)` + manual path parsing | `mux.HandleFunc("GET /api/{id}", h)` + `r.PathValue("id")` |

```go
// before
mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/api/")
    ...
})
// after
mux.HandleFunc("GET /api/{id}", func(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    ...
})
```

### Go 1.22+ — `math/rand/v2`

| Before | After |
|---|---|
| `rand.Intn(n)` | `rand.IntN(n)` |
| `rand.Int63n(n)` / `rand.Int31n(n)` | `rand.Int64N(n)` / `rand.Int32N(n)` |
| `rand.Seed(...)` + global funcs | drop `Seed`; use auto-seeded top-level funcs or `rand.N[T]` |

```go
// before
import "math/rand"
n := rand.Intn(100)
// after
import "math/rand/v2"
n := rand.IntN(100)
```

```go
// before — type-specific bound
d := time.Duration(rand.Int63n(int64(max)))
// after — generic N works for any integer type
d := rand.N(max)
```

`math/rand/v2` (Go 1.22) drops the deprecated global `Seed` (top-level funcs are auto-seeded) and adds generic `rand.N[T]`. **Semantic migration:** the random stream differs from `math/rand`, so do not apply where reproducibility from a fixed seed matters. Flag as a suggestion, not auto-apply.

### Go 1.23+ — Range over function (iterators)

| Before | After |
|---|---|
| Custom `func Walk(yield func(T) bool)` callback traversal | `func Walk() iter.Seq[T]` returning an iterator |

```go
// before — callback-based iteration
func (t *Tree) Each(fn func(v int)) {
    for _, v := range t.values {
        fn(v)
    }
}
// caller: t.Each(func(v int) { use(v) })

// after — standard iterator, usable with range
func (t *Tree) All() iter.Seq[int] {
    return func(yield func(int) bool) {
        for _, v := range t.values {
            if !yield(v) { return }
        }
    }
}
// caller: for v := range t.All() { use(v) }
```

Adopt the `iter.Seq[T]` / `iter.Seq2[K,V]` protocol (Go 1.23) so custom containers compose with `range`, `slices.Collect`, `maps.Keys`, etc. Requires importing `"iter"`. Flag as a suggestion — it reshapes the API surface.

### Go 1.23+ — Iterator helpers

| Before | After |
|---|---|
| `var keys []K; for k := range m { keys = append(keys, k) }` | `slices.Collect(maps.Keys(m))` |
| `var vals []V; for _, v := range m { vals = append(vals, v) }` | `slices.Collect(maps.Values(m))` |

```go
// before
var keys []string
for k := range m {
    keys = append(keys, k)
}
// after
keys := slices.Collect(maps.Keys(m))
```

```go
// before
var vals []int
for _, v := range m {
    vals = append(vals, v)
}
// after
vals := slices.Collect(maps.Values(m))
```

Requires importing `"slices"` and `"maps"`.

### Go 1.23+ — `strings.SplitSeq` / `strings.FieldsSeq`

| Before | After |
|---|---|
| `for _, part := range strings.Split(s, sep)` | `for part := range strings.SplitSeq(s, sep)` |
| `for _, field := range strings.Fields(s)` | `for field := range strings.FieldsSeq(s)` |

```go
// before
for _, part := range strings.Split(line, ",") {
    process(part)
}
// after
for part := range strings.SplitSeq(line, ",") {
    process(part)
}
```

Only when the loop body does not need the index or the full slice.

### Go 1.23+ — `bytes.SplitSeq` / `bytes.FieldsSeq`

| Before | After |
|---|---|
| `for _, part := range bytes.Split(b, sep)` | `for part := range bytes.SplitSeq(b, sep)` |

```go
// before
for _, part := range bytes.Split(data, sep) {
    process(part)
}
// after
for part := range bytes.SplitSeq(data, sep) {
    process(part)
}
```

### Go 1.23+ — `slices.Backward` (slicesbackward)

| Before | After |
|---|---|
| `for i := len(s) - 1; i >= 0; i-- { use(s[i]) }` | `for _, v := range slices.Backward(s) { use(v) }` |
| reverse loop needing the index too | `for i, v := range slices.Backward(s) { ... }` |

```go
// before
for i := len(items) - 1; i >= 0; i-- {
    process(items[i])
}
// after
for _, v := range slices.Backward(items) {
    process(v)
}
```

```go
// before — index still needed
for i := len(items) - 1; i >= 0; i-- {
    fmt.Println(i, items[i])
}
// after
for i, v := range slices.Backward(items) {
    fmt.Println(i, v)
}
```

The `slicesbackward` modernizer (gopls v0.22.0) replaces manual descending-index loops with the `slices.Backward` iterator. Requires importing `"slices"`. **Caveat:** the rewrite preserves exact semantics in normal cases, but do not apply it when the loop body mutates the slice length or the index is used for out-of-band arithmetic — those edge cases can become unsound.

### Go 1.24+ — `t.Context()` in tests

| Before | After |
|---|---|
| `ctx, cancel := context.WithCancel(context.Background()); defer cancel()` | `ctx := t.Context()` |

```go
// before
func TestFetch(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    result := fetch(ctx)
}
// after
func TestFetch(t *testing.T) {
    result := fetch(t.Context())
}
```

### Go 1.24+ — `strings.Lines` / `bytes.Lines`

| Before | After |
|---|---|
| `bufio.Scanner` line loop over a string/buffer | `for line := range strings.Lines(s)` |

```go
// before
sc := bufio.NewScanner(strings.NewReader(s))
for sc.Scan() {
    process(sc.Text())
}
// after
for line := range strings.Lines(s) {
    process(strings.TrimSuffix(line, "\n"))
}
```

`strings.Lines` / `bytes.Lines` (Go 1.24) return line iterators — no Scanner setup, no default 64KB token-size limit. Note the yielded line **retains its trailing `\n`**, unlike `Scanner.Text()`; trim it if the old code relied on stripped lines. Only apply for in-memory strings/buffers, not streaming `io.Reader`s.

### Go 1.24+ — `os.Root` (directory-scoped filesystem access)

| Before | After |
|---|---|
| manual `filepath.Clean` + prefix check to block traversal | `root, _ := os.OpenRoot(dir); root.Open(name)` |

```go
// before — hand-rolled path-traversal guard
p := filepath.Join(base, name)
if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(base)+string(os.PathSeparator)) {
    return errUnsafePath
}
f, err := os.Open(p)
// after — the OS enforces the boundary
root, err := os.OpenRoot(base)
if err != nil { return err }
defer root.Close()
f, err := root.Open(name) // symlinks/".." escaping base are rejected
```

`os.Root` (Go 1.24) confines all operations to a directory tree, rejecting `..` and symlink escapes at the syscall layer — far more robust than string prefix checks. **Security hardening:** flag as a strong suggestion wherever user-controlled paths are joined to a base directory.

### Go 1.24+ — `omitzero` struct tag

| Before | After |
|---|---|
| `json:"field,omitempty"` (for `time.Time`, `time.Duration`, structs, slices, maps) | `json:"field,omitzero"` |

```go
// before
type Config struct {
    Timeout time.Duration `json:"timeout,omitempty"`
    Labels  []string      `json:"labels,omitempty"`
}
// after
type Config struct {
    Timeout time.Duration `json:"timeout,omitzero"`
    Labels  []string      `json:"labels,omitzero"`
}
```

Only for types where `omitempty` fails: `time.Time`, `time.Duration`, structs, slices, maps. Flag as suggestion, not auto-apply.

### Go 1.24+ — `b.Loop()` in benchmarks

| Before | After |
|---|---|
| `for i := 0; i < b.N; i++ { ... }` | `for b.Loop() { ... }` |

```go
// before
func BenchmarkHash(b *testing.B) {
    for i := 0; i < b.N; i++ {
        hash(input)
    }
}
// after
func BenchmarkHash(b *testing.B) {
    for b.Loop() {
        hash(input)
    }
}
```

### Go 1.25+ — `sync.WaitGroup.Go`

| Before | After |
|---|---|
| `wg.Add(1); go func() { defer wg.Done(); fn() }()` | `wg.Go(fn)` |

```go
// before
var wg sync.WaitGroup
for _, item := range items {
    wg.Add(1)
    go func(item Item) {
        defer wg.Done()
        process(item)
    }(item)
}
wg.Wait()
// after
var wg sync.WaitGroup
for _, item := range items {
    wg.Go(func() { process(item) })
}
wg.Wait()
```

### Go 1.26+ — `new` with expressions

| Before | After |
|---|---|
| `v := val; &v` | `new(val)` |
| Helper `func ptr[T any](v T) *T { return &v }` | `new(val)` directly |

```go
// before
timeout := 30
debug := true
cfg := Config{
    Timeout: &timeout,
    Debug:   &debug,
}
// after
cfg := Config{
    Timeout: new(30),
    Debug:   new(true),
}
```

```go
// before
func ptr[T any](v T) *T { return &v }
cfg := Config{Count: ptr(10)}

// after
cfg := Config{Count: new(10)}
```

### Go 1.26+ — `errors.AsType`

| Before | After |
|---|---|
| `var t *T; errors.As(err, &t)` | `t, ok := errors.AsType[*T](err)` |

```go
// before
var pathErr *os.PathError
if errors.As(err, &pathErr) {
    log.Println(pathErr.Path)
}
// after
if pathErr, ok := errors.AsType[*os.PathError](err); ok {
    log.Println(pathErr.Path)
}
```

### Go 1.27+ — Embedded field literals (embedlit)

| Before | After |
|---|---|
| `T{U: U{x: 1}}` (redundant embedded-type specifier) | `T{x: 1}` |

```go
type Base struct {
    ID   int
    Name string
}
type User struct {
    Base
    Age int
}

// before
u := User{
    Base: Base{ID: 1, Name: "alice"},
    Age:  30,
}
// after
u := User{
    ID:   1,
    Name: "alice",
    Age:  30,
}
```

The `embedlit` modernizer (gopls v0.22.0, `EmbedLitAnalyzer`) strips redundant embedded-struct field-type specifiers from composite literals. Go 1.27 lets you initialize promoted fields directly without the nested literal. Only apply when the promoted field names don't collide with the outer struct's own fields.

## Operation Phases

### Phase 1: Detect
Read `go.mod` to extract the Go version (`go 1.xx` line). If no `go.mod` is found, default to `go 1.21`.

### Phase 2: Gather files
Find all `.go` files in the target scope (project root, or user-specified file/directory). Exclude `vendor/`, `.git/`, and `testdata/` directories.

### Phase 3: Apply transformations
For each `.go` file, apply all transformations for versions ≤ the detected Go version. Process files sequentially. For each file:
1. Read the file content.
2. Identify applicable transformations by scanning for the "Before" patterns.
3. Apply each transformation using the Edit tool.
4. Run `goimports -w` (or `gofmt -w`) on the file after all edits.

Never apply a transformation that requires a version higher than the project's Go version.

### Phase 4: Report
Print a summary table showing:
- **File**: path relative to project root
- **Transformations applied**: list of transformation names per file
- **Total files modified** and **total transformations applied**
- **Skipped transformations** (available but not applicable due to version constraints) and their required Go version

## Example Summary Output

```
## Modernization Summary

| File | Transformations |
|---|---|
| main.go | any, strings.Cut, min/max (2 occurrences) |
| pkg/handler.go | range over int (3), slices.Contains, t.Context() |
| pkg/util.go | new(expr) (1), errors.AsType → errors.Is |

**3 files modified, 10 transformations applied**

Skipped (requires higher Go version):
- new(expr): requires go 1.26 (project is go 1.24)
- WaitGroup.Go: requires go 1.25 (project is go 1.24)
```

## Safety Rules

- Never apply transformations that change semantics in edge cases without the user's awareness.
- Do not apply `omitzero` blindly—it changes JSON serialization behavior; flag it as a suggestion instead.
- Treat semantic migrations as suggestions, not auto-applies: `math/rand/v2` (changes the random stream), `iter.Seq` iterators (reshapes the API), `os.Root` (behavior/error-path change). Point them out and let the user opt in.
- When replacing a `bufio.Scanner` loop with `strings.Lines`/`bytes.Lines`, remember the yielded line keeps its trailing `\n`; add a `TrimSuffix` if the old code relied on `Scanner.Text()` semantics.
- Do not apply `strings.SplitSeq` or `bytes.SplitSeq` when the loop body references the index or the full slice elsewhere.
- Do not apply `strings.Builder` if the concatenation happens outside a loop (single `+=` is fine).
- When a transformation requires a new import, ensure the import is added to the file.
- After all edits, run `goimports -w` on each modified file to clean up imports.
- If `goimports` is not available, fall back to `gofmt -w`.
- If the project has no `go.mod`, ask the user for the target Go version before proceeding.

## Automated tooling (Go 1.27+)

Many of these transformations are now shipped as official modernizers in `gopls`/`go fix`. On Go 1.27+ the whole set can be applied across a module with:

```sh
go fix ./...
```

gopls v0.22.0 added four notable passes covered above:

| Modernizer | Min Go | Transformation |
|---|---|---|
| `unsafefuncs` | 1.17 | `uintptr` pointer math → `unsafe.Add` / `unsafe.Slice` |
| `atomictypes` | 1.19 | primitive `atomic.*` funcs → typed `atomic.Int32`/`Pointer[T]` wrappers |
| `slicesbackward` | 1.23 | descending-index loops → `slices.Backward` iterator |
| `embedlit` | 1.27 | redundant embedded-field literals → promoted-field init |

Other modernizers in the same suite that this catalog covers: `minmax`, `efaceany`, `fmtappendf`, `stringscut`, `stringsseq`, `sortslice`/`slicescontains`, `mapsloop`, `stditerators`, `forvar`, `rangeint`, `testingcontext`, `bloop`, `waitgroup`, `newexpr`, `errorsastype`, `appendclipped` (→ `slices.Concat`), and `plusbuild` (→ drop obsolete `// +build`).

To disable an over-eager pass (e.g. `slicesbackward` rewriting loops that mutate the slice), scope the run with `-fixes` or exclude that analyzer in your editor's gopls settings.

# internal/Util

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- `Nothing` (`= struct{}`, for empty RPC args/replies and done channels), `Set[T]` (`map[T]Nothing` with
  `NewSet`/`Add`/`Remove`/`Contains`), the map interface family (`IMap`/`IMutMap`/`ILen`/`IMapLen`/`IMutMapLen`) +
  `Map[K, V]` (plain `map`-backed; Cache aliases `Map` and `IMutMap` as its `Map`/`Cache`), `OrderedMap[K, V]`
  (insertion-ordered, private fields: `Get`/`Set`/`Len`/`All`/`Keys`, `Set` keeping a known key's position,
  `Reorder(keys)` — must be a permutation of the current keys; zero value usable; no delete, no equality — flatten
  `All()` and compare). It implements `GobEncoder`/`GobDecoder` — parallel key/value slices in insertion order —
  because gob skips unexported fields and BuildConfig's `EnvTable` embeds it inside daemon RPC values; an embedder
  inherits that encoding for its whole self, so it must not add fields of its own. `NormalizePath(path)`: the
  cache-key fold, `ToSlash`+`ToLower`, no clean/absolutize. `RelStrictlyBelow(rel)` (a `filepath.Rel` result names a
  proper descendant — not "." and not a `..`-escape; exists because `filepath.IsLocal(".")` is true),
  `AtOrBelow(base, path)` / `StrictlyBelow(base, path)` (`Rel` + the IsLocal predicates folded into one call),
  `AtOrBelowIgnoreUnrelated(base, path)` (a Rel error — different volumes — reports false instead of panicking;
  Store's kept-link target validation), `PathsOverlap(a, b)` (`AtOrBelow` both ways; Build's ext-dep collision
  checks). Pure path math and containers — no fs access.
- **Concurrency** — `DefaultParallelism` (`runtime.NumCPU()`, read at construction), `RoutinePool` (a slot
  semaphore: `Go(task)` blocks until one of `Parallelism()` slots frees, then runs the task on its own goroutine;
  knows nothing of contexts; `n >= 1`, a serial pool is legal; **no** recover — a panicking task kills the process,
  so every caller wraps), and `ForkJoin[V]` over it, built by
  `ForkJoinBuilder[V]().Context(…).Pool(…).Capacity(…).Build()` — fresh, the builder is the default (Background ctx,
  own pool of `DefaultParallelism`, no pre-sizing; `NewForkJoin[V]()` is that bare build); every setter returns a
  modified copy, only `Build` allocates. `Parallelism(n)` sizes the own pool; `Pool(pool)` shares one across
  instances (how a global cap is expressed) — the two are exclusive. `Capacity(n)`, `n >= 0`, pre-sizes the results.
  **The ctx is the first-error cell**: `Build` derives `WithCancelCause` from `Context(ctx)` (`Context(nil)`
  `Require`s). `Stop(err)` is `cancel(err)` (first cause wins), `Stopped()` is `ctx.Err() != nil`, `Err()` is
  `context.Cause` — so cancelling the parent ctx (an ancestor's failure via `parent.Context()`, or an outside
  cancel) stops this instance with the parent's cause, while this instance's own cancel never reaches the parent.
  Contract: **main-thread + workers** — exactly one goroutine, the one that constructed it, calls `Go`, `AddResult`,
  `Results`, and `defer fj.Cleanup()` (deferred **directly**: it is `recover()`-based, a wrapping closure blinds it).
  `Go` throws `Err()` up front, runs the task on the pool, rescues its panic into `Stop` and sends its value to a
  collector goroutine; a `Go` parked on a busy pool waits for its slot regardless of the ctx, bounded by one task. A
  worker observing a cancel bails **silently** — nothing recovers in a pool goroutine. `Results` is single-shot: it
  waits every worker, closes the result channel, waits for the collector, then throws the cause or returns the
  results and cancels with the collected marker. **Cancel discipline**: a derived ctx stays registered in its parent
  until cancelled, so every path cancels — `Results`, `Cleanup`'s normal return (a no-op after `Results`),
  `Cleanup`'s panic path (records, reaps, re-panics the same error). Only the main thread ever closes, always
  **synchronously** — so a worker must never wait on something the main thread provides only after its panic.
  Nesting: an instance created on the main thread while a parent's workers hold slots is fine, even on a shared
  1-slot pool (`TestForkJoin_SharedPoolNestedFromMain`); creating one **inside a worker** of a shared pool deadlocks
  once depth reaches parallelism.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Store](../../Store/CLAUDE.md) — `AtOrBelow`/`StrictlyBelow` **panic** on unrelatable paths (different Windows
  volumes); `AtOrBelowIgnoreUnrelated` is the variant Store's kept-link target validation must use.
- [Watcher](../../Watcher/CLAUDE.md) — `NormalizePath` only lower-cases and forward-slashes: **no** `Clean`, **no**
  absolutize. Callers must hand it already-clean, consistently-rooted paths, or two spellings of one dir land on
  different keys and every lookup is a silent miss. Same trap for NameServer's cwd registry and Build's ext-dep
  dedup.
- [Hashing](../../Hashing/CLAUDE.md) — `hashDir` builds one `ForkJoin[NamedEntry]` per directory with
  `Capacity(len(dirEntries))`, so `0` must remain legal (`TestEmptyDirHash_Constant`). Its file goroutines throw
  contract panics from `HashFile`, which only ForkJoin's worker-side `Rescue` stands between and a process crash;
  every instance of one walk shares the pool its public entry created, with nested instances created on the walker
  while the parent's workers run — safe only because a Hashing worker never calls `hashDir`/`hashLink`. Each child
  level derives from `parent.Context()`, so Hashing's stop rule **is** the ctx tree
  (`TestHashDir_CancelReachesChildWalk`).
- [FilterFiles](../../FilterFiles/CLAUDE.md) — the repo folds path case two incompatible ways: `NormalizePath`'s
  `strings.ToLower` vs FilterFiles' `cases.Fold()` (they disagree on e.g. Turkish dotted İ and the Kelvin sign).
  `Doc/BrainStorms/DirectoryCasing.md` records that FilterFiles is meant to match `NormalizePath` — unresolved, so
  don't "fix" either side in isolation.

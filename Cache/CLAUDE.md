# Cache

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Generic memoization wrappers layering a real getter over a cache backend (Build stacks daemon + per-Builder caches
  with them; Runtime's `getRootConfig`). `Wrap_Bool`/`Wrap0_Bool` (miss = `ok==false`; `0` = no-arg getter),
  `Wrap_Err_Bool` (realGet may error), `Wrap_Def` (miss = zero value of a `comparable` V; a zero from realGet is
  returned **uncached** — it would read back as a miss, so the Set would be waste), `WrapInt` (miss via the
  `Cache[K,V]` interface), `Memoize` (WrapInt over a fresh private `Map`). `Cache[K,V]` = `Get(K)(V,bool)` +
  `Set(K,V)`; `Map[K,V]` is the `map`-backed impl (both alias `Util.IMutMap`/`Util.Map`); `FromMap` adapts an
  existing map. Variadic `cacheName` turns on Info-level hit logging; a second `"no-value"` element logs the key
  only.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [DaemonClient](../DaemonClient/CLAUDE.md) — `Wrap_Def`'s zero-as-miss marker is what lets the daemon's
  `""`-on-miss (and `""`-when-disabled) hash getters drop in as cache tiers. A new hash RPC whose empty string is a
  *legitimate* value silently turns every hit into a miss.
- [Runtime](../Runtime/CLAUDE.md) — struct-valued caches **cannot** use zero-as-miss: a zero `RootConfig` is a
  legitimate value (`ttl = 0` is a sentinel). That is why `getRootConfig` takes `Wrap0_Bool` backed by an explicit
  `Found` on the wire; `Wrap_Def` would make a legitimately-zero project config a permanent miss.
- [Build](../Build/CLAUDE.md) — each `Wrap*` application wraps **outermost**, so the newest tier is consulted first
  and every inner-tier hit primes the outer tiers through their `Set`. That composition alone maintains dirty mode's
  daemon shadow of the on-disk `dirty/∕<dir>` skip hash — and only the stale-shadow direction self-corrects.

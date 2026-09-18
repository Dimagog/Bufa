package Cache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dimagog/bufa/internal/Util"
)

func Wrap_Bool[K comparable, V any](
	realGet func(K) V,
	cacheGet func(K) (V, bool),
	cacheSet func(K, V),
	cacheName ...string,
) func(K) V {
	return func(key K) V {
		if val, ok := cacheGet(key); ok {
			logCacheHit(cacheName, key, val)
			return val
		}
		val := realGet(key)
		cacheSet(key, val)
		return val
	}
}

func Wrap0_Bool[V any](
	realGet func() V,
	cacheGet func() (V, bool),
	cacheSet func(V),
	cacheName ...string,
) func() V {
	return func() V {
		if val, ok := cacheGet(); ok {
			logCacheHit0(cacheName, val)
			return val
		}
		val := realGet()
		cacheSet(val)
		return val
	}
}

func Wrap_Err_Bool[K comparable, V any](
	realGet func(K) (V, error),
	cacheGet func(K) (V, bool),
	cacheSet func(K, V),
	cacheName ...string,
) func(K) (V, error) {
	return func(key K) (V, error) {
		if val, ok := cacheGet(key); ok {
			logCacheHit(cacheName, key, val)
			return val, nil
		}
		val, err := realGet(key)
		if err != nil {
			return val, err
		}
		cacheSet(key, val)
		return val, nil
	}
}

// A zero from realGet is returned but NOT cached — it would read back as a miss, so the Set
// would be pure waste.
func Wrap_Def[K comparable, V comparable](
	realGet func(K) V,
	cacheGet func(K) V,
	cacheSet func(K, V),
	cacheName ...string,
) func(K) V {
	return func(key K) V {
		val := cacheGet(key)
		var notFound V
		if val != notFound {
			logCacheHit(cacheName, key, val)
			return val
		}

		val = realGet(key)
		if val != notFound {
			cacheSet(key, val)
		}
		return val
	}
}

type Cache[K comparable, V any] = Util.IMutMap[K, V]

type Map[K comparable, V any] = Util.Map[K, V]

func WrapInt[K comparable, V any](realGet func(K) V, cache Cache[K, V], cacheName ...string) func(K) V {
	return func(key K) V {
		if val, ok := cache.Get(key); ok {
			logCacheHit(cacheName, key, val)
			return val
		}
		val := realGet(key)
		cache.Set(key, val)
		return val
	}
}

func Memoize[K comparable, V any](realGet func(K) V, cacheName ...string) func(K) V {
	return WrapInt(realGet, Map[K, V]{}, cacheName...)
}

func FromMap[K comparable, V any](m map[K]V) Map[K, V] {
	return Map[K, V](m)
}

func shouldLogCacheHit(cacheName []string) bool {
	return len(cacheName) > 0 && cacheName[0] != "" && slog.Default().Enabled(context.Background(), slog.LevelInfo)
}

func logCacheHit(cacheName []string, key any, value any) {
	if !shouldLogCacheHit(cacheName) {
		return
	}
	if len(cacheName) > 1 && cacheName[1] == "no-value" {
		slog.Info(fmt.Sprintf("Cache '%s' hit:", cacheName[0]), "key", key)
	} else {
		slog.Info(fmt.Sprintf("Cache '%s' hit:", cacheName[0]), "key", key, "value", value)
	}
}

func logCacheHit0(cacheName []string, value any) {
	if !shouldLogCacheHit(cacheName) {
		return
	}
	if len(cacheName) > 1 && cacheName[1] == "no-value" {
		slog.Info(fmt.Sprintf("Cache '%s' hit", cacheName[0]))
	} else {
		slog.Info(fmt.Sprintf("Cache '%s' hit", cacheName[0]), "value", value)
	}
}

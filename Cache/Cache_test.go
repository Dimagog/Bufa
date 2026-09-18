package Cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

func TestWrapBool_MissCallsRealAndStoresThenHitUsesCache(t *testing.T) {
	store := map[string]string{}
	realCalls := 0
	setCalls := 0

	get := Wrap_Bool(
		func(key string) string {
			realCalls++
			return "real:" + key
		},
		func(key string) (string, bool) {
			val, ok := store[key]
			return val, ok
		},
		func(key, val string) {
			setCalls++
			store[key] = val
		},
	)

	if got, want := get("a"), "real:a"; got != want {
		t.Fatalf("first get = %q, want %q", got, want)
	}
	if got, want := store["a"], "real:a"; got != want {
		t.Fatalf("cached value = %q, want %q", got, want)
	}
	if got, want := get("a"), "real:a"; got != want {
		t.Fatalf("second get = %q, want %q", got, want)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}
	if setCalls != 1 {
		t.Errorf("cacheSet calls = %d, want 1", setCalls)
	}
}

func TestWrapBool_CanCacheZeroValue(t *testing.T) {
	store := map[string]int{"zero": 0}
	realCalls := 0

	get := Wrap_Bool(
		func(string) int {
			realCalls++
			return 42
		},
		func(key string) (int, bool) {
			val, ok := store[key]
			return val, ok
		},
		func(key string, val int) {
			store[key] = val
		},
	)

	if got := get("zero"); got != 0 {
		t.Fatalf("cached zero get = %d, want 0", got)
	}
	if realCalls != 0 {
		t.Errorf("realGet calls = %d, want 0", realCalls)
	}
}

func TestWrap0Bool_MissCallsRealAndStoresThenHitUsesCache(t *testing.T) {
	var stored string
	cached := false
	realCalls := 0
	setCalls := 0

	get := Wrap0_Bool(
		func() string {
			realCalls++
			return "real"
		},
		func() (string, bool) {
			return stored, cached
		},
		func(val string) {
			setCalls++
			stored = val
			cached = true
		},
	)

	if got, want := get(), "real"; got != want {
		t.Fatalf("first get = %q, want %q", got, want)
	}
	if got, want := stored, "real"; got != want {
		t.Fatalf("cached value = %q, want %q", got, want)
	}
	if got, want := get(), "real"; got != want {
		t.Fatalf("second get = %q, want %q", got, want)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}
	if setCalls != 1 {
		t.Errorf("cacheSet calls = %d, want 1", setCalls)
	}
}

func TestWrap0Bool_CanCacheZeroValue(t *testing.T) {
	realCalls := 0

	get := Wrap0_Bool(
		func() int {
			realCalls++
			return 42
		},
		func() (int, bool) {
			return 0, true
		},
		func(int) {},
	)

	if got := get(); got != 0 {
		t.Fatalf("cached zero get = %d, want 0", got)
	}
	if realCalls != 0 {
		t.Errorf("realGet calls = %d, want 0", realCalls)
	}
}

func TestWrap0Bool_NamedHitLogsInfoRecordWithoutKey(t *testing.T) {
	h := &captureHandler{level: slog.LevelInfo}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })

	get := Wrap0_Bool(
		func() string {
			t.Fatal("realGet called on hit")
			return ""
		},
		func() (string, bool) {
			return "cached", true
		},
		func(string) {},
		"Named0",
	)

	if got := get(); got != "cached" {
		t.Fatalf("get = %q, want cached", got)
	}
	if len(h.records) != 1 {
		t.Fatalf("log record count = %d, want 1", len(h.records))
	}
	r := h.records[0]
	if r.Message != "Cache 'Named0' hit" {
		t.Errorf("log message = %q, want cache hit message", r.Message)
	}
	attrs := recordAttrs(r)
	if _, ok := attrs["key"]; ok {
		t.Errorf("keyless cache hit should not log a key attr, got %v", attrs["key"].Any())
	}
	if got := fmt.Sprint(attrs["value"].Any()); got != "cached" {
		t.Errorf("log value attr = %q, want cached", got)
	}
}

func TestWrapErrBool_MissSuccessStoresReturnedValue(t *testing.T) {
	store := map[string]int{}
	realCalls := 0
	setCalls := 0

	get := Wrap_Err_Bool(
		func(key string) (int, error) {
			realCalls++
			return len(key), nil
		},
		func(key string) (int, bool) {
			val, ok := store[key]
			return val, ok
		},
		func(key string, val int) {
			setCalls++
			store[key] = val
		},
	)

	got, err := get("abcd")
	if err != nil {
		t.Fatalf("first get error: %v", err)
	}
	if got != 4 {
		t.Fatalf("first get = %d, want 4", got)
	}
	if store["abcd"] != 4 {
		t.Fatalf("cached value = %d, want 4", store["abcd"])
	}

	got, err = get("abcd")
	if err != nil {
		t.Fatalf("second get error: %v", err)
	}
	if got != 4 {
		t.Fatalf("second get = %d, want 4", got)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}
	if setCalls != 1 {
		t.Errorf("cacheSet calls = %d, want 1", setCalls)
	}
}

func TestWrapErrBool_ErrorIsReturnedAndNotCached(t *testing.T) {
	wantErr := errors.New("boom")
	setCalls := 0

	get := Wrap_Err_Bool(
		func(string) (string, error) {
			return "partial", wantErr
		},
		func(string) (string, bool) {
			return "", false
		},
		func(string, string) {
			setCalls++
		},
	)

	got, err := get("x")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != "partial" {
		t.Fatalf("value on error = %q, want partial", got)
	}
	if setCalls != 0 {
		t.Errorf("cacheSet calls = %d, want 0", setCalls)
	}
}

func TestWrapErrBool_HitReturnsCachedValueWithoutReal(t *testing.T) {
	realCalls := 0

	get := Wrap_Err_Bool(
		func(string) (int, error) {
			realCalls++
			return 0, errors.New("should not be called")
		},
		func(string) (int, bool) {
			return 0, true
		},
		func(string, int) {},
	)

	got, err := get("zero")
	if err != nil {
		t.Fatalf("hit error: %v", err)
	}
	if got != 0 {
		t.Fatalf("hit value = %d, want cached zero", got)
	}
	if realCalls != 0 {
		t.Errorf("realGet calls = %d, want 0", realCalls)
	}
}

func TestWrapDef_NonZeroCacheHitSkipsReal(t *testing.T) {
	realCalls := 0

	get := Wrap_Def(
		func(string) string {
			realCalls++
			return "real"
		},
		func(string) string {
			return "cached"
		},
		func(string, string) {
			t.Fatal("cacheSet called on hit")
		},
	)

	if got := get("k"); got != "cached" {
		t.Fatalf("get = %q, want cached", got)
	}
	if realCalls != 0 {
		t.Errorf("realGet calls = %d, want 0", realCalls)
	}
}

func TestWrapDef_ZeroCacheValueMissesAndStores(t *testing.T) {
	store := map[string]string{}
	realCalls := 0

	get := Wrap_Def(
		func(key string) string {
			realCalls++
			return "real:" + key
		},
		func(key string) string {
			return store[key]
		},
		func(key, val string) {
			store[key] = val
		},
	)

	if got, want := get("k"), "real:k"; got != want {
		t.Fatalf("get = %q, want %q", got, want)
	}
	if got, want := store["k"], "real:k"; got != want {
		t.Fatalf("cached value = %q, want %q", got, want)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}
}

func TestWrapDef_CannotCacheZeroValue(t *testing.T) {
	realCalls := 0

	get := Wrap_Def(
		func(string) int {
			realCalls++
			return 0
		},
		func(string) int {
			return 0
		},
		func(string, int) {
			t.Fatal("cacheSet called with the zero value (would read back as a miss)")
		},
	)

	if got := get("k"); got != 0 {
		t.Fatalf("first get = %d, want 0", got)
	}
	if got := get("k"); got != 0 {
		t.Fatalf("second get = %d, want 0", got)
	}
	if realCalls != 2 {
		t.Errorf("realGet calls = %d, want 2 because zero means miss", realCalls)
	}
}

func TestWrapInt_UsesCacheInterface(t *testing.T) {
	cache := FromMap(map[string]int{"hit": 0})
	realCalls := 0

	get := WrapInt(
		func(key string) int {
			realCalls++
			return len(key)
		},
		cache,
	)

	if got := get("hit"); got != 0 {
		t.Fatalf("hit = %d, want cached zero", got)
	}
	if realCalls != 0 {
		t.Fatalf("realGet calls after hit = %d, want 0", realCalls)
	}

	if got := get("miss"); got != 4 {
		t.Fatalf("miss = %d, want 4", got)
	}
	if got, ok := cache.Get("miss"); !ok || got != 4 {
		t.Fatalf("cache.Get(miss) = %d, %v; want 4, true", got, ok)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}
}

func TestMemoize_CachesRepeatCalls(t *testing.T) {
	realCalls := 0

	get := Memoize(func(key string) int {
		realCalls++
		return len(key)
	})

	if got := get("abc"); got != 3 {
		t.Fatalf("first get = %d, want 3", got)
	}
	if got := get("abc"); got != 3 {
		t.Fatalf("second get = %d, want 3", got)
	}
	if realCalls != 1 {
		t.Errorf("realGet calls = %d, want 1", realCalls)
	}

	if got := get("wxyz"); got != 4 {
		t.Fatalf("new key = %d, want 4", got)
	}
	if realCalls != 2 {
		t.Errorf("realGet calls = %d, want 2", realCalls)
	}
}

func TestMap_GetSetAndFromMapShareBackingMap(t *testing.T) {
	raw := map[string]int{"a": 1}
	cache := FromMap(raw)

	if got, ok := cache.Get("a"); !ok || got != 1 {
		t.Fatalf("Get(a) = %d, %v; want 1, true", got, ok)
	}
	if got, ok := cache.Get("missing"); ok || got != 0 {
		t.Fatalf("Get(missing) = %d, %v; want 0, false", got, ok)
	}

	cache.Set("b", 2)
	if raw["b"] != 2 {
		t.Fatalf("raw map was not updated by Set: raw[b] = %d, want 2", raw["b"])
	}

	raw["c"] = 3
	if got, ok := cache.Get("c"); !ok || got != 3 {
		t.Fatalf("cache does not share raw backing map: Get(c) = %d, %v; want 3, true", got, ok)
	}
}

func TestLogCacheHit_GatesOnNameAndLevel(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })

	logsAt := func(level slog.Level, cacheName []string) int {
		h := &captureHandler{level: level}
		slog.SetDefault(slog.New(h))
		logCacheHit(cacheName, "k", "v")
		return len(h.records)
	}

	if got := logsAt(slog.LevelInfo, []string{"named"}); got != 1 {
		t.Errorf("info logger with non-empty cache name records = %d, want 1", got)
	}
	if got := logsAt(slog.LevelInfo, nil); got != 0 {
		t.Errorf("nil cache name records = %d, want 0", got)
	}
	if got := logsAt(slog.LevelInfo, []string{""}); got != 0 {
		t.Errorf("empty cache name records = %d, want 0", got)
	}
	if got := logsAt(slog.LevelWarn, []string{"named"}); got != 0 {
		t.Errorf("logger above info records = %d, want 0", got)
	}
}

func TestNamedCacheHitLogsInfoRecord(t *testing.T) {
	h := &captureHandler{level: slog.LevelInfo}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })

	get := Wrap_Bool(
		func(string) string {
			t.Fatal("realGet called on hit")
			return ""
		},
		func(string) (string, bool) {
			return "cached", true
		},
		func(string, string) {},
		"Named",
	)

	if got := get("k"); got != "cached" {
		t.Fatalf("get = %q, want cached", got)
	}
	if len(h.records) != 1 {
		t.Fatalf("log record count = %d, want 1", len(h.records))
	}
	r := h.records[0]
	if r.Level != slog.LevelInfo {
		t.Errorf("log level = %v, want Info", r.Level)
	}
	if r.Message != "Cache 'Named' hit:" {
		t.Errorf("log message = %q, want cache hit message", r.Message)
	}
	attrs := recordAttrs(r)
	if got := fmt.Sprint(attrs["key"].Any()); got != "k" {
		t.Errorf("log key attr = %q, want k", got)
	}
	if got := fmt.Sprint(attrs["value"].Any()); got != "cached" {
		t.Errorf("log value attr = %q, want cached", got)
	}
}

type captureHandler struct {
	level   slog.Level
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func recordAttrs(r slog.Record) map[string]slog.Value {
	attrs := make(map[string]slog.Value)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	return attrs
}

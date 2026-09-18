package Util

type IMap[K comparable, V any] interface {
	Get(key K) (V, bool)
}

type IMutMap[K comparable, V any] interface {
	IMap[K, V]
	Set(key K, value V)
}

type ILen interface {
	Len() int
}

type IMapLen[K comparable, V any] interface {
	IMap[K, V]
	ILen
}

type IMutMapLen[K comparable, V any] interface {
	IMutMap[K, V]
	ILen
}

// compile-time assertion that Map implements interfaces
var _ IMutMapLen[string, int] = Map[string, int]{}

type Map[K comparable, V any] map[K]V

func (m Map[K, V]) Get(key K) (V, bool) {
	val, ok := m[key]
	return val, ok
}

func (m Map[K, V]) Set(key K, value V) {
	m[key] = value
}

func (m Map[K, V]) Len() int {
	return len(m)
}

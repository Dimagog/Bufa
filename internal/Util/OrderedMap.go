package Util

import (
	"bytes"
	"encoding/gob"
	"iter"
	"slices"

	c "github.com/dimagog/bufa/internal/contract"
)

// compile-time assertion that OrderedMap implements interface
var _ IMutMapLen[string, int] = &OrderedMap[string, int]{}

// Insertion-ordered map; the zero value is empty and usable.
type OrderedMap[K comparable, V any] struct {
	values map[K]V
	keys   []K // insertion order
}

func NewOrderedMap[K comparable, V any](capacity int) OrderedMap[K, V] {
	return OrderedMap[K, V]{values: make(map[K]V, capacity), keys: make([]K, 0, capacity)}
}

func (m OrderedMap[K, V]) Len() int {
	return len(m.keys)
}

func (m OrderedMap[K, V]) Get(key K) (V, bool) {
	value, found := m.values[key]
	return value, found
}

// A known key keeps its position; a new one is appended.
func (m *OrderedMap[K, V]) Set(key K, value V) {
	if m.values == nil {
		m.values = make(map[K]V)
	}
	if _, found := m.values[key]; !found {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// keys must be a permutation of the current keys; the map itself is untouched.
// Checks are length + membership only — a duplicated key slips through and corrupts the order.
func (m *OrderedMap[K, V]) Reorder(keys []K) {
	keys = slices.Clone(keys)
	m.ReorderUnsafe(keys)
}

// Takes ownership of keys — the caller must not retain or mutate the slice.
func (m *OrderedMap[K, V]) ReorderUnsafe(keys []K) {
	c.Require(len(keys) == len(m.keys), "Reorder: %d keys for a map of %d", len(keys), len(m.keys))
	for _, key := range keys {
		_, found := m.values[key]
		c.Require(found, "Reorder: key %v is not in the map", key)
	}
	m.keys = keys
}

// Insertion order. Set of an existing key is safe during iteration — it never mutates keys.
func (m OrderedMap[K, V]) Keys() iter.Seq[K] {
	return slices.Values(m.keys)
}

// Insertion order. Set of an existing key is safe during iteration — it never mutates keys.
func (m OrderedMap[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for _, key := range m.keys {
			if !yield(key, m.values[key]) {
				return
			}
		}
	}
}

// gob skips unexported fields: the wire form is two parallel slices in insertion order.
type orderedMapWire[K comparable, V any] struct {
	Keys   []K
	Values []V
}

func (m OrderedMap[K, V]) GobEncode() ([]byte, error) {
	wire := orderedMapWire[K, V]{Keys: m.keys, Values: make([]V, 0, len(m.keys))}
	for _, key := range m.keys {
		wire.Values = append(wire.Values, m.values[key])
	}
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(wire)
	return buf.Bytes(), err
}

func (m *OrderedMap[K, V]) GobDecode(data []byte) error {
	var wire orderedMapWire[K, V]
	err := gob.NewDecoder(bytes.NewReader(data)).Decode(&wire)
	if err == nil {
		*m = NewOrderedMap[K, V](len(wire.Keys))
		for i, key := range wire.Keys {
			m.Set(key, wire.Values[i])
		}
	}
	return err
}

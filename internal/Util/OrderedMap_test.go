package Util

import (
	"bytes"
	"encoding/gob"
	"maps"
	"slices"
	"strings"
	"testing"

	c "github.com/dimagog/bufa/internal/contract"
)

func TestOrderedMap_SetKeepsPosition(t *testing.T) {
	var m OrderedMap[string, int]
	m.Set("b", 1)
	m.Set("a", 2)
	m.Set("b", 3)
	var keys []string
	var values []int
	for k, v := range m.All() {
		keys = append(keys, k)
		values = append(values, v)
	}
	if !slices.Equal(keys, []string{"b", "a"}) || !slices.Equal(values, []int{3, 2}) {
		t.Errorf("All = %v/%v, want [b a]/[3 2]", keys, values)
	}
	if got := slices.Collect(m.Keys()); !slices.Equal(got, keys) {
		t.Errorf("Keys = %v, want %v", got, keys)
	}
	if v, ok := m.Get("b"); !ok || v != 3 || m.Len() != 2 {
		t.Errorf("Get(b) = %d, %v; Len = %d", v, ok, m.Len())
	}
	if _, ok := m.Get("c"); ok {
		t.Error("Get(c) hit")
	}
}

func TestOrderedMap_Reorder(t *testing.T) {
	var m OrderedMap[string, int]
	m.Set("b", 1)
	m.Set("a", 2)
	m.Reorder([]string{"a", "b"})
	if got := slices.Collect(m.Keys()); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("Keys = %v after Reorder, want [a b]", got)
	}
	if v, _ := m.Get("b"); v != 1 {
		t.Errorf("Get(b) = %d after Reorder, want the value untouched", v)
	}
	for name, keys := range map[string][]string{"short": {"a"}, "long": {"a", "b", "c"}, "foreign": {"a", "c"}} {
		err := c.Rescue(func() { m.Reorder(keys) })
		if err == nil || !strings.Contains(err.Error(), "Reorder") {
			t.Errorf("%s: err = %v, want a Reorder precondition failure", name, err)
		}
	}
}

func TestOrderedMap_GobRoundTrip(t *testing.T) {
	type holder struct{ M OrderedMap[string, int] }
	in := holder{}
	in.M.Set("z", 1)
	in.M.Set("a", 2)
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(in); err != nil {
		t.Fatal(err)
	}
	var out holder
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.M.keys, []string{"z", "a"}) || !maps.Equal(out.M.values, in.M.values) {
		t.Errorf("round trip = %v/%v, want %v/%v", out.M.keys, out.M.values, in.M.keys, in.M.values)
	}

	buf.Reset()
	if err := gob.NewEncoder(&buf).Encode(holder{}); err != nil {
		t.Fatal(err)
	}
	var zero holder
	if err := gob.NewDecoder(&buf).Decode(&zero); err != nil || zero.M.Len() != 0 {
		t.Errorf("zero value round trip: err=%v Len=%d", err, zero.M.Len())
	}
}

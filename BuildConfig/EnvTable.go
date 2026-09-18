package BuildConfig

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

type EnvValue interface{ string | EnvVar }

// An [env] table: name → value in document order — the order a script-env export and its ${NAME} visibility
// follow. UnmarshalTOML gets an unordered map, so a decode gateway must orderByDocument it before use.
type EnvTable[V EnvValue] struct {
	Util.OrderedMap[string, V]
}

func (t *EnvTable[V]) UnmarshalTOML(data any) error {
	table, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf("[env] must be a table, got %T", data)
	}
	t.OrderedMap = Util.NewOrderedMap[string, V](len(table))
	for _, name := range slices.Sorted(maps.Keys(table)) {
		var value V
		if err := decodeEnvValue(&value, table[name]); err != nil {
			return fmt.Errorf("[env] variable '%s': %w", name, err)
		}
		t.Set(name, value)
	}
	return nil
}

func decodeEnvValue[V EnvValue](v *V, raw any) error {
	var err error
	if target, isVar := any(v).(*EnvVar); isVar {
		err = target.UnmarshalTOML(raw)
	} else if literal, ok := raw.(string); ok {
		*any(v).(*string) = literal
	} else {
		err = fmt.Errorf("must be a literal string, got %T", raw)
	}
	return err
}

// The platform fold: a section entry overrides its top-level namesake in place, a new name follows the top-level ones.
func (t *EnvTable[V]) override(section EnvTable[V]) {
	for name, value := range section.All() {
		t.Set(name, value)
	}
}

// UnmarshalTOML's map lost the document order; the decode metadata still has it — rebuild in that order.
func (t *EnvTable[V]) orderByDocument(keys []toml.Key, tablePath ...string) {
	names := make([]string, 0, t.Len())
	for _, key := range keys {
		if len(key) == len(tablePath)+1 && slices.EqualFunc(key[:len(tablePath)], tablePath, strings.EqualFold) {
			names = append(names, key[len(tablePath)])
		}
	}
	// Decoder EqualFold-matches case-variant tables ([env] + [ENV]) to one field, each resetting the table.
	c.Require(len(names) == t.Len(), "[%s] table declared more than once (case-variant spellings)",
		strings.Join(tablePath, "."))
	t.ReorderUnsafe(names)
}

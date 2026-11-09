package receipts

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeUnion struct {
	Value any
	Tag   string
}

type fakeInterval struct {
	Days   int32
	Months int32
	Micros int64
}

type hidden struct {
	Public int
	secret string
}

type namedString string

func TestResultHasher_ReflectedValuesDoNotCollide(t *testing.T) {
	one := []Column{{Name: "v", Type: "T"}}
	h := func(v any) string { return sum(t, testKey, one, []any{v}) }

	// Maps: %v does not delimit, so these used to collide.
	assert.NotEqual(t, h(map[string]string{"a": "b", "c": "d"}), h(map[string]string{"a": "b c:d"}))
	assert.NotEqual(t, h(map[string]any{"a": nil}), h(map[string]any{"a": "<nil>"}))
	assert.NotEqual(t, h(map[string]any{"a": nil}), h(map[string]any{}))
	assert.Equal(t, h(map[string]int{"a": 1, "b": 2, "c": 3}), h(map[string]int{"c": 3, "b": 2, "a": 1}), "entry order is not part of a map")
	assert.NotEqual(t, h(map[int]string{1: "a"}), h(map[string]string{"1": "a"}), "key types")
	assert.Equal(t, h(map[string]any{"k": int64(1)}), h(map[string]int64{"k": 1}), "one encoding for a map of the same content")

	// Unions and other structs.
	assert.NotEqual(t, h(fakeUnion{Value: nil, Tag: "a"}), h(fakeUnion{Value: "<nil>", Tag: "a"}))
	assert.NotEqual(t, h(fakeUnion{Value: "x", Tag: "a b"}), h(fakeUnion{Value: "x a", Tag: "b"}))
	assert.NotEqual(t, h(fakeUnion{Value: int64(1), Tag: "a"}), h(fakeUnion{Value: int64(2), Tag: "a"}))
	assert.NotEqual(t, h(fakeInterval{Days: 1}), h(fakeInterval{Months: 1}))
	assert.NotEqual(t, h(hidden{Public: 1, secret: "a"}), h(hidden{Public: 1, secret: "b"}), "unexported fields still count")

	// Pointers, lists, bytes, big integers, named scalars.
	v := int64(5)
	assert.Equal(t, h(&v), h(int64(5)))
	assert.Equal(t, h((*int64)(nil)), h(nil))
	assert.NotEqual(t, h([]string{"a", "b"}), h([]string{"ab"}))
	assert.NotEqual(t, h([2]string{"a", "b"}), h([2]string{"a", "c"}))
	assert.Equal(t, h([4]byte{1, 2, 3, 4}), h([]byte{1, 2, 3, 4}))
	assert.NotEqual(t, h(big.NewInt(1)), h(big.NewInt(2)), "a big integer has no exported fields")
	assert.Equal(t, h(namedString("x")), h("x"))
	assert.NotEqual(t, h(complex(1, 2)), h(complex(1, 3)))

	// A pointer cycle terminates.
	type node struct{ Next *node }
	n := &node{}
	n.Next = n
	assert.True(t, IsHash(h(n)))
}

func TestResultHasher_TiedMapKeysAndNestedTimes(t *testing.T) {
	key := make([]byte, 32)
	sum := func(v any) string {
		h := NewResultHasher(key, []Column{{Name: "m", Type: "MAP"}})
		if err := h.AddRow([]any{v}); err != nil {
			t.Fatal(err)
		}
		return h.Sum()
	}
	tied := map[any]any{int32(1): "a", int64(1): "b", math.NaN(): "c", math.NaN(): "d"}
	first := sum(tied)
	for range 50 {
		if got := sum(tied); got != first {
			t.Fatalf("tied keys hash by iteration order: %s != %s", got, first)
		}
	}
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	plus1 := at.In(time.FixedZone("+01", 3600))
	if sum(map[any]any{"t": at}) != sum(map[any]any{"t": plus1}) {
		t.Fatal("a nested time hashes by its zone")
	}
}

package receipts

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"time"
)

// maxDepth bounds the nesting appendReflect follows (a pointer cycle).
const maxDepth = 32

var (
	stringerType = reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	timeType     = reflect.TypeOf(time.Time{})
)

// appendReflect encodes a value the type switch did not name, by its kind, so
// every part is tagged and delimited: a map is its entries sorted by their
// encoded keys, a struct (a DuckDB union, an interval and the like) its
// exported fields by name, a pointer its target (nil is NULL), a non-byte slice
// or array a list. A value with a String method (a big integer, a decimal) or
// an unexported field is also covered by its length-prefixed text. The last
// resort is the Go type name and the length-prefixed %v, never an undelimited
// %v on its own.
func appendReflect(b []byte, rv reflect.Value, depth int) []byte {
	if depth > maxDepth {
		return appendString(append(b, tagOther), "depth")
	}
	switch rv.Kind() {
	case reflect.Invalid:
		return append(b, tagNull)
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return append(b, tagNull)
		}
		if rv.Kind() == reflect.Pointer && rv.Type().Implements(stringerType) {
			return appendText(b, rv)
		}
		return appendReflect(b, rv.Elem(), depth+1)
	}
	if !rv.CanInterface() {
		return appendText(b, rv)
	}
	if rv.Type() == timeType {
		// The same instant encodes the same nested as at the top level.
		return appendValue(b, rv.Interface())
	}
	if rv.Kind() != reflect.Struct && rv.Type().Implements(stringerType) {
		return appendText(b, rv)
	}
	switch rv.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.String:
		return appendValue(b, rv.Convert(scalarType(rv.Kind())).Interface())
	case reflect.Slice, reflect.Array:
		return appendSequence(b, rv, depth)
	case reflect.Map:
		return appendMap(b, rv, depth)
	case reflect.Struct:
		return appendStruct(b, rv, depth)
	}
	return appendText(b, rv)
}

func scalarType(k reflect.Kind) reflect.Type {
	switch k {
	case reflect.Bool:
		return reflect.TypeOf(false)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.TypeOf(int64(0))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.TypeOf(uint64(0))
	case reflect.Float32, reflect.Float64:
		return reflect.TypeOf(float64(0))
	}
	return reflect.TypeOf("")
}

// appendText is the value's Go type and its length-prefixed text.
func appendText(b []byte, rv reflect.Value) []byte {
	b = appendString(append(b, tagOther), rv.Type().String())
	if rv.CanInterface() {
		return appendString(b, fmt.Sprintf("%v", rv.Interface()))
	}
	return appendString(b, fmt.Sprintf("%v", rv))
}

func appendSequence(b []byte, rv reflect.Value, depth int) []byte {
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		raw := make([]byte, rv.Len())
		for i := range raw {
			raw[i] = byte(rv.Index(i).Uint())
		}
		return appendString(append(b, tagBytes), string(raw))
	}
	b = appendUint(append(b, tagList), uint64(rv.Len()))
	for i := 0; i < rv.Len(); i++ {
		b = appendReflect(b, rv.Index(i), depth+1)
	}
	return b
}

func appendMap(b []byte, rv reflect.Value, depth int) []byte {
	if rv.IsNil() {
		return append(b, tagNull)
	}
	type entry struct{ k, v []byte }
	entries := make([]entry, 0, rv.Len())
	it := rv.MapRange()
	for it.Next() {
		entries = append(entries, entry{
			k: appendReflect(nil, it.Key(), depth+1),
			v: appendReflect(nil, it.Value(), depth+1),
		})
	}
	// Keys that encode alike (int32(1) and int64(1), two NaNs) are ordered by
	// their values, so the order never depends on map iteration.
	sort.Slice(entries, func(i, j int) bool {
		if c := bytes.Compare(entries[i].k, entries[j].k); c != 0 {
			return c < 0
		}
		return bytes.Compare(entries[i].v, entries[j].v) < 0
	})
	b = appendUint(append(b, tagMap), uint64(len(entries)))
	for _, e := range entries {
		b = append(append(b, e.k...), e.v...)
	}
	return b
}

func appendStruct(b []byte, rv reflect.Value, depth int) []byte {
	t := rv.Type()
	b = appendString(append(b, tagStruct), t.String())
	hidden := false
	var fields []int
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			fields = append(fields, i)
		} else {
			hidden = true
		}
	}
	b = appendUint(b, uint64(len(fields)))
	for _, i := range fields {
		b = appendString(b, t.Field(i).Name)
		b = appendReflect(b, rv.Field(i), depth+1)
	}
	if hidden || t.Implements(stringerType) {
		b = appendString(b, fmt.Sprintf("%v", rv.Interface()))
	}
	return b
}

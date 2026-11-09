package receipts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"reflect"
	"time"
)

// KeySize is the HMAC key's length in bytes.
const KeySize = 32

// The type tags of the result encoding. Every value is a tag, then a fixed or
// length-prefixed body, so no two different values share an encoding.
const (
	tagNull   = 'n'
	tagFalse  = 'f'
	tagTrue   = 't'
	tagInt    = 'i'
	tagUint   = 'u'
	tagFloat  = 'd'
	tagString = 's'
	tagBytes  = 'b'
	tagTime   = 'T'
	tagList   = 'l'
	tagMap    = 'm'
	tagOther  = 'x'
	tagStruct = 'S'
)

const resultDomain = "slaw-receipt-result-v1\x00"

func appendUint(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }

func appendString(b []byte, s string) []byte {
	b = appendUint(b, uint64(len(s)))
	return append(b, s...)
}

// ResultHasher is the streaming result_hmac: HMAC-SHA256, keyed with the
// workspace's receipt key, over a canonical encoding of the columns and then
// each row as shown. Every part is length-prefixed and every value carries a
// type tag, so the encoding is unambiguous: a different column, value, type or
// row order is a different digest. The key stops anyone brute-forcing a small
// result from the digest.
//
// Values are encoded by type: nil, bool, every signed and unsigned integer
// width (as 64-bit), floats (their IEEE bits, NaN made canonical), string,
// []byte, time.Time (UTC, RFC 3339 with nanoseconds), []any and
// map[string]any (keys sorted), and anything else as its Go type name and %v.
// An int64 and a float64 of the same number are different values.
type ResultHasher struct {
	mac  hash.Hash
	ncol int
	rows uint64
	buf  []byte
}

// NewResultHasher starts a fingerprint of a result with these columns. The key is
// LoadKey's.
func NewResultHasher(key []byte, columns []Column) *ResultHasher {
	h := &ResultHasher{mac: hmac.New(sha256.New, key), ncol: len(columns)}
	b := []byte(resultDomain)
	b = appendUint(b, uint64(len(columns)))
	for _, c := range columns {
		b = appendString(b, c.Name)
		b = appendString(b, c.Type)
	}
	h.mac.Write(b)
	h.buf = make([]byte, 0, 1024)
	return h
}

// AddRow feeds one row, in the order it is shown. A row of the wrong width is
// refused.
func (h *ResultHasher) AddRow(row []any) error {
	if len(row) != h.ncol {
		return fmt.Errorf("a row of %d values for %d columns", len(row), h.ncol)
	}
	b := append(h.buf[:0], 'R')
	for _, v := range row {
		b = appendValue(b, v)
	}
	h.mac.Write(b)
	h.buf = b[:0]
	h.rows++
	return nil
}

// Sum finishes the fingerprint as lowercase hex, counting the rows fed. It can
// be called once.
func (h *ResultHasher) Sum() string {
	h.mac.Write(appendUint([]byte{'E'}, h.rows))
	return hex.EncodeToString(h.mac.Sum(nil))
}

func appendValue(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, tagNull)
	case bool:
		if x {
			return append(b, tagTrue)
		}
		return append(b, tagFalse)
	case string:
		return appendString(append(b, tagString), x)
	case []byte:
		return appendString(append(b, tagBytes), string(x))
	case int:
		return appendUint(append(b, tagInt), uint64(int64(x)))
	case int8:
		return appendUint(append(b, tagInt), uint64(int64(x)))
	case int16:
		return appendUint(append(b, tagInt), uint64(int64(x)))
	case int32:
		return appendUint(append(b, tagInt), uint64(int64(x)))
	case int64:
		return appendUint(append(b, tagInt), uint64(x))
	case uint:
		return appendUint(append(b, tagUint), uint64(x))
	case uint8:
		return appendUint(append(b, tagUint), uint64(x))
	case uint16:
		return appendUint(append(b, tagUint), uint64(x))
	case uint32:
		return appendUint(append(b, tagUint), uint64(x))
	case uint64:
		return appendUint(append(b, tagUint), x)
	case float32:
		return appendFloat(b, float64(x))
	case float64:
		return appendFloat(b, x)
	case time.Time:
		return appendString(append(b, tagTime), x.UTC().Format(time.RFC3339Nano))
	case []any:
		b = appendUint(append(b, tagList), uint64(len(x)))
		for _, e := range x {
			b = appendValue(b, e)
		}
		return b
	default:
		return appendReflect(b, reflect.ValueOf(v), 0)
	}
}

func appendFloat(b []byte, f float64) []byte {
	if math.IsNaN(f) {
		f = math.NaN() // one canonical NaN
	}
	return appendUint(append(b, tagFloat), math.Float64bits(f))
}

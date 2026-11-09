package receipts

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	assert.True(t, p.KeepParams)
	assert.Equal(t, 30*24*time.Hour, p.MaxAge)
	assert.Equal(t, 20000, p.MaxCount)
	assert.Equal(t, int64(64<<20), p.MaxBytes)
	assert.Equal(t, p, p.Clamp())
}

func TestPolicyClamp_FloorsCeilingsAndUnset(t *testing.T) {
	lo := Policy{MaxAge: time.Minute, MaxCount: 1, MaxBytes: 1}.Clamp()
	assert.Equal(t, 24*time.Hour, lo.MaxAge)
	assert.Equal(t, 100, lo.MaxCount)
	assert.Equal(t, int64(1<<20), lo.MaxBytes)
	hi := Policy{MaxAge: 1000 * 24 * time.Hour, MaxCount: 1 << 30, MaxBytes: 1 << 40}.Clamp()
	assert.Equal(t, 365*24*time.Hour, hi.MaxAge)
	assert.Equal(t, 200000, hi.MaxCount)
	assert.Equal(t, int64(1<<30), hi.MaxBytes)
	d := DefaultPolicy()
	assert.Equal(t, d, Policy{KeepParams: true}.Clamp())
	neg := Policy{MaxAge: -1, MaxCount: -5, MaxBytes: -9}.Clamp()
	assert.Equal(t, d.MaxAge, neg.MaxAge)
	assert.Equal(t, d.MaxCount, neg.MaxCount)
	assert.Equal(t, d.MaxBytes, neg.MaxBytes)
}

func TestApplyParams_KeepParamsOffKeepsNamesAndHashOnly(t *testing.T) {
	r := sample(10)
	r.Params = map[string]ParamValue{"region": {Value: "EMEA-secret-ish"}, "n": {Value: "5"}}
	r.ParamsHash = HashParams(testKey, map[string]string{"region": "EMEA-secret-ish", "n": "5"})
	hash := r.ParamsHash
	p := DefaultPolicy()
	p.KeepParams = false
	p.ApplyParams(&r)
	s, err := Seal(r)
	require.NoError(t, err)
	assert.True(t, s.ParamsWithheld)
	assert.Equal(t, hash, s.ParamsHash)
	assert.Len(t, s.Params, 2)
	assert.Contains(t, s.Params, "region")
	assert.Contains(t, s.Params, "n")
	b, _ := Encode(s)
	assert.NotContains(t, string(b), "EMEA-secret-ish")
	assert.Contains(t, string(b), `"params":{"n":{},"region":{}}`)

	// A record that says withheld but still holds a value is refused.
	s.Params["n"] = ParamValue{Value: "5"}
	_, err = Seal(s)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestApplyParams_KeepParamsOnCapsAtAValidUTF8Boundary(t *testing.T) {
	r := sample(11)
	long := strings.Repeat("é", 200) // 400 bytes, two bytes a character
	r.Params = map[string]ParamValue{"a": {Value: long}, "b": {Value: "short"}, "c": {Withheld: true}}
	r.ParamsHash = HashParams(testKey, map[string]string{"a": long, "b": "short", "c": "secret"})
	DefaultPolicy().ApplyParams(&r)
	a := r.Params["a"]
	assert.True(t, a.Truncated)
	assert.LessOrEqual(t, len(a.Value), MaxParamBytes)
	assert.True(t, strings.HasPrefix(long, a.Value))
	assert.Equal(t, 0, len(a.Value)%2, "cut mid-character")
	assert.False(t, r.Params["b"].Truncated)
	assert.Equal(t, ParamValue{Withheld: true}, r.Params["c"])
	assert.False(t, r.ParamsWithheld)
	_, err := Seal(r)
	require.NoError(t, err)
	// A 3-byte character straddling the cap.
	v, cut := truncateUTF8(strings.Repeat("a", 255)+"€", 256)
	assert.True(t, cut)
	assert.Equal(t, strings.Repeat("a", 255), v)
}

func TestHashParams_IsOrderFreeAndUnambiguous(t *testing.T) {
	assert.Equal(t, HashParams(testKey, map[string]string{"a": "1", "b": "2"}), HashParams(testKey, map[string]string{"b": "2", "a": "1"}))
	assert.NotEqual(t, HashParams(testKey, map[string]string{"a": "1b", "": "2"}), HashParams(testKey, map[string]string{"a": "1", "b": "2"}))
	assert.NotEqual(t, HashParams(testKey, map[string]string{"ab": ""}), HashParams(testKey, map[string]string{"a": "b"}))
	assert.NotEqual(t, HashParams(testKey, nil), HashParams(testKey, map[string]string{"": ""}))
}

// The three hashes are keyed, so a withheld credential or a low-entropy value
// cannot be checked against a dictionary from a receipt alone.
func TestKeyedHashes_NeedTheKeyAndKeepTheirDomains(t *testing.T) {
	other := []byte("fedcba9876543210fedcba9876543210")
	assert.NotEqual(t, HashText(testKey, "SELECT 1"), HashText(other, "SELECT 1"))
	assert.NotEqual(t, HashRunText(testKey, "SELECT 1"), HashRunText(other, "SELECT 1"))
	assert.NotEqual(t, HashParams(testKey, map[string]string{"a": "1"}), HashParams(other, map[string]string{"a": "1"}))
	assert.NotEqual(t, HashText(testKey, "SELECT 1"), HashRunText(testKey, "SELECT 1"), "distinct domain tags")
	plain := sha256.Sum256([]byte("SELECT 1"))
	assert.NotEqual(t, hex.EncodeToString(plain[:]), HashText(testKey, "SELECT 1"), "not a plain SHA-256")
	assert.True(t, IsHash(HashText(testKey, "")))
}

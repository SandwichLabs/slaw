package receipts

import (
	"time"
	"unicode/utf8"
)

// The retention defaults, floors and ceilings. A person may move each limit
// inside [floor, ceiling] and no further, so a typo can neither switch pruning
// off nor make it delete everything.
const (
	DefaultMaxAge   = 30 * 24 * time.Hour
	DefaultMaxCount = 20000
	DefaultMaxBytes = int64(64 << 20)

	FloorMaxAge   = 24 * time.Hour
	FloorMaxCount = 100
	FloorMaxBytes = int64(1 << 20)

	CeilingMaxAge   = 365 * 24 * time.Hour
	CeilingMaxCount = 200000
	CeilingMaxBytes = int64(1 << 30)
)

// Policy is the per-person retention and privacy setting for receipts.
type Policy struct {
	// KeepParams keeps bound parameter values (each capped at MaxParamBytes);
	// off keeps only their names and params_hash.
	KeepParams bool
	MaxAge     time.Duration
	MaxCount   int
	MaxBytes   int64
}

// DefaultPolicy is the policy of a person who never changed a setting.
func DefaultPolicy() Policy {
	return Policy{KeepParams: true, MaxAge: DefaultMaxAge, MaxCount: DefaultMaxCount, MaxBytes: DefaultMaxBytes}
}

// Clamp returns p with every limit inside its floor and ceiling. A limit that
// is zero or negative is unset and takes its default. Clamp, not refusal, is
// the one rule, so a stored or typed value can never bypass the bounds and
// every consumer (the store, a settings form) applies the same function; the
// store clamps on every Prune.
func (p Policy) Clamp() Policy {
	if p.MaxAge <= 0 {
		p.MaxAge = DefaultMaxAge
	}
	if p.MaxCount <= 0 {
		p.MaxCount = DefaultMaxCount
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = DefaultMaxBytes
	}
	p.MaxAge = min(max(p.MaxAge, FloorMaxAge), CeilingMaxAge)
	p.MaxCount = min(max(p.MaxCount, FloorMaxCount), CeilingMaxCount)
	p.MaxBytes = min(max(p.MaxBytes, FloorMaxBytes), CeilingMaxBytes)
	return p
}

// ApplyParams shapes r's parameters by the policy, before the receipt is
// sealed. Every value is cut to MaxParamBytes at a UTF-8 boundary and marked
// truncated. With KeepParams off every value is replaced by nothing, the names
// stay, params_withheld is set and params_hash (computed by the caller over the
// full values) is kept. The map is replaced, never edited in place.
func (p Policy) ApplyParams(r *Receipt) {
	if len(r.Params) == 0 {
		return
	}
	out := make(map[string]ParamValue, len(r.Params))
	for name, v := range r.Params {
		switch {
		case !p.KeepParams:
			out[name] = ParamValue{}
		case v.Withheld:
			out[name] = ParamValue{Withheld: true}
		default:
			value, cut := truncateUTF8(v.Value, MaxParamBytes)
			out[name] = ParamValue{Value: value, Truncated: v.Truncated || cut}
		}
	}
	r.Params = out
	r.ParamsWithheld = !p.KeepParams
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

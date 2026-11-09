package receipts

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_RequiresUTCAndValidUTF8(t *testing.T) {
	r := sealed(t, 12)
	r.At = r.At.In(time.FixedZone("x", 3600))
	require.ErrorIs(t, r.Validate(), ErrInvalid, "one record would have two ids")
	// Seal normalises, so a caller with a local time still gets one id.
	local := sample(12)
	local.At = local.At.In(time.FixedZone("x", 3600))
	got, err := Seal(local)
	require.NoError(t, err)
	assert.Equal(t, sealed(t, 12).ID, got.ID)

	bad := "SELECT '\xff'"
	cases := map[string]func(*Receipt){
		"sql":      func(r *Receipt) { r.SQL = bad },
		"run sql":  func(r *Receipt) { r.RunSQL = bad },
		"ref":      func(r *Receipt) { r.Source.Ref = "queries/\xff.sql" },
		"session":  func(r *Receipt) { r.Actor, r.AgentSession = ActorAgent, "s\xff" },
		"param":    func(r *Receipt) { r.Params["id"] = ParamValue{Value: "\xff"} },
		"name":     func(r *Receipt) { r.Params["\xff"] = ParamValue{} },
		"conn":     func(r *Receipt) { r.Connections[0].Name = "w\xff" },
		"snapshot": func(r *Receipt) { r.Connections[0].Snapshot.Value = "\xff" },
		"column":   func(r *Receipt) { r.Columns[0].Name = "\xff" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := sample(13)
			mutate(&r)
			_, err := Seal(r)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

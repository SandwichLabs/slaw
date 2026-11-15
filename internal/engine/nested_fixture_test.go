package engine

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNestedTextSharedVectors runs the vectors that studio/frontend/src/lib
// nestedValue.spec.ts also runs, so the console's text (NestedText) and the
// grid's (nestedText in nestedValue.ts) cannot drift apart.
func TestNestedTextSharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/nested_text.json")
	require.NoError(t, err)
	var cases []struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value any    `json:"value"`
		Text  string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.NotEmpty(t, cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Type == "JSON" {
				assert.Equal(t, c.Text, CellTextTyped(c.Value, "NULL", c.Type))
				return
			}
			assert.Equal(t, c.Text, NestedText(c.Value, c.Type))
		})
	}
}

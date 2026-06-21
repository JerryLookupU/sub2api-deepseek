package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendAnthropicSystemDirective(t *testing.T) {
	t.Run("empty directive is a no-op", func(t *testing.T) {
		system := json.RawMessage(`"existing"`)
		got := AppendAnthropicSystemDirective(system, "   ")
		require.Equal(t, `"existing"`, string(got))
	})

	t.Run("appends to string system without clobbering", func(t *testing.T) {
		system := json.RawMessage(`"client instructions"`)
		got := AppendAnthropicSystemDirective(system, "DIRECTIVE")
		var s string
		require.NoError(t, json.Unmarshal(got, &s))
		require.True(t, strings.HasPrefix(s, "client instructions"))
		require.Contains(t, s, "\n\nDIRECTIVE")
	})

	t.Run("appends when system is empty", func(t *testing.T) {
		got := AppendAnthropicSystemDirective(nil, "DIRECTIVE")
		var s string
		require.NoError(t, json.Unmarshal(got, &s))
		require.Equal(t, "DIRECTIVE", s)
	})

	t.Run("extracts text from content blocks then appends directive", func(t *testing.T) {
		system := json.RawMessage(`[{"type":"text","text":"block system"}]`)
		got := AppendAnthropicSystemDirective(system, "DIRECTIVE")
		var s string
		require.NoError(t, json.Unmarshal(got, &s))
		require.Contains(t, s, "block system")
		require.Contains(t, s, "DIRECTIVE")
	})
}

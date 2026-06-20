package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrependChatSystemDirective(t *testing.T) {
	t.Run("prepends a system message carrying the directive", func(t *testing.T) {
		req := &ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
		PrependChatSystemDirective(req, "DIRECTIVE TEXT")
		require.Len(t, req.Messages, 2)
		require.Equal(t, "system", req.Messages[0].Role)
		require.Contains(t, string(req.Messages[0].Content), "DIRECTIVE TEXT")
		require.Equal(t, "user", req.Messages[1].Role)
	})

	t.Run("empty directive is a no-op", func(t *testing.T) {
		req := &ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
		PrependChatSystemDirective(req, "")
		require.Len(t, req.Messages, 1)
	})

	t.Run("nil request is safe", func(t *testing.T) {
		require.NotPanics(t, func() { PrependChatSystemDirective(nil, "x") })
	})
}

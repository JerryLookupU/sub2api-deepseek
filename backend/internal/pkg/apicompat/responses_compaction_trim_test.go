package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTrimResponsesInputForCompaction(t *testing.T) {
	limits := CompactionTrimLimits{MessageTextChars: 10, ToolOutputChars: 5}

	t.Run("truncates long array-form message text", func(t *testing.T) {
		input := json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("x", 80) + `"}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		text := gjson.GetBytes(out, "0.content.0.text").String()
		require.Contains(t, text, "... [truncated]")
		require.Less(t, len([]rune(text)), 80)
	})

	t.Run("truncates long string-form message content", func(t *testing.T) {
		input := json.RawMessage(`[{"role":"user","content":"` + strings.Repeat("y", 60) + `"}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.Contains(t, gjson.GetBytes(out, "0.content").String(), "... [truncated]")
	})

	t.Run("truncates function_call_output", func(t *testing.T) {
		input := json.RawMessage(`[{"type":"function_call_output","call_id":"c1","output":"` + strings.Repeat("z", 40) + `"}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.Contains(t, gjson.GetBytes(out, "0.output").String(), "... [truncated]")
	})

	t.Run("leaves lone compaction_summary untouched", func(t *testing.T) {
		long := strings.Repeat("p", 60)
		input := json.RawMessage(`[{"type":"compaction_summary","encrypted_content":"` + long + `"}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.Equal(t, string(input), string(out))
	})

	t.Run("leaves compaction_trigger and short content unchanged", func(t *testing.T) {
		input := json.RawMessage(`[{"type":"compaction_trigger"},{"role":"user","content":[{"type":"input_text","text":"short"}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.Equal(t, string(input), string(out))
	})

	t.Run("fail-open on malformed input", func(t *testing.T) {
		input := json.RawMessage(`not-json`)
		require.Equal(t, string(input), string(TrimResponsesInputForCompaction(input, limits)))
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		require.Nil(t, TrimResponsesInputForCompaction(nil, limits))
	})

	// Regression for the blocker codex found: a prior compaction_summary must keep
	// its encrypted_content (an unmodeled field) even when another item is trimmed.
	t.Run("preserves prior compaction_summary when re-compacting mixed input", func(t *testing.T) {
		input := json.RawMessage(`[{` +
			`"type":"compaction_summary","encrypted_content":"PRIOR_COMPACTED_CONTEXT"` +
			`},{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("a", 60) + `"}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.True(t, json.Valid(out), "output must be valid JSON")
		require.Equal(t, "compaction_summary", gjson.GetBytes(out, "0.type").String())
		require.Equal(t, "PRIOR_COMPACTED_CONTEXT", gjson.GetBytes(out, "0.encrypted_content").String(),
			"prior compaction_summary.encrypted_content must survive (raw-preserving transform)")
		require.Contains(t, gjson.GetBytes(out, "1.content.0.text").String(), "... [truncated]",
			"the long user message must still be trimmed")
	})

	t.Run("preserves call_id on function_call and function_call_output", func(t *testing.T) {
		input := json.RawMessage(`[{` +
			`"type":"function_call","call_id":"call_42","name":"foo","arguments":"{}"` +
			`},{"type":"function_call_output","call_id":"call_42","output":"` + strings.Repeat("z", 40) + `"}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.True(t, json.Valid(out))
		require.Equal(t, "call_42", gjson.GetBytes(out, "0.call_id").String())
		require.Equal(t, "foo", gjson.GetBytes(out, "0.name").String())
		require.Equal(t, "call_42", gjson.GetBytes(out, "1.call_id").String())
		require.Contains(t, gjson.GetBytes(out, "1.output").String(), "... [truncated]")
	})

	t.Run("preserves unknown fields when content trimmed", func(t *testing.T) {
		input := json.RawMessage(`[{"role":"user","custom_unknown_field":"keep-me","content":[{"type":"input_text","text":"` + strings.Repeat("a", 60) + `"}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.True(t, json.Valid(out))
		require.Equal(t, "keep-me", gjson.GetBytes(out, "0.custom_unknown_field").String())
		require.Contains(t, gjson.GetBytes(out, "0.content.0.text").String(), "... [truncated]")
	})

	t.Run("does not trim system/developer role items but trims user", func(t *testing.T) {
		long := strings.Repeat("d", 60)
		input := json.RawMessage(`[{` +
			`"role":"developer","content":"` + long + `"` +
			`},{"role":"system","content":"` + long + `"` +
			`},{"role":"user","content":[{"type":"input_text","text":"` + long + `"}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.True(t, json.Valid(out))
		require.Equal(t, long, gjson.GetBytes(out, "0.content").String(), "developer content must be preserved verbatim")
		require.Equal(t, long, gjson.GetBytes(out, "1.content").String(), "system content must be preserved verbatim")
		require.Contains(t, gjson.GetBytes(out, "2.content.0.text").String(), "... [truncated]")
	})

	t.Run("truncates multi-byte unicode on a rune boundary", func(t *testing.T) {
		// CJK + emoji, all multi-byte; cap at 5 runes.
		unicodeLimits := CompactionTrimLimits{MessageTextChars: 5, ToolOutputChars: 5}
		text := "中文测试🚀🎉abcdef"
		input := json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"` + text + `"}]}]`)
		out := TrimResponsesInputForCompaction(input, unicodeLimits)
		require.True(t, json.Valid(out), "must remain valid JSON after rune truncation")
		got := gjson.GetBytes(out, "0.content.0.text").String()
		require.True(t, strings.HasPrefix(got, "中文测试🚀"), "first 5 runes preserved: %q", got)
		require.Contains(t, got, "... [truncated]")
	})

	// Locks the residual edge codex noted: part-level unmodeled fields (e.g.
	// cache_control) must survive when a text content part is trimmed.
	t.Run("preserves part-level unknown fields when text part trimmed", func(t *testing.T) {
		input := json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"` +
			strings.Repeat("a", 60) + `","cache_control":{"type":"ephemeral"}}]}]`)
		out := TrimResponsesInputForCompaction(input, limits)
		require.True(t, json.Valid(out))
		require.Equal(t, "ephemeral", gjson.GetBytes(out, "0.content.0.cache_control.type").String(),
			"part-level cache_control must survive the raw-preserving trim")
		require.Contains(t, gjson.GetBytes(out, "0.content.0.text").String(), "... [truncated]")
	})
}

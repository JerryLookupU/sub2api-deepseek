package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIPassthroughOAuthBody_RemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"prompt_cache_retention":"24h","safety_identifier":"sid","stream_options":{"include_usage":true}}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	for _, field := range openAIChatGPTInternalUnsupportedFields {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), "%s should be stripped", field)
	}
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
}

func TestNormalizeOpenAIPassthroughOAuthBody_CompactRemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"stream":true,"store":true}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "user").Exists())
	require.False(t, gjson.GetBytes(normalized, "metadata").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
}

func TestEnsureOpenAIPassthroughInstructions_AddsDefaultForGPT55(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"text","text":"hello"}]}`)

	normalized, changed, err := ensureOpenAIPassthroughInstructions(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "You are a helpful coding assistant.", gjson.GetBytes(normalized, "instructions").String())
}

func TestEnsureOpenAIPassthroughInstructions_PreservesExisting(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","instructions":"keep me","input":[{"type":"text","text":"hello"}]}`)

	normalized, changed, err := ensureOpenAIPassthroughInstructions(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(normalized))
}

func TestNormalizePlaintextEncryptedContentInOpenAIBody_RewritesPlainCompactionSummaryMessage(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_summary","encrypted_content":"Hi there compacted summary"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)

	normalized, changed, err := normalizePlaintextEncryptedContentInOpenAIBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.0.type").String())
	require.Equal(t, "user", gjson.GetBytes(normalized, "input.0.role").String())
	require.Contains(t, gjson.GetBytes(normalized, "input.0.content.0.text").String(), "Hi there compacted summary")
	require.False(t, gjson.GetBytes(normalized, "input.0.encrypted_content").Exists())
	require.Equal(t, "hello", gjson.GetBytes(normalized, "input.1.content.0.text").String())
}

func TestNormalizePlaintextEncryptedContentInOpenAIBody_RewritesPlainReasoningEncryptedContentMessage(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"reasoning","summary":[],"encrypted_content":"Hi there compacted conversation"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)

	normalized, changed, err := normalizePlaintextEncryptedContentInOpenAIBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.0.type").String())
	require.Equal(t, "user", gjson.GetBytes(normalized, "input.0.role").String())
	require.Contains(t, gjson.GetBytes(normalized, "input.0.content.0.text").String(), "Hi there compacted conversation")
	require.False(t, gjson.GetBytes(normalized, "input.0.encrypted_content").Exists())
	require.Equal(t, "hello", gjson.GetBytes(normalized, "input.1.content.0.text").String())
}

func TestNormalizePlaintextEncryptedContentInOpenAIBody_RemovesNestedPlainEncryptedContent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello","encrypted_content":"plain nested value"}]}]}`)

	normalized, changed, err := normalizePlaintextEncryptedContentInOpenAIBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "hello", gjson.GetBytes(normalized, "input.0.content.0.text").String())
	require.False(t, gjson.GetBytes(normalized, "input.0.content.0.encrypted_content").Exists())
}

func TestNormalizePlaintextEncryptedContentInOpenAIBody_RewritesAnyPlainInputItem(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"opaque_history_item","encrypted_content":"Hi there compacted item"}]}`)

	normalized, changed, err := normalizePlaintextEncryptedContentInOpenAIBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.0.type").String())
	require.Contains(t, gjson.GetBytes(normalized, "input.0.content.0.text").String(), "Hi there compacted item")
	require.False(t, gjson.GetBytes(normalized, "input.0.encrypted_content").Exists())
}

func TestNormalizePlaintextEncryptedContentInOpenAIBody_PreservesOpenAIEncryptedSummary(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_summary","encrypted_content":"gAAAAAB1234567890123456789012345678901234567890123456789012345678901234"}]}`)

	normalized, changed, err := normalizePlaintextEncryptedContentInOpenAIBody(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(normalized))
}

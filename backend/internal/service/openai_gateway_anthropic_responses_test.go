package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIForwardAnthropicResponsesAPIKeyConvertsResponsesToAnthropic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","instructions":"be terse","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-chat","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_1"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{
					Enabled:           false,
					AllowInsecureHTTP: true,
				},
			},
		},
		httpUpstream: upstream,
	}
	account := openAIAnthropicResponsesTestAccount()
	account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "http://deepseek.example/anthropic/v1/messages?beta=true", upstream.lastReq.URL.String())
	require.Equal(t, "sk-deepseek", getHeaderRaw(upstream.lastReq.Header, "x-api-key"))
	require.Equal(t, "2023-06-01", getHeaderRaw(upstream.lastReq.Header, "anthropic-version"))
	require.Equal(t, "text/event-stream", getHeaderRaw(upstream.lastReq.Header, "accept"))
	require.Equal(t, "deepseek-chat", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "be terse", gjson.GetBytes(upstream.lastBody, "system").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "messages.0.content.0.text").String())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "deepseek-v4-flash", gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, "deepseek-v4-flash", result.Model)
	require.Equal(t, "deepseek-chat", result.UpstreamModel)
	require.Equal(t, "deepseek-chat", result.BillingModel)
	require.False(t, result.Stream)
}

func TestOpenAIAnthropicResponsesAPIKeySupportsResponsesOnly(t *testing.T) {
	account := openAIAnthropicResponsesTestAccount()

	require.True(t, account.IsOpenAIAnthropicResponsesAPIKey())
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
}

func openAIAnthropicResponsesTestAccount() *Account {
	return &Account{
		ID:          202,
		Name:        "deepseek-openai-anthropic",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":  "sk-deepseek",
			"base_url": "http://deepseek.example/anthropic",
			"model_mapping": map[string]any{
				"deepseek-v4-flash": "deepseek-chat",
			},
			openAIEndpointCapabilitiesCredentialKey: []any{string(OpenAIEndpointCapabilityResponses)},
		},
		Extra: map[string]any{
			openAIUpstreamProtocolExtraKey: OpenAIUpstreamProtocolAnthropicResponses,
		},
	}
}

func TestOpenAIForwardAnthropicResponsesCompactRewritesToSingleCompactionItem(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","instructions":"summarize","input":[{"role":"user","content":[{"type":"input_text","text":"long conversation"}]}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-chat","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compact summary"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_c"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
			},
		},
		httpUpstream: upstream,
	}
	account := openAIAnthropicResponsesTestAccount()

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	// remote compaction v2 要求恰好一个 type="compaction_summary" 的 output item，且 encrypted_content 承载摘要正文。
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response.compaction", gjson.Get(rec.Body.String(), "object").String())
	require.Len(t, gjson.Get(rec.Body.String(), "output").Array(), 1)
	require.Equal(t, "compaction_summary", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "output.0.encrypted_content").String(), "compact summary")
}

func TestOpenAIForwardAnthropicResponsesCompactStreamingEmitsSingleCompactionItem(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","input":[{"role":"user","content":[{"type":"input_text","text":"summarize"}]}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-chat","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compact summary"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_s"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
			},
		},
		httpUpstream: upstream,
	}
	account := openAIAnthropicResponsesTestAccount()

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 流式 compact：SSE 输出恰好一个 compaction_summary item，不再有 reasoning/message 两个 item。
	s := rec.Body.String()
	require.Contains(t, s, "event: response.created")
	require.Contains(t, s, "event: response.completed")
	require.Contains(t, s, `"object":"response.compaction"`)
	require.Contains(t, s, `"type":"compaction_summary"`)
	require.Contains(t, s, `encrypted_content`)
	require.NotContains(t, s, `"type":"reasoning"`)
	require.NotContains(t, s, `"type":"message"`)
}

func TestOpenAIForwardAnthropicResponsesCompactionTriggerStreamingEmitsSingleCompactionItem(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"summarize"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"prior summary"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},{"type":"compaction_trigger"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-chat","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compact trigger summary"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_trigger"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
			},
		},
		httpUpstream: upstream,
	}
	account := openAIAnthropicResponsesTestAccount()

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	s := rec.Body.String()
	require.Contains(t, s, "event: response.created")
	require.Contains(t, s, "event: response.completed")
	require.Contains(t, s, `"object":"response.compaction"`)
	require.Contains(t, s, `"type":"compaction_summary"`)
	require.Contains(t, s, "compact trigger summary")
	require.NotContains(t, s, `"type":"reasoning"`)
	require.NotContains(t, s, `"type":"message"`)
}

func TestOpenAIForwardAnthropicResponsesCompactionTriggerOnlyAddsFallbackMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-pro","input":[{"type":"compaction_trigger"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-reasoner","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"empty-context summary"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_trigger_only"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
			},
		},
		httpUpstream: upstream,
	}
	account := openAIAnthropicResponsesTestAccount()
	account.Credentials["model_mapping"] = map[string]any{
		"deepseek-v4-pro": "deepseek-reasoner",
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "deepseek-reasoner", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Len(t, gjson.GetBytes(upstream.lastBody, "messages").Array(), 1)
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "messages.0.content").String(), "compact summary")
	require.NotEqual(t, "null", gjson.GetBytes(upstream.lastBody, "messages").Raw)

	s := rec.Body.String()
	require.Contains(t, s, `"object":"response.compaction"`)
	require.Contains(t, s, `"type":"compaction_summary"`)
	require.Contains(t, s, "empty-context summary")
}

// anthropicCompactDirectiveRecorder runs a /v1/responses/compact request through
// the anthropic bridge and returns the captured upstream request/recorder so tests
// can inspect the injected system directive.
func anthropicCompactDirectiveRecorder(t *testing.T, account *Account, body []byte) *httpUpstreamRecorder {
	t.Helper()
	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek-chat","stop_reason":"","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compact summary"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "request-id": []string{"req_ds_dir"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true},
			},
		},
		httpUpstream: upstream,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	_, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	return upstream
}

func TestOpenAIForwardAnthropicResponsesCompactInjectsDefaultDirective(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","instructions":"codex rules","input":[{"role":"user","content":[{"type":"input_text","text":"long conversation"}]}],"stream":false}`)
	account := openAIAnthropicResponsesTestAccount()

	upstream := anthropicCompactDirectiveRecorder(t, account, body)

	sys := gjson.GetBytes(upstream.lastBody, "system").String()
	require.Contains(t, sys, "codex rules", "Codex instructions must be preserved, not clobbered")
	require.Contains(t, sys, "context compaction", "default compact directive must be injected")
	require.Contains(t, sys, "PRESERVE EXACTLY")
}

func TestOpenAIForwardAnthropicResponsesCompactChunqiuStyleAppendsAddon(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","input":[{"role":"user","content":[{"type":"input_text","text":"long conversation"}]}],"stream":false}`)
	account := openAIAnthropicResponsesTestAccount()
	account.Extra["compact_style"] = "chunqiu"

	upstream := anthropicCompactDirectiveRecorder(t, account, body)

	sys := gjson.GetBytes(upstream.lastBody, "system").String()
	require.Contains(t, sys, "CHUNQIU MODE", "chunqiu addon must be appended when compact_style=chunqiu")
	require.Contains(t, sys, "context compaction", "default directive still present as the base")
}

func TestOpenAIForwardAnthropicResponsesCompactCustomPromptOverridesDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","input":[{"role":"user","content":[{"type":"input_text","text":"long conversation"}]}],"stream":false}`)
	account := openAIAnthropicResponsesTestAccount()
	account.Extra["compact_system_prompt"] = "MY CUSTOM COMPACT PROMPT"

	upstream := anthropicCompactDirectiveRecorder(t, account, body)

	sys := gjson.GetBytes(upstream.lastBody, "system").String()
	require.Contains(t, sys, "MY CUSTOM COMPACT PROMPT", "custom prompt must override the built-in default")
	require.NotContains(t, sys, "context compaction", "built-in default must not leak when custom prompt is set")
}

func TestOpenAIForwardAnthropicResponsesCompactTrimsLargeInputBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	long := strings.Repeat("a", 2000)
	body := []byte(`{"model":"deepseek-v4-flash","input":[{"role":"user","content":[{"type":"input_text","text":"` + long + `"}]}],"stream":false}`)
	account := openAIAnthropicResponsesTestAccount()

	upstream := anthropicCompactDirectiveRecorder(t, account, body)

	content := gjson.GetBytes(upstream.lastBody, "messages.0.content.0.text").String()
	require.Contains(t, content, "... [truncated]", "compact path must pre-trim oversized input blocks")
	require.Less(t, len(content), 2000, "trimmed content must be smaller than the original")
}

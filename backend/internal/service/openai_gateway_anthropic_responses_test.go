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

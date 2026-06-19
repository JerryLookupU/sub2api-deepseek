package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (s *OpenAIGatewayService) forwardResponsesViaAnthropicResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	clientStream := responsesReq.Stream
	compactRequest := isOpenAIResponsesCompactRequest(c, body)

	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}
	ensureAnthropicCompactRequestHasMessages(anthropicReq, compactRequest)
	anthropicReq.Stream = true

	mappedModel := account.GetMappedModel(originalModel)
	anthropicReq.Model = mappedModel

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	anthropicBody = enforceCacheControlLimit(anthropicBody)

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, true)
	upstreamReq, err := s.buildOpenAIAnthropicResponsesUpstreamRequest(upstreamCtx, c, account, anthropicBody, token)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build anthropic upstream request: %w", err)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	upstreamStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeResponsesError(c, http.StatusBadGateway, "server_error", "Upstream request failed")
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		if upstreamMsg == "" {
			upstreamMsg = http.StatusText(resp.StatusCode)
		}

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  openAIAnthropicResponsesRequestID(resp.Header),
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, mappedModel)
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}

		writeResponsesError(c, mapUpstreamStatusCode(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}
	defer func() { _ = resp.Body.Close() }()

	reasoningEffort := ExtractResponsesReasoningEffortFromBody(body)
	// compact 请求需要整体重写为单个 compaction_summary item：统一走 buffered（即使客户端流式），
	// 在 buffered 末尾按 clientStream 以 SSE 或 JSON 输出该 compaction_summary item。
	if clientStream && !compactRequest {
		return s.handleOpenAIAnthropicResponsesStreamingResponse(resp, c, originalModel, mappedModel, reasoningEffort, startTime)
	}
	return s.handleOpenAIAnthropicResponsesBufferedStreamingResponse(resp, c, originalModel, mappedModel, reasoningEffort, startTime, clientStream, compactRequest)
}

func (s *OpenAIGatewayService) buildOpenAIAnthropicResponsesUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	baseURL := strings.TrimSpace(account.GetBaseURL())
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	targetURL := strings.TrimRight(validatedURL, "/") + "/v1/messages?beta=true"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	setHeaderRaw(req.Header, "x-api-key", token)
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(key)
			if !allowedHeaders[lowerKey] {
				continue
			}
			wireKey := resolveWireCasing(key)
			for _, value := range values {
				addHeaderRaw(req.Header, wireKey, value)
			}
		}
	}
	if getHeaderRaw(req.Header, "content-type") == "" {
		setHeaderRaw(req.Header, "content-type", "application/json")
	}
	if getHeaderRaw(req.Header, "anthropic-version") == "" {
		setHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if getHeaderRaw(req.Header, "accept") == "" {
		setHeaderRaw(req.Header, "accept", "text/event-stream")
	}

	return req, nil
}

func (s *OpenAIGatewayService) handleOpenAIAnthropicResponsesBufferedStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientStream bool,
	compactRequest bool,
) (*OpenAIForwardResult, error) {
	requestID := openAIAnthropicResponsesRequestID(resp.Header)

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	var finalResp *apicompat.AnthropicResponse
	var usage ClaudeUsage

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}

		if !scanner.Scan() {
			break
		}
		dataLine := scanner.Text()
		if !strings.HasPrefix(dataLine, "data: ") {
			continue
		}

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(dataLine[6:]), &event); err != nil {
			logger.L().Warn("openai_anthropic_responses buffered: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			continue
		}

		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}
		if event.Type == "message_delta" {
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = event.Delta.StopReason
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = appendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai_anthropic_responses buffered: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	if finalResp == nil {
		writeResponsesError(c, http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalResp.Usage = apicompat.AnthropicUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		}
	}

	responsesResp := apicompat.AnthropicToResponsesResponse(finalResp)
	responsesResp.Model = originalModel
	// compact 请求：codex remote compaction v2 要求恰好一个 type="compaction_summary" 的 output
	// item。anthropic 桥接产出的是 reasoning/message 多 item，需重写为单个 compaction_summary item
	// （对齐 chat fallback 路径 rewriteChatFallbackResponsesAsCompact 的做法）。
	if compactRequest {
		rewriteChatFallbackResponsesAsCompact(responsesResp)
	}

	if clientStream {
		// compact 流式场景：以 Responses SSE 输出该单个 compaction_summary item，满足 codex 流式解析预期。
		s.writeAnthropicResponsesAsSSE(c, resp.Header, responsesResp)
	} else {
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		}
		if respBytes, err := json.Marshal(responsesResp); err == nil {
			respBytes = reverseToolNamesIfPresent(c, respBytes)
			c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
		} else {
			c.JSON(http.StatusOK, responsesResp)
		}
	}

	return &OpenAIForwardResult{
		RequestID:       requestID,
		Usage:           openAIUsageFromClaudeUsage(usage),
		Model:           originalModel,
		BillingModel:    mappedModel,
		UpstreamModel:   mappedModel,
		ReasoningEffort: reasoningEffort,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

func ensureAnthropicCompactRequestHasMessages(req *apicompat.AnthropicRequest, compactRequest bool) {
	if req == nil || len(req.Messages) > 0 {
		return
	}
	if !compactRequest {
		req.Messages = []apicompat.AnthropicMessage{}
		return
	}

	content, _ := json.Marshal("Create a compact summary of the conversation context for continuing the session. Preserve important user requests, decisions, files, commands, errors, and pending work. If no prior context is available, return a concise empty-context summary.")
	req.Messages = []apicompat.AnthropicMessage{{
		Role:    "user",
		Content: content,
	}}
}

func (s *OpenAIGatewayService) handleOpenAIAnthropicResponsesStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := openAIAnthropicResponsesRequestID(resp.Header)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	state := apicompat.NewAnthropicEventToResponsesState()
	state.Model = originalModel
	var usage ClaudeUsage
	var firstTokenMs *int
	firstChunk := true

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	resultWithUsage := func() *OpenAIForwardResult {
		return &OpenAIForwardResult{
			RequestID:       requestID,
			Usage:           openAIUsageFromClaudeUsage(usage),
			Model:           originalModel,
			BillingModel:    mappedModel,
			UpstreamModel:   mappedModel,
			ReasoningEffort: reasoningEffort,
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}
	}

	processEvent := func(event *apicompat.AnthropicStreamEvent) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if event.Type == "message_delta" && event.Usage != nil {
			mergeAnthropicUsage(&usage, *event.Usage)
		}
		if event.Type == "message_start" && event.Message != nil {
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		events := apicompat.AnthropicEventToResponsesEvents(event, state)
		for _, evt := range events {
			sse, err := apicompat.ResponsesEventToSSE(evt)
			if err != nil {
				logger.L().Warn("openai_anthropic_responses stream: failed to marshal event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			out := string(reverseToolNamesIfPresent(c, []byte(sse)))
			if _, err := fmt.Fprint(c.Writer, out); err != nil {
				logger.L().Info("openai_anthropic_responses stream: client disconnected",
					zap.String("request_id", requestID),
				)
				return true
			}
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}
		return false
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}

		if !scanner.Scan() {
			break
		}
		dataLine := scanner.Text()
		if !strings.HasPrefix(dataLine, "data: ") {
			continue
		}

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(dataLine[6:]), &event); err != nil {
			logger.L().Warn("openai_anthropic_responses stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			continue
		}
		if processEvent(&event) {
			return resultWithUsage(), nil
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai_anthropic_responses stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	if finalEvents := apicompat.FinalizeAnthropicResponsesStream(state); len(finalEvents) > 0 {
		for _, evt := range finalEvents {
			sse, err := apicompat.ResponsesEventToSSE(evt)
			if err != nil {
				continue
			}
			out := string(reverseToolNamesIfPresent(c, []byte(sse)))
			_, _ = fmt.Fprint(c.Writer, out)
		}
		c.Writer.Flush()
	}

	return resultWithUsage(), nil
}

func openAIUsageFromClaudeUsage(usage ClaudeUsage) OpenAIUsage {
	return OpenAIUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		ImageOutputTokens:        usage.ImageOutputTokens,
	}
}

func openAIAnthropicResponsesRequestID(header http.Header) string {
	if header == nil {
		return ""
	}
	if requestID := strings.TrimSpace(header.Get("request-id")); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(header.Get("x-request-id"))
}

// writeAnthropicResponsesAsSSE 以 Responses SSE 形式输出一个完整的 responsesResp，
// 用于 compact 流式场景：把单个 compaction_summary item 以 codex 期望的流式事件序列输出。
func (s *OpenAIGatewayService) writeAnthropicResponsesAsSSE(c *gin.Context, upstreamHeader http.Header, resp *apicompat.ResponsesResponse) {
	if c == nil || resp == nil {
		return
	}
	if s != nil && s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), upstreamHeader, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	writeEvt := func(evt apicompat.ResponsesStreamEvent) {
		sse, err := apicompat.ResponsesEventToSSE(evt)
		if err != nil {
			return
		}
		out := string(reverseToolNamesIfPresent(c, []byte(sse)))
		_, _ = fmt.Fprint(c.Writer, out)
	}

	created := *resp
	created.Status = "in_progress"
	created.Output = []apicompat.ResponsesOutput{}
	writeEvt(apicompat.ResponsesStreamEvent{Type: "response.created", Response: &created})
	for i := range resp.Output {
		item := resp.Output[i]
		writeEvt(apicompat.ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: i, Item: &item})
		writeEvt(apicompat.ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: i, Item: &item})
	}
	writeEvt(apicompat.ResponsesStreamEvent{Type: "response.completed", Response: resp})
	c.Writer.Flush()
}

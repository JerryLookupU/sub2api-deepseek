# Codex DeepSeek 模型目录接入修改报告

日期：2026-06-19

## 背景

本次修改的目标是在不要求用户维护 `model_catalog_json` / `model-catalogs` 的前提下，让 Codex 的 `/model` 模型选择列表出现以下自定义模型：

- `deepseek-v4-pro`
- `deepseek-v4-flash`

同时保留 Codex 原有模型，例如 `gpt-5.5`。最终方案不是在用户本地配置中覆盖完整模型目录，而是在 Sub2API 后端为 Codex 模型目录请求返回增量模型目录，让 Codex 客户端与自身内置目录合并。

## 涉及提交

| 提交 | 主题 | 主要能力 |
| --- | --- | --- |
| `b9e64179` | `feat: OpenAI Responses API 与 Anthropic 兼容支持 + 前端文档补充` | 增加 Codex DeepSeek 模型目录、OpenAI Responses 与 Anthropic Messages 双向转换、Anthropic-compatible upstream 桥接 |
| `1e8fd04c` | `fix(codex): support compact across model switches` | 修复 Codex `/compact` 输出形状、Anthropic compact `messages:null`、模型切换后的明文 `encrypted_content` 上下文问题，并增加 built canary 脚本 |

## 近期能力总览

### 1. Anthropic response 到 OpenAI Responses 的转换

Sub2API 现在可以让 OpenAI Responses 客户端访问 Anthropic-compatible upstream。请求链路是：

```text
Codex / OpenAI Responses client
  -> Sub2API /v1/responses
  -> ResponsesToAnthropicRequest
  -> upstream /v1/messages?beta=true
  -> AnthropicToResponsesResponse / AnthropicEventToResponsesEvents
  -> OpenAI Responses JSON/SSE returned to client
```

转换覆盖：

- Anthropic `text` block -> Responses `message` / `output_text`。
- Anthropic `thinking` block -> Responses `reasoning` / `summary_text`。
- Anthropic `tool_use` block -> Responses `function_call`，并保留 `call_id` / `name` / `arguments`。
- Anthropic `stop_reason=max_tokens` -> Responses `status=incomplete` + `incomplete_details.reason=max_output_tokens`。
- Anthropic usage 中的 `cache_read_input_tokens` / `cache_creation_input_tokens` 会补回 Responses `input_tokens` 语义，避免下游统计少算缓存 token。
- 流式 Anthropic SSE 会被状态机转换成 Responses SSE，例如 `response.created`、`response.output_item.added`、`response.output_text.delta`、`response.reasoning_summary_text.delta`、`response.function_call_arguments.delta`、`response.completed`。

对应代码：

- `backend/internal/pkg/apicompat/anthropic_to_responses_response.go`
- `backend/internal/service/openai_gateway_anthropic_responses.go`
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`

### 2. Codex Anthropic compact 能力

Codex `/compact` 不要求 DeepSeek/Anthropic-compatible 上游原生支持 `/responses/compact`。Sub2API 接住 compact 请求后，用上游 Messages/ChatCompletions 生成摘要，再包装成 Codex remote compact v2 期望的单个 output item：

```json
{
  "object": "response.compaction",
  "output": [
    {
      "type": "compaction_summary",
      "encrypted_content": "..."
    }
  ]
}
```

支持两种触发方式：

- 显式路径：`/v1/responses/compact`。
- Codex TUI 触发：普通 `/v1/responses`，但 `input` 中包含 `{"type":"compaction_trigger"}`。

修复点：

- ChatCompletions fallback 原来会返回普通 `reasoning` + `message` 两个 item，Codex 解析 compact 时会报 `expected exactly one compaction output item, got 0 from 2 output items`。现在会重写为单个 `compaction_summary`。
- Anthropic-compatible fallback 原来在 trigger-only compact 场景下会转出 `messages:null`，DeepSeek 上游报 `messages: invalid type: null, expected a sequence`。现在如果 compact 请求没有可转换消息，会补一个最小 user message。
- compact 流式请求不会直接透传上游 reasoning/message SSE，而是先 buffer 上游结果，再以 Responses SSE 输出单个 `compaction_summary` item。

对应代码：

- `backend/internal/service/openai_gateway_anthropic_responses.go`
- `backend/internal/service/openai_gateway_responses_chat_fallback.go`
- `backend/internal/service/openai_gateway_anthropic_responses_test.go`
- `backend/internal/service/openai_gateway_responses_chat_fallback_test.go`

### 3. 切换模型后的上下文支持

Codex 在同一会话中切换模型时，会把上一段 compact 结果继续带到下一次请求。现在针对两类方向分别处理：

- `gpt-5.5 -> deepseek-v4-pro/flash`：compact 请求可能携带 `previous_response_id`。HTTP 普通 Responses 续链仍不支持该字段，但 compact fallback 不依赖上游续链 ID，因此只在 compact 请求中放行，后续按当前 `input` 生成 `response.compaction`。
- `deepseek-v4-pro/flash -> gpt-5.5`：DeepSeek compact fallback 产生的 `encrypted_content` 实际是明文摘要。直接送到 OpenAI upstream 会触发 `invalid_encrypted_content`。现在非 compact 请求转发 OpenAI 前会识别非 OpenAI 加密格式的 `encrypted_content`，把它改写成普通 user message：

```json
{
  "type": "message",
  "role": "user",
  "content": [
    {
      "type": "input_text",
      "text": "Previous compacted conversation summary:\n\n..."
    }
  ]
}
```

保护边界：

- 只把不像 OpenAI 原生加密内容的值当明文处理；当前实现保留 `gAAA...` 形状的 OpenAI encrypted content。
- 会递归移除嵌套明文 `encrypted_content`，避免上游误判。
- compact 请求本身不做这层归一化，避免破坏 Codex compact payload。
- OpenAI OAuth passthrough 请求如果缺少 `instructions`，会在上游前补默认 `You are a helpful coding assistant.`，避免模型切换后 Codex 请求缺少 instructions 被本地策略拒绝。

对应代码：

- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_oauth_passthrough_test.go`
- `backend/internal/service/openai_passthrough_normalization_test.go`

## 后端修改概览

| 文件 | 修改类型 | 作用 |
| --- | --- | --- |
| `backend/internal/handler/codex_models.go` | 新增 | 定义 Codex 模型目录响应结构，并返回 DeepSeek 两个 slug |
| `backend/internal/handler/gateway_handler.go` | 修改 | 在 `GatewayHandler.Models` 中识别 Codex 模型目录请求并返回 Codex schema |
| `backend/internal/server/routes/gateway.go` | 修改 | 为 `/backend-api/codex/models` 增加路由，复用模型目录处理逻辑 |
| `backend/internal/handler/gateway_models_test.go` | 修改 | 增加 Codex 模型目录请求的单元测试 |
| `backend/internal/pkg/apicompat/anthropic_to_responses_response.go` | 新增/修改 | 把 Anthropic Messages JSON/SSE 响应转换成 OpenAI Responses JSON/SSE |
| `backend/internal/pkg/apicompat/responses_to_anthropic_request.go` | 修改 | 把 OpenAI Responses 请求转换成 Anthropic Messages 请求 |
| `backend/internal/pkg/apicompat/anthropic_responses_test.go` | 修改 | 覆盖 Anthropic/Responses 双向转换、streaming 事件、tool use、reasoning、usage 语义 |
| `backend/internal/service/openai_gateway_responses_chat_fallback.go` | 修改 | DeepSeek 不支持原生 compact 时，通过 ChatCompletions fallback 合成 Codex remote compact v2 响应 |
| `backend/internal/service/openai_gateway_responses_chat_fallback_test.go` | 修改 | 增加 DeepSeek compact fallback 回归测试 |
| `backend/internal/service/openai_gateway_anthropic_responses.go` | 新增/修改 | OpenAI Responses -> Anthropic Messages -> OpenAI Responses 桥接，并支持 compact trigger |
| `backend/internal/handler/openai_gateway_handler.go` | 修改 | compact trigger 携带 `previous_response_id` 时不再被 HTTP 续链校验提前拒绝 |
| `backend/internal/service/openai_gateway_service.go` | 修改 | OpenAI upstream 前处理明文 `encrypted_content`，补 OAuth passthrough 默认 instructions |
| `backend/internal/service/openai_oauth_passthrough_test.go` | 修改 | 覆盖模型切换后的 instructions 和明文 compact summary passthrough |
| `backend/internal/service/openai_passthrough_normalization_test.go` | 新增 | 覆盖明文 `encrypted_content` 改写、嵌套清理、OpenAI 原生 encrypted content 保留 |
| `backend/internal/pkg/apicompat/types.go` | 修改 | 补充 `ResponsesOutput` 对 `compaction_summary` output 类型的说明 |
| `scripts/start-sub2api-built-canary.sh` | 新增 | 使用独立端口启动已编译前后端 canary，避免测试影响当前正式资源 |

## 文件级修改说明

### `backend/internal/handler/codex_models.go`

新增 Codex 专用模型目录定义和输出逻辑。

#### 新增类型

- `codexModelsResponse`
  - 对应 Codex 期望的顶层响应：`{ "models": [...] }`。
- `codexModelInfo`
  - 描述单个 Codex 模型的完整字段，包括 `slug`、`display_name`、`supported_reasoning_levels`、`shell_type`、`visibility`、`context_window` 等。
- `codexReasoningLevel`
  - 描述 `low`、`medium`、`high` 三档 reasoning level。
- `codexTruncationPolicy`
  - 描述 Codex truncation 策略。

#### 新增变量

- `deepseekCodexModels`
  - 当前提供给 Codex 的增量模型列表。
  - 包含：
    - `deepseek-v4-pro`
    - `deepseek-v4-flash`
  - 不包含 `gpt-5.5`，因为 `gpt-5.5` 由 Codex 内置模型目录保留；这里返回 DeepSeek 增量，避免覆盖内置模型。

#### 新增方法

- `isCodexModelsCatalogRequest(c *gin.Context) bool`
  - 判断当前 `/v1/models` 请求是否来自 Codex 模型目录拉取。
  - 当前识别条件是请求 query 中存在 `client_version`。
  - 这样普通 OpenAI-compatible `/v1/models` 请求仍走原有模型列表逻辑。

- `writeCodexModelsCatalog(c *gin.Context)`
  - 输出 Codex 期望的 `{ "models": [...] }` 响应。
  - 返回 DeepSeek 两个模型 slug。

- `newDeepseekCodexModel(slug, displayName, description string, priority int) codexModelInfo`
  - 统一构造 DeepSeek Codex 模型条目。
  - 当前默认字段包括：
    - `default_reasoning_level = "medium"`
    - `supported_reasoning_levels = low / medium / high`
    - `shell_type = "shell_command"`
    - `visibility = "list"`
    - `supported_in_api = true`
    - `apply_patch_tool_type = "freeform"`
    - `context_window = 128000`
    - `input_modalities = ["text"]`

### `backend/internal/handler/gateway_handler.go`

修改 `GatewayHandler.Models(c *gin.Context)`。

#### 新增逻辑

在方法入口增加：

```go
if isCodexModelsCatalogRequest(c) {
    writeCodexModelsCatalog(c)
    return
}
```

#### 实现效果

- 当 Codex 请求 `/v1/models?client_version=...` 时，后端返回 Codex 模型目录 schema。
- 当普通 OpenAI-compatible 客户端请求 `/v1/models` 时，不带 `client_version`，仍走原有逻辑：
  - 根据 API key / group / platform / model mapping 返回模型列表。
  - 不破坏原本的 OpenAI-compatible model list 行为。

## `backend/internal/server/routes/gateway.go`

修改 Codex direct route 组：`/backend-api/codex`。

#### 新增路由

```go
codexDirect.GET("/models", h.Gateway.Models)
```

#### 实现效果

- 支持 Codex 访问 `/backend-api/codex/models` 获取同一份模型目录。
- 与 `/v1/models?client_version=...` 使用同一套 handler，避免两套逻辑不一致。
- 该路由仍复用已有中间件：
  - body limit
  - client request id
  - ops error logger
  - endpoint normalization
  - API key auth
  - group/platform 校验

### `backend/internal/handler/gateway_models_test.go`

新增 Codex 模型目录单元测试。

#### 新增测试结构

- `codexModelsResponseForTest`
- `codexModelItemForTest`

用于解析 Codex schema 响应。

#### 新增测试方法

- `TestGatewayModels_CodexCatalogRequestReturnsDeepSeekModels`

验证内容：

- 请求 `/v1/models?client_version=0.140.0` 返回 HTTP 200。
- 响应模型 slug 顺序为：
  - `deepseek-v4-pro`
  - `deepseek-v4-flash`
- 验证关键 Codex 字段：
  - `display_name`
  - `default_reasoning_level`
  - `shell_type`
  - `visibility`
  - `supported_in_api`
  - `supports_parallel_tool_calls`

#### 新增辅助方法

- `codexModelSlugsForTest(models []codexModelItemForTest) []string`
  - 从测试响应中提取 slug 列表，便于断言。

### `backend/internal/pkg/apicompat/anthropic_to_responses_response.go`

新增 Anthropic Messages 响应到 OpenAI Responses 响应的转换层。

#### 非流式转换

`AnthropicToResponsesResponse(resp *AnthropicResponse) *ResponsesResponse` 负责把上游 Messages 响应包装成 Responses API 响应。

转换规则：

- 响应 `id` 复用 Anthropic `message.id`；如果为空则生成 `resp_*`。
- `object` 固定为 `response`。
- Anthropic `text` content block 合并为一个 Responses `message` output item。
- Anthropic `thinking` content block 转成 Responses `reasoning` output item，摘要写入 `summary[].text`。
- Anthropic `tool_use` content block 转成 Responses `function_call` output item：
  - `tool_use.id` -> Responses `call_id`
  - `tool_use.name` -> `name`
  - `tool_use.input` -> `arguments`
- 如果上游没有任何可输出 content，则补一个空 assistant message，保证 Responses `output` 不为空。
- `stop_reason=max_tokens` 转成 `status=incomplete`，并设置 `incomplete_details.reason=max_output_tokens`。
- `stop_reason=end_turn/tool_use/stop_sequence` 转成 `status=completed`。

usage 语义差异：

- Anthropic `input_tokens` 不含 cache read/cache creation。
- OpenAI Responses `input_tokens` 语义包含 cached tokens。
- 转换时会计算：

```text
Responses input_tokens =
  Anthropic input_tokens
  + cache_read_input_tokens
  + cache_creation_input_tokens
```

并在 `input_tokens_details.cached_tokens` 中记录 `cache_read_input_tokens`。

#### 流式转换

`AnthropicEventToResponsesEvents(evt, state)` 通过 `AnthropicEventToResponsesState` 把 Anthropic SSE 逐条转成 Responses SSE。

主要事件映射：

- `message_start` -> `response.created`
- `content_block_start(type=text)` -> `response.output_item.added` + `response.content_part.added`
- `content_block_delta(type=text_delta)` -> `response.output_text.delta`
- `content_block_start(type=thinking)` -> `response.output_item.added` + `response.reasoning_summary_part.added`
- `content_block_delta(type=thinking_delta)` -> `response.reasoning_summary_text.delta`
- `content_block_start(type=tool_use)` -> `response.output_item.added(function_call)`
- `content_block_delta(type=input_json_delta)` -> `response.function_call_arguments.delta`
- `content_block_stop` -> 对应的 `*.done` 事件和 `response.output_item.done`
- `message_stop` -> `response.completed`

`FinalizeAnthropicResponsesStream(state)` 用于上游异常结束或缺少 `message_stop` 时补齐关闭事件，避免客户端一直等待 terminal event。

### `backend/internal/pkg/apicompat/responses_to_anthropic_request.go`

负责把 OpenAI Responses 请求转换成 Anthropic Messages 请求，是 Anthropic-compatible upstream 链路的请求半边。

核心职责：

- Responses `instructions` / developer message -> Anthropic `system`。
- Responses user/assistant message -> Anthropic `messages[]`。
- Responses `function_call` / `function_call_output` -> Anthropic `tool_use` / `tool_result`。
- Responses tools -> Anthropic tools schema。
- Responses `reasoning` summary -> Anthropic `thinking` block。
- 将 Codex/OpenAI 请求中的模型、流式标志、max output tokens 等字段映射到 Anthropic Messages 语义。

这部分与 `AnthropicToResponsesResponse` 组合后，形成完整的：

```text
Responses request -> Anthropic request -> Anthropic response -> Responses response
```

### `backend/internal/service/openai_gateway_responses_chat_fallback.go`

补充 DeepSeek 第三方 compact fallback。

#### 背景

DeepSeek 第三方上游不支持原生 `/responses/compact`。Codex 调用 Sub2API 的 `/v1/responses/compact` 时，Sub2API 可以向 DeepSeek 上游发送 `/v1/chat/completions` 生成 compact 摘要，但最终返回给 Codex 的响应必须符合 Codex remote compact v2 schema。

原失败信息：

```text
remote compaction v2 expected exactly one compaction output item, got 0 from 2 output items
```

原因是原 ChatCompletions fallback 会调用 `ChatCompletionsResponseToResponses`，输出普通 Responses item，例如：

- `type="reasoning"`
- `type="message"`

Codex remote compact v2 不把这些当 compact 结果；它期望 `output` 中有且只有一个 `compaction_summary` item。Codex TUI 的 `/compact` 在 v0.141.0 会打普通 `/v1/responses`，并在 `input` 末尾追加 `{"type":"compaction_trigger"}`，因此网关需要同时识别 `/responses/compact` 路径和该 trigger item。

```json
{
  "type": "compaction_summary",
  "encrypted_content": "..."
}
```

#### 修改逻辑

`bufferChatCompletionsAsResponses` 在 ChatCompletions 非流式返回转成 Responses 后，新增 compact 请求判断：

```go
responsesResp := apicompat.ChatCompletionsResponseToResponses(&ccResp, originalModel)
if compactRequest {
    rewriteChatFallbackResponsesAsCompact(responsesResp)
}
```

#### 新增方法

- `rewriteChatFallbackResponsesAsCompact(response *apicompat.ResponsesResponse)`
  - 只在 `/responses/compact` 路径或 `/v1/responses` + `compaction_trigger` 请求中使用。
  - 把普通 `reasoning` / `message` outputs 替换成单个 `type="compaction_summary"` output。
  - 把响应 `object` 设置为 `response.compaction`，对齐原生 compact 响应形状。

- `compactEncryptedContentFromResponsesOutputs(outputItems []apicompat.ResponsesOutput) string`
  - 优先从 assistant `message.content[].text` 提取摘要正文。
  - 如果没有 message 文本，再回退到 `reasoning.summary[].text`。
  - 这样 DeepSeek 返回 reasoning + content 两个 item 时，最终仍只给 Codex 一个 compact summary item。

#### 行为结果

DeepSeek compact fallback 返回形状变为：

```json
{
  "object": "response.compaction",
  "output": [
    {
      "type": "compaction_summary",
      "encrypted_content": "compact summary"
    }
  ]
}
```

普通 `/v1/responses` fallback 不受影响，仍返回标准 Responses `message` / `reasoning`。

### `backend/internal/service/openai_gateway_anthropic_responses.go`

新增 DeepSeek Anthropic-compatible upstream 的 Responses 桥接，并补充 compact fallback。

#### 通用请求链路

`forwardResponsesViaAnthropicResponses` 是 OpenAI Responses 请求走 Anthropic-compatible upstream 的入口。

处理流程：

1. 解析客户端 `/v1/responses` 请求为 `apicompat.ResponsesRequest`。
2. 记录客户端是否要求 `stream`，并判断当前请求是否为 compact。
3. 调用 `apicompat.ResponsesToAnthropicRequest` 转为 Anthropic Messages 请求。
4. 根据账号模型映射把 `deepseek-v4-pro/flash` 映射到真实上游模型，例如 `deepseek-reasoner` / `deepseek-chat`。
5. 构造上游请求：
   - URL：`<base_url>/v1/messages?beta=true`
   - auth header：`x-api-key`
   - 默认 `anthropic-version: 2023-06-01`
   - 默认 `accept: text/event-stream`
6. 上游按 Anthropic SSE 返回后，Sub2API 再转成 OpenAI Responses JSON 或 SSE。

非 compact 情况下：

- 客户端请求 `stream=true` 时，逐条把 Anthropic SSE 转成 Responses SSE。
- 客户端请求 `stream=false` 时，buffer 上游 SSE，组装完整 `AnthropicResponse`，再调用 `AnthropicToResponsesResponse` 输出普通 Responses JSON。
- 返回给客户端的 `model` 保持原始请求模型，例如 `deepseek-v4-flash`；计费和上游模型记录使用映射后的真实模型。

#### 触发场景

Codex TUI v0.141.0 的 `/compact` 可能走普通 `/v1/responses`，并把 `input` 写成只有：

```json
[
  { "type": "compaction_trigger" }
]
```

这种请求没有可转换成 Anthropic `/v1/messages` 的 user/assistant message。原转换结果会序列化成：

```json
{
  "messages": null
}
```

DeepSeek Anthropic-compatible 上游会返回：

```text
messages: invalid type: null, expected a sequence
```

#### 修改逻辑

- 在发送 Anthropic upstream 前提前判断 compact 请求：
  - `/v1/responses/compact`
  - `/v1/responses` + `input[].type == "compaction_trigger"`
- 如果 compact 请求没有任何 Anthropic message，则补一个最小 user message，避免 `messages:null`。
- compact 流式请求不直接透传 reasoning/message SSE，而是先 buffer Anthropic SSE，再重写成：

```json
{
  "object": "response.compaction",
  "output": [
    {
      "type": "compaction_summary",
      "encrypted_content": "..."
    }
  ]
}
```

实现边界：

- compact 请求即使客户端传 `stream=true`，上游仍先以 Anthropic SSE 被整体读取，因为最终需要把多个上游 block 合并成单个 `compaction_summary`。
- buffer 完成后先使用通用 `AnthropicToResponsesResponse` 得到 Responses 结构，再复用 `rewriteChatFallbackResponsesAsCompact` 重写成 compact 响应。
- 输出 SSE 时使用 `writeAnthropicResponsesAsSSE`，事件序列是 `response.created` -> `response.output_item.added` -> `response.output_item.done` -> `response.completed`。
- 该逻辑只影响 compact 请求；普通 DeepSeek Responses 对话仍保留 reasoning/message/function_call 等标准 Responses output。

### `backend/internal/handler/openai_gateway_handler.go`

补充 compact trigger + `previous_response_id` 的兼容。

#### 背景

从 `gpt-5.5` 会话切换到 `deepseek-v4-pro` 后，Codex 的 compact 请求可能携带上一轮 `previous_response_id`。普通 HTTP `/v1/responses` 续链仍然不支持该字段，但 compact fallback 不依赖上游续链 ID，可以忽略它并按当前 input 生成 compact summary。

#### 修改逻辑

- `previous_response_id` 仍然会校验不能是 `msg_*`。
- 非 compact 请求继续返回原来的 HTTP 400：
  - `previous_response_id is only supported on Responses WebSocket v2`
- compact 请求不再被提前拒绝，后续 DeepSeek fallback 会忽略 `previous_response_id` 并返回 `response.compaction`。

这个修改只解决 Codex compact 场景，不表示 HTTP `/v1/responses` 已经支持普通续链。普通对话如果要依赖 `previous_response_id`，仍需要走 Responses WebSocket v2。

### `backend/internal/service/openai_gateway_service.go`

补充模型切换后的 OpenAI upstream 请求归一化。

#### 明文 `encrypted_content` 归一化

从 DeepSeek compact 切回 `gpt-5.5` 后，Codex 可能把 DeepSeek compact fallback 生成的明文摘要放在 `encrypted_content` 字段里继续发送。OpenAI upstream 会把这个字段当作官方加密内容校验，导致：

```text
invalid_encrypted_content
```

新增逻辑：

- `normalizePlaintextEncryptedContentInOpenAIBody`
- `normalizePlaintextEncryptedContentInOpenAIRequestBodyMap`
- `normalizePlaintextEncryptedContentInputItem`
- `stripPlaintextEncryptedContentRecursive`

处理规则：

- 如果 input item 带 `encrypted_content`，且内容不像 OpenAI 原生加密内容，则把整个 item 改写为普通 user message。
- 改写后的 message 文本前缀为 `Previous compacted conversation summary:`，保留原摘要文本，作为可读上下文交给 OpenAI 模型。
- 如果 `encrypted_content` 出现在嵌套 content part 中，也会递归删除，避免 OpenAI upstream 继续尝试解密。
- `isLikelyOpenAIEncryptedContent` 当前以 `gAAA` 前缀作为 OpenAI 原生 encrypted content 的保留条件。
- compact 请求本身跳过这层归一化，避免破坏 Codex remote compact payload。

该逻辑覆盖两条 OpenAI 转发路径：

- native/HTTP OpenAI Responses 路径：上游前对 request body map 做归一化。
- OpenAI OAuth passthrough 路径：passthrough 前对 body 做归一化。

#### OAuth passthrough instructions 默认值

Codex 在模型切换后，发往 OpenAI OAuth passthrough 的请求可能缺少 `instructions`。本地策略原本会拒绝 Codex passthrough 请求中的空 instructions。

新增 `ensureOpenAIPassthroughInstructions`：

- 如果 `instructions` 已存在且非空，保持不变。
- 如果缺失或为空，补：

```text
You are a helpful coding assistant.
```

随后再执行原来的 passthrough body 归一化与本地策略校验。

### `backend/internal/service/openai_passthrough_normalization_test.go`

新增明文 `encrypted_content` 归一化单元测试。

覆盖场景：

- `compaction_summary.encrypted_content` 为明文时，改写成普通 user message。
- `reasoning.encrypted_content` 为明文时，改写成普通 user message。
- 嵌套 content part 中的明文 `encrypted_content` 会被移除。
- 未知 history item 只要带明文 `encrypted_content`，也会改写成普通 user message。
- OpenAI 原生 `gAAA...` encrypted content 保持原样，不被错误清理。

### `backend/internal/service/openai_oauth_passthrough_test.go`

补充 OpenAI OAuth passthrough 的模型切换回归测试。

新增/补充测试：

- `TestOpenAIGatewayService_OAuthPassthrough_MissingInstructionsAddedBeforeUpstream`
  - 验证模型切换后缺少 `instructions` 时，上游前会补默认 instructions。
- `TestOpenAIGatewayService_OAuthPassthrough_CompactionSummaryPlaintextDoesNotReachEncryptedContent`
  - 验证明文 compact summary 不再以 `encrypted_content` 发送到 OpenAI。
- `TestOpenAIGatewayService_OAuthPassthrough_ReasoningPlaintextDoesNotReachEncryptedContent`
  - 验证明文 reasoning summary 不再以 `encrypted_content` 发送到 OpenAI。

### `backend/internal/service/openai_gateway_responses_chat_fallback_test.go`

新增测试：

- `TestForwardResponses_CompactFallbackReturnsSingleCompactionItem`
- `TestForwardResponses_CompactionTriggerStreamingFallbackBuffersUpstreamJSON`

验证内容：

- `/v1/responses/compact` 仍转发到上游 `/v1/chat/completions`。
- 上游返回 `reasoning_content` + `content` 时，Sub2API 下游响应只包含一个 output item。
- `output.0.type == "compaction_summary"`。
- `output.0.encrypted_content == "compact summary"`。
- 不再出现 `output.0.content` / `output.0.summary` / `output.1`。

### `backend/internal/service/openai_gateway_anthropic_responses_test.go`

新增测试：

- `TestOpenAIForwardAnthropicResponsesCompactionTriggerOnlyAddsFallbackMessage`
- `TestOpenAIForwardAnthropicResponsesCompactionTriggerStreamingEmitsSingleCompactionItem`

验证内容：

- `input=[{"type":"compaction_trigger"}]` 不再向上游发送 `messages:null`。
- 上游请求至少包含一个 Anthropic user message。
- 下游 SSE 返回 `response.compaction` 和单个 `compaction_summary`。

### `backend/internal/pkg/apicompat/types.go`

补充 `ResponsesOutput.Type` 注释：

```go
Type string `json:"type"` // "message" | "reasoning" | "function_call" | "web_search_call" | "compaction_summary"
```

这是文档性补充，避免后续维护者误认为 `compaction_summary` 不是合法 Responses output item。

## 行为结果

修改完成后，Codex 模型列表行为如下：

- `codex debug models` 能看到：
  - `gpt-5.5`
  - `deepseek-v4-pro`
  - `deepseek-v4-flash`
- `/v1/models?client_version=0.140.0` 返回 DeepSeek 增量模型目录。
- 普通 `/v1/models` 请求不受 Codex schema 影响。
- 不需要用户手动维护 `model_catalog_json`。
- 不会因为自定义 catalog 覆盖掉 Codex 内置 `gpt-5.5`。
- DeepSeek 第三方 compact 不依赖上游原生 compact 接口；Sub2API 通过 ChatCompletions/Anthropic fallback 合成 Codex 可解析的 `compaction_summary` item。
- Anthropic-compatible upstream 可以服务 OpenAI Responses 客户端：
  - 下游仍看到 `/v1/responses` JSON/SSE。
  - 上游实际走 `/v1/messages?beta=true`。
  - text、thinking、tool_use、usage、stop_reason 都会转回 Responses 语义。
- Codex 模型切换后的 compact 和继续对话可以跨方向工作：
  - `gpt-5.5 -> deepseek-v4-pro/flash` compact：允许 compact 请求携带 `previous_response_id`，但普通 HTTP 续链仍不放行。
  - `deepseek-v4-pro/flash -> gpt-5.5` 继续对话：明文 compact summary 不再以 `encrypted_content` 送到 OpenAI upstream，避免 `invalid_encrypted_content`。

## 配套脚本说明

虽然不是后端业务代码，本次还增加了安全重载脚本：

- `scripts/reload-sub2api-backend-codex-models.sh`
- `scripts/start-sub2api-built-canary.sh`

### `scripts/reload-sub2api-backend-codex-models.sh`

该脚本用于避免 Sub2API 作为当前 Codex 底座时被粗暴重启导致会话中断。流程是：

1. 编译后端二进制到 `/tmp/sub2api-codex-models-reload/sub2api-server`。
2. 找一个空闲 canary 端口，默认从 `18080` 开始。
3. 在 canary 端口启动新后端。
4. 健康检查 `/health`。
5. 请求 `/v1/models?client_version=0.140.0`，确认包含两个 DeepSeek slug。
6. canary 通过后，用 `tmux respawn-pane` 只重载 `sub2api:backend` pane。
7. 验证 active backend `8080` 健康。
8. 再次验证 active backend 模型目录。
9. 更新 `~/code/.service_ports` 中的 `sub2api` 状态。

### `scripts/start-sub2api-built-canary.sh`

该脚本用于在不占用正式端口的情况下，启动已编译产物进行 canary 验证。

默认行为：

- backend 默认端口：`18081`。
- frontend 默认端口：`12334`。
- tmux session 默认名：`sub2api-built-canary`。
- 支持 `--replace` 重建同名测试 session。
- 会检测端口占用，避免和当前正式 backend `8080` / frontend `12333` 冲突。
- 使用随机生成的 `ADMIN_PASSWORD` 和 `JWT_SECRET`，避免把固定敏感值写入脚本。
- 数据库密码默认仍按本地开发约定读取 `DATABASE_PASSWORD`，未设置时使用本地默认值。

典型用途：

```bash
BACKEND_PORT=18081 FRONTEND_PORT=12334 ./scripts/start-sub2api-built-canary.sh --replace
```

## 前一次未改对的问题复盘

### 问题 1：只从 Codex config 侧处理不够

前一版思路偏向在 Codex 本地配置里补模型目录，例如 `model_catalog_json` / `model-catalogs`。这个方向不符合最终目标：用户不希望每个用户都维护一份本地 catalog。

修正后：把模型目录能力放到 Sub2API 后端网关，所有走这个 base URL 的 Codex 客户端都能拿到同一份模型增量。

### 问题 2：容易覆盖内置模型

如果用完整 catalog 覆盖式配置，很容易导致 Codex 原本已有的模型，例如 `gpt-5.5`，在 `/model` 里消失。

修正后：后端只返回 DeepSeek 增量模型，不把 `gpt-5.5` 写进自定义 catalog。Codex 客户端会保留内置目录并合并 DeepSeek slug，因此最终同时存在 `gpt-5.5`、`deepseek-v4-pro`、`deepseek-v4-flash`。

### 问题 3：普通 `/v1/models` 与 Codex catalog schema 混淆

OpenAI-compatible `/v1/models` 常规响应是 `{ "object": "list", "data": [...] }`，而 Codex 模型目录请求需要 `{ "models": [...] }`。如果直接改普通模型列表，会破坏其他客户端。

修正后：通过 `client_version` query 区分 Codex 模型目录请求；只有 Codex catalog 请求走 `writeCodexModelsCatalog`，其他客户端仍走原有 `GatewayHandler.Models` 逻辑。

### 问题 4：缺少 `/backend-api/codex/models` 直连路由

只处理 `/v1/models?client_version=...` 还不完整，因为 Codex 相关流量也可能走 `/backend-api/codex/*` 直连路径。

修正后：新增 `/backend-api/codex/models`，复用 `h.Gateway.Models`，保持两条入口行为一致。

### 问题 5：不能直接重启当前 Sub2API

当前环境中 Codex 自己可能通过本机 `http://127.0.0.1:12333/v1` 走 Sub2API。直接停止或重启 active backend 可能打断当前会话。

修正后：先用 canary 端口验证，再用脚本只重载 backend tmux pane，并在重载后检查 active backend 和模型目录，降低中断风险。

### 问题 6：把 compact 问题误判成上游必须支持原生 compact

DeepSeek 第三方 API 确实不支持原生 `/responses/compact`，但 Codex 的第三方模型 compact 能力不要求上游也提供同名接口。正确做法是：Sub2API 接住 Codex 的 `/v1/responses/compact` 或 `/v1/responses` + `compaction_trigger`，再用上游 `/v1/chat/completions` / Anthropic Messages 生成摘要，最后把响应包装成 Codex remote compact v2 期望的单个 `compaction_summary` output item。

修正后：DeepSeek compact 请求走 ChatCompletions/Anthropic fallback，但返回给 Codex 的 `output` 被重写为单个 `type="compaction_summary"` item，避免 `got 0 from 2 output items`。

### 问题 7：只修 compact，没有处理模型切回 OpenAI 后的上下文

DeepSeek compact fallback 为了满足 Codex remote compact v2，会把摘要放到 `compaction_summary.encrypted_content`。这个字段名对 Codex 是 compact 载体，但对 OpenAI upstream 是需要解密校验的官方 encrypted content。

如果用户从 DeepSeek compact 后切回 `gpt-5.5` 并继续发送普通对话，OpenAI 会尝试解密这段明文摘要并返回 `invalid_encrypted_content`。

修正后：非 compact OpenAI 请求上游前会把明文 `encrypted_content` 转成普通 user message，真正的 OpenAI `gAAA...` encrypted content 保留不动。

### 问题 8：Anthropic trigger-only compact 会转出 `messages:null`

Codex 的 `/compact` 有时只发送 `input=[{"type":"compaction_trigger"}]`，这没有任何可转换成 Anthropic Messages 的 user/assistant message。直接转换会得到 `messages:null`，DeepSeek Anthropic-compatible 上游会拒绝。

修正后：compact trigger-only 请求会补一个最小 user message，要求上游生成 compact summary；下游仍只返回单个 `compaction_summary`。

## 验证命令

已执行并通过：

```bash
cd backend && go test ./internal/handler -run GatewayModels
cd backend && go test ./internal/server/routes -run Gateway
cd backend && go test ./internal/service -run TestForwardResponses_CompactFallbackReturnsSingleCompactionItem -tags unit -count=1
cd backend && go test ./internal/service -run 'TestForwardResponses_|TestOpenAI.*Compact|Test.*ResponsesCompact|Test.*Compact.*Mapping|Test.*NormalizeOpenAICompact' -tags unit -count=1
cd backend && go test ./internal/service -tags unit -count=1
cd backend && go test ./internal/pkg/apicompat -tags unit -count=1
cd backend && go test ./internal/service -run 'TestNormalizePlaintextEncryptedContent|TestOpenAIGatewayService_OAuthPassthrough_(CompactionSummaryPlaintext|ReasoningPlaintext|MissingInstructionsAddedBeforeUpstream)|TestOpenAIForwardAnthropicResponses.*Compact|TestBufferChatCompletionsAsResponses.*Compact|TestNormalizeOpenAICompactRequestBody|TestHasOpenAIResponsesCompactionTrigger' -tags unit -count=1
make build
```

运行后验证：

```bash
curl -fsS -H "Authorization: Bearer <token>" \
  "http://127.0.0.1:8080/v1/models?client_version=0.140.0"

curl -fsS -H "Authorization: Bearer <token>" \
  "http://127.0.0.1:12333/v1/models?client_version=0.140.0"

codex debug models
```

实际验证结果：

- backend `8080` 返回 `deepseek-v4-pro`、`deepseek-v4-flash`。
- frontend `12333` 返回 `deepseek-v4-pro`、`deepseek-v4-flash`。
- `codex debug models` 同时包含 `gpt-5.5`、`deepseek-v4-pro`、`deepseek-v4-flash`。

## 后续维护建议

如果后续要新增更多 Codex 可选模型，只需要修改：

- `backend/internal/handler/codex_models.go` 中的 `deepseekCodexModels`

并补充对应测试断言：

- `backend/internal/handler/gateway_models_test.go` 中的 `TestGatewayModels_CodexCatalogRequestReturnsDeepSeekModels`

不要优先让用户配置 `model_catalog_json`，除非是单用户临时实验；生产或多用户场景应继续由 Sub2API 后端统一提供模型目录。

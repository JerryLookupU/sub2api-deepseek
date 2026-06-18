# Codex DeepSeek 模型目录接入修改报告

日期：2026-06-19

## 背景

本次修改的目标是在不要求用户维护 `model_catalog_json` / `model-catalogs` 的前提下，让 Codex 的 `/model` 模型选择列表出现以下自定义模型：

- `deepseek-v4-pro`
- `deepseek-v4-flash`

同时保留 Codex 原有模型，例如 `gpt-5.5`。最终方案不是在用户本地配置中覆盖完整模型目录，而是在 Sub2API 后端为 Codex 模型目录请求返回增量模型目录，让 Codex 客户端与自身内置目录合并。

## 后端修改概览

| 文件 | 修改类型 | 作用 |
| --- | --- | --- |
| `backend/internal/handler/codex_models.go` | 新增 | 定义 Codex 模型目录响应结构，并返回 DeepSeek 两个 slug |
| `backend/internal/handler/gateway_handler.go` | 修改 | 在 `GatewayHandler.Models` 中识别 Codex 模型目录请求并返回 Codex schema |
| `backend/internal/server/routes/gateway.go` | 修改 | 为 `/backend-api/codex/models` 增加路由，复用模型目录处理逻辑 |
| `backend/internal/handler/gateway_models_test.go` | 修改 | 增加 Codex 模型目录请求的单元测试 |

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

## 配套脚本说明

虽然不是后端业务代码，本次还增加了安全重载脚本：

- `scripts/reload-sub2api-backend-codex-models.sh`

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

## 验证命令

已执行并通过：

```bash
cd backend && go test ./internal/handler -run GatewayModels
cd backend && go test ./internal/server/routes -run Gateway
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

# CLIProxyAPI 接口参考

本文档总结 `router-for-me/CLIProxyAPI` 对外可用的接口、鉴权方式和常见调用方法。

整理时间：2026-05-06
源码基准：`router-for-me/CLIProxyAPI` commit `ed1458aa6d3430ba59538aeb980b8934f0e80c1f`

> 说明：这里聚焦运行时 HTTP 接口和 Redis-compatible usage queue。Go SDK 的嵌入式接口只做简要说明，具体 SDK 用法建议继续看上游仓库 `docs/sdk-*.md`。

## 1. 基础地址和鉴权

默认端口来自示例配置：

```text
http://127.0.0.1:8317
```

### 推理接口鉴权

推理接口使用配置里的顶层 `api-keys`。可用的认证位置包括：

| 方式 | 示例 |
| --- | --- |
| `Authorization` | `Authorization: Bearer <API_KEY>` |
| `X-Api-Key` | `X-Api-Key: <API_KEY>` |
| `X-Goog-Api-Key` | `X-Goog-Api-Key: <API_KEY>` |
| 查询参数 | `?key=<API_KEY>` 或 `?auth_token=<API_KEY>` |

如果服务端没有配置任何访问密钥，源码里的认证中间件会保持旧行为：放行请求。

### Management API 鉴权

Management API 前缀：

```text
/v0/management
```

启用条件：

- 配置 `remote-management.secret-key`，或
- 环境变量 `MANAGEMENT_PASSWORD`，或
- 嵌入式服务通过本地 runtime password 启用。

所有 management 请求都需要管理密钥：

```bash
curl -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  http://127.0.0.1:8317/v0/management/config
```

也可以使用：

```text
X-Management-Key: <MANAGEMENT_KEY>
```

远程访问还需要配置允许远程管理。否则非 localhost 请求会被拒绝。认证失败过多会触发临时 IP 封禁。

## 2. 基础服务接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/` | 返回服务信息和少量 endpoint 提示 |
| `GET`, `HEAD` | `/healthz` | 健康检查，成功时 `{"status":"ok"}` |
| `GET` | `/management.html` | 内置管理面板静态页，可能被 `remote-management.disable-control-panel` 禁用 |
| `GET` | `/keep-alive` | 嵌入式 keep-alive 心跳接口，仅在 SDK/宿主启用时注册 |
| `GET` | `/anthropic/callback` | Claude OAuth 回调 |
| `GET` | `/codex/callback` | Codex/OpenAI OAuth 回调 |
| `GET` | `/google/callback` | Gemini OAuth 回调 |
| `GET` | `/antigravity/callback` | Antigravity OAuth 回调 |

`/keep-alive` 如果配置了本地密码，需要：

```text
Authorization: Bearer <LOCAL_PASSWORD>
```

或：

```text
X-Local-Password: <LOCAL_PASSWORD>
```

## 3. OpenAI 兼容接口

前缀：`/v1`

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/v1/models` | 模型列表。`User-Agent` 以 `claude-cli` 开头时走 Claude models handler，否则走 OpenAI models handler |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions 格式 |
| `POST` | `/v1/completions` | OpenAI Completions 格式 |
| `POST` | `/v1/images/generations` | 图片生成 |
| `POST` | `/v1/images/edits` | 图片编辑 |
| `GET` | `/v1/responses` | OpenAI Responses WebSocket/SSE 兼容入口 |
| `POST` | `/v1/responses` | OpenAI Responses API |
| `POST` | `/v1/responses/compact` | Codex/Responses compact |
| `POST` | `/v1/messages` | Claude Messages 风格接口 |
| `POST` | `/v1/messages/count_tokens` | Claude Messages token 计数 |

示例：

```bash
curl -sS http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.5",
    "messages": [
      {"role": "user", "content": "hello"}
    ],
    "stream": true
  }'
```

## 4. Codex direct alias

前缀：`/backend-api/codex`

这些路径用于兼容 Codex CLI 的 `chatgpt_base_url` 风格。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/backend-api/codex/responses` | Responses WebSocket/SSE 入口 |
| `POST` | `/backend-api/codex/responses` | Responses API |
| `POST` | `/backend-api/codex/responses/compact` | compact |

示例：

```bash
curl -sS http://127.0.0.1:8317/backend-api/codex/responses \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.5","input":"hello"}'
```

## 5. Gemini 兼容接口

前缀：`/v1beta`

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/v1beta/models` | Gemini 模型列表 |
| `POST` | `/v1beta/models/*action` | Gemini model-scoped action，例如 generateContent、streamGenerateContent |
| `GET` | `/v1beta/models/*action` | Gemini model-scoped GET action |

示例：

```bash
curl -sS "http://127.0.0.1:8317/v1beta/models/gemini-2.5-pro:generateContent" \
  -H "x-goog-api-key: <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {"parts": [{"text": "hello"}]}
    ]
  }'
```

### Gemini CLI internal endpoint

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/v1internal:*` | Gemini CLI 内部接口，源码以 `/v1internal:method` 形式注册 |

使用条件：

- 配置 `enable-gemini-cli-endpoint: true`
- 请求必须来自 localhost
- 请求 Host 必须是 `127.0.0.1`

源码中特殊处理：

- `/v1internal:generateContent`
- `/v1internal:streamGenerateContent`

其他 `/v1internal:*` 会转发到 `https://cloudcode-pa.googleapis.com`。

## 6. WebSocket 路由

嵌入式服务可以通过 SDK 注册 WebSocket upgrade handler：

| 方法 | 默认路径 | 用途 |
| --- | --- | --- |
| `GET` | `/v1/ws` | WebSocket upgrade，实际路径可由宿主覆盖 |

鉴权由配置 `ws-auth` 控制：

- `ws-auth: true` 时需要普通推理 API key
- `ws-auth: false` 时不走推理鉴权

## 7. Amp CLI / Provider 路由

CLIProxyAPI 集成了 Amp CLI/IDE 扩展支持。相关路由分两类：Amp management proxy 和 provider aliases。

### 7.1 Amp management proxy

这些接口主要代理到 Amp upstream，用于 Amp CLI 的 OAuth、用户、线程、文档、设置等流程。

`/api` 前缀下：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `ANY` | `/api/internal`, `/api/internal/*path` | Amp internal proxy |
| `ANY` | `/api/user`, `/api/user/*path` | Amp user proxy |
| `ANY` | `/api/auth`, `/api/auth/*path` | Amp auth proxy |
| `ANY` | `/api/meta`, `/api/meta/*path` | Amp meta proxy |
| `ANY` | `/api/ads` | Amp ads proxy |
| `ANY` | `/api/telemetry`, `/api/telemetry/*path` | Amp telemetry proxy |
| `ANY` | `/api/threads`, `/api/threads/*path` | Amp threads proxy |
| `ANY` | `/api/otel`, `/api/otel/*path` | Amp otel proxy |
| `ANY` | `/api/tab`, `/api/tab/*path` | Amp tab proxy |

根路径下：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/threads`, `/threads/*path` | Amp threads |
| `GET` | `/docs`, `/docs/*path` | Amp docs |
| `GET` | `/settings`, `/settings/*path` | Amp settings |
| `GET` | `/threads.rss` | Amp RSS |
| `GET` | `/news.rss` | Amp RSS |
| `ANY` | `/auth`, `/auth/*path` | Amp OAuth 登录流程 |

Google 特殊桥接：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `ANY` | `/api/provider/google/v1beta1/*path` | POST 且路径包含 `/models/` 时先走本地 Gemini bridge，可 fallback 到 Amp upstream；其他情况直接代理 |

安全和鉴权：

- `/api/*` 和 `/api/provider/*` 使用普通推理 API key。
- Amp proxy 会移除客户端的 `Authorization` / `X-Api-Key`，再注入配置里的 Amp upstream key。
- `ampcode.restrict-management-to-localhost` 可限制 Amp management proxy 只能 localhost 访问。

### 7.2 Provider aliases

前缀：

```text
/api/provider/{provider}
```

`{provider}` 常见值包括 `openai`、`anthropic`、`google`，也可以是 OpenAI-compatible provider 名。路径选择协议表面，但最终执行器仍由请求里的 model/alias 决定。若需要严格绑定后端，建议为不同后端设置唯一 alias 或 prefix。

| 方法 | 路径 | 协议表面 |
| --- | --- | --- |
| `GET` | `/api/provider/{provider}/models` | models |
| `POST` | `/api/provider/{provider}/chat/completions` | OpenAI chat |
| `POST` | `/api/provider/{provider}/completions` | OpenAI completions |
| `POST` | `/api/provider/{provider}/responses` | OpenAI responses |
| `GET` | `/api/provider/{provider}/v1/models` | models |
| `POST` | `/api/provider/{provider}/v1/chat/completions` | OpenAI chat |
| `POST` | `/api/provider/{provider}/v1/completions` | OpenAI completions |
| `POST` | `/api/provider/{provider}/v1/responses` | OpenAI responses |
| `POST` | `/api/provider/{provider}/v1/messages` | Claude messages |
| `POST` | `/api/provider/{provider}/v1/messages/count_tokens` | Claude token count |
| `GET` | `/api/provider/{provider}/v1beta/models` | Gemini models |
| `POST` | `/api/provider/{provider}/v1beta/models/*action` | Gemini action |
| `GET` | `/api/provider/{provider}/v1beta/models/*action` | Gemini GET action |

示例：

```bash
curl -sS http://127.0.0.1:8317/api/provider/anthropic/v1/messages \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4",
    "max_tokens": 1024,
    "messages": [{"role":"user","content":"hello"}]
  }'
```

## 8. Management API 通用约定

所有路径都以 `/v0/management` 为前缀。

常见请求体规则：

| 类型 | 写入方式 |
| --- | --- |
| bool/int/string 单值 | `PUT`/`PATCH` 使用 `{"value": true}`、`{"value": 3}` 或 `{"value":"..."}` |
| string list | `PUT` 可传数组 `["a","b"]` 或 `{"items":["a","b"]}` |
| string list patch | `{"index":0,"value":"new"}` 或 `{"old":"a","new":"b"}` |
| string list delete | `DELETE ?index=0` 或 `DELETE ?value=a` |
| provider key list | `PUT` 通常传对象数组或 `{"items":[...]}`；`PATCH` 用 `{"index":0,"value":{...}}` 或对应 match 字段 |
| 成功响应 | 多数写操作返回 `{"status":"ok"}`，少数接口返回 `{"ok":true}` 或资源详情 |

### 8.1 配置和运行时开关

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/config` | 返回当前配置 JSON |
| `GET` | `/config.yaml` | 返回原始 YAML，保留注释和格式 |
| `PUT` | `/config.yaml` | 用请求体里的 YAML 覆盖配置文件，写入前会校验 |
| `GET` | `/latest-version` | 查询 GitHub latest release |
| `GET`, `PUT`, `PATCH` | `/debug` | 调试日志开关，body `{"value": true}` |
| `GET`, `PUT`, `PATCH` | `/logging-to-file` | 文件日志开关 |
| `GET`, `PUT`, `PATCH` | `/logs-max-total-size-mb` | 日志总大小限制，int |
| `GET`, `PUT`, `PATCH` | `/error-logs-max-files` | error log 文件数量限制，int |
| `GET`, `PUT`, `PATCH` | `/usage-statistics-enabled` | usage queue 采集开关，bool |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/proxy-url` | 全局代理 URL |
| `GET`, `PUT`, `PATCH` | `/request-log` | 请求日志开关 |
| `GET`, `PUT`, `PATCH` | `/ws-auth` | WebSocket 鉴权开关 |
| `GET`, `PUT`, `PATCH` | `/request-retry` | 请求重试次数 |
| `GET`, `PUT`, `PATCH` | `/max-retry-interval` | 最大重试间隔 |
| `GET`, `PUT`, `PATCH` | `/force-model-prefix` | 强制模型 prefix 开关 |
| `GET`, `PUT`, `PATCH` | `/routing/strategy` | 路由策略，支持 `round-robin` / `fill-first` |

示例：

```bash
curl -sS -X PUT http://127.0.0.1:8317/v0/management/usage-statistics-enabled \
  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"value": true}'
```

### 8.2 API key 和 provider 配置

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/api-keys` | 顶层访问 API key 列表 |
| `GET` | `/api-key-usage` | 按 provider 和 API key 聚合近期成功/失败桶 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/gemini-api-key` | Gemini API key 配置 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/claude-api-key` | Claude API key 配置 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/codex-api-key` | Codex/OpenAI API key 配置 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/openai-compatibility` | OpenAI-compatible provider 配置 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/vertex-api-key` | Vertex-compatible key 配置 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/oauth-excluded-models` | OAuth provider 排除模型 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/oauth-model-alias` | OAuth provider 模型 alias |

顶层 API key 示例：

```bash
curl -sS -X PUT http://127.0.0.1:8317/v0/management/api-keys \
  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '["key-a","key-b"]'
```

### 8.3 Usage queue 和用量相关

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/usage-queue?count=100` | 从内存 usage queue 弹出最多 `count` 条记录，默认 1 |
| `GET`, `PUT`, `PATCH` | `/usage-statistics-enabled` | 控制是否把请求用量写入 usage queue |

`/usage-queue` 返回 JSON 数组，每个元素通常是请求级用量记录：

```json
{
  "timestamp": "2026-05-06T12:00:00Z",
  "latency_ms": 1234,
  "source": "codex",
  "auth_index": "...",
  "tokens": {
    "input_tokens": 100,
    "output_tokens": 20,
    "reasoning_tokens": 0,
    "cached_tokens": 80,
    "total_tokens": 120
  },
  "failed": false,
  "provider": "codex",
  "model": "gpt-5.5",
  "alias": "gpt-5.5",
  "endpoint": "GET /v1/responses",
  "auth_type": "api_key",
  "api_key": "...",
  "request_id": "..."
}
```

注意：

- queue 是内存队列，不是持久化数据库。
- `usage-statistics-enabled` 必须为 `true` 才会发布记录。
- management routes 必须启用，因为 queue 的 HTTP 和 Redis-compatible 读取都依赖 management key。
- `redis-usage-queue-retention-seconds` 默认 60，源码限制最大 3600 秒。
- `GET /usage-queue` 会弹出记录，读过就从队列移除。

### 8.4 日志接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/logs?after=<timestamp>&limit=<n>` | 增量读取文件日志，需要 `logging-to-file: true` |
| `DELETE` | `/logs` | 清空日志，保留/截断活动日志文件 |
| `GET` | `/request-error-logs` | 当 request-log 关闭时，列出 error request log 文件 |
| `GET` | `/request-error-logs/:name` | 下载指定 error request log 文件 |
| `GET` | `/request-log-by-id/:id` | 按 request ID 查找并下载请求日志 |

### 8.5 Quota exceeded 行为

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET`, `PUT`, `PATCH` | `/quota-exceeded/switch-project` | Gemini quota exceeded 时是否切换 project |
| `GET`, `PUT`, `PATCH` | `/quota-exceeded/switch-preview-model` | quota exceeded 时是否切换 preview model |

### 8.6 Amp 配置接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/ampcode` | 返回 AmpCode 配置块 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/ampcode/upstream-url` | Amp upstream URL |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/ampcode/upstream-api-key` | 默认 Amp upstream API key |
| `GET`, `PUT`, `PATCH` | `/ampcode/restrict-management-to-localhost` | 是否限制 Amp management proxy 只能 localhost |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/ampcode/model-mappings` | Amp 模型映射 |
| `GET`, `PUT`, `PATCH` | `/ampcode/force-model-mappings` | 是否强制使用模型映射 |
| `GET`, `PUT`, `PATCH`, `DELETE` | `/ampcode/upstream-api-keys` | 按客户端 API key 映射不同 Amp upstream key |

### 8.7 Auth files 和 OAuth 登录

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/auth-files` | 列出 auth 文件/运行时 auth 记录 |
| `GET` | `/auth-files/models?name=<name>` | 查询某个 auth 支持的模型 |
| `GET` | `/model-definitions/:channel` | 查询静态模型定义，channel 也可用 `?channel=` |
| `GET` | `/auth-files/download?name=<file.json>` | 下载 auth JSON |
| `POST` | `/auth-files?name=<file.json>` | 上传 raw JSON auth 文件 |
| `POST` | `/auth-files` | multipart 上传一个或多个 `.json` auth 文件 |
| `DELETE` | `/auth-files?name=<file.json>` | 删除指定 auth 文件 |
| `DELETE` | `/auth-files?all=true` | 删除全部 auth JSON |
| `PATCH` | `/auth-files/status` | 启用/禁用 auth，body `{"name":"...","disabled":true}` |
| `PATCH` | `/auth-files/fields` | 更新 auth 字段：prefix、proxy_url、headers、priority、note |
| `POST` | `/vertex/import` | multipart 上传 Vertex service account JSON，字段名 `file`，可带 `location` |
| `GET` | `/anthropic-auth-url` | 创建 Claude OAuth 登录 URL |
| `GET` | `/codex-auth-url` | 创建 Codex/OpenAI OAuth 登录 URL |
| `GET` | `/gemini-cli-auth-url` | 创建 Gemini CLI OAuth 登录 URL |
| `GET` | `/antigravity-auth-url` | 创建 Antigravity OAuth 登录 URL |
| `GET` | `/kimi-auth-url` | 创建 Kimi device flow 登录 URL |
| `POST` | `/oauth-callback` | 手动提交 OAuth callback |
| `GET` | `/get-auth-status?state=<state>` | 查询 OAuth flow 状态 |

`*-auth-url` 成功时一般返回：

```json
{
  "status": "ok",
  "url": "https://...",
  "state": "..."
}
```

`gemini-cli-auth-url` 可带：

```text
?project_id=<GCP_PROJECT_ID>
```

手动 OAuth callback 示例：

```bash
curl -sS -X POST http://127.0.0.1:8317/v0/management/oauth-callback \
  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "codex",
    "redirect_url": "http://127.0.0.1:8317/codex/callback?code=...&state=..."
  }'
```

### 8.8 Generic API call

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/api-call` | 由 management API 代发任意 HTTP 请求，可用 auth record 的 token 替换 `$TOKEN$` |

请求体：

```json
{
  "auth_index": "<AUTH_INDEX>",
  "method": "GET",
  "url": "https://api.example.com/v1/ping",
  "header": {
    "Authorization": "Bearer $TOKEN$"
  },
  "data": ""
}
```

响应：

```json
{
  "status_code": 200,
  "header": {},
  "body": "..."
}
```

`auth_index` 也可写作 `authIndex` 或 `AuthIndex`。代理优先级是：所选 credential 的 `proxy_url`，然后全局 `proxy-url`，最后直连。

## 9. Redis-compatible usage queue

CLIProxyAPI 在同一个监听端口上通过协议前缀区分 HTTP 和 Redis RESP。只实现了一个很小的 Redis-compatible 子集，用于读取 usage queue。

支持命令：

| 命令 | 用途 |
| --- | --- |
| `AUTH <password>` | 使用 management key 鉴权 |
| `AUTH <username> <password>` | 兼容 Redis 客户端，忽略 username |
| `LPOP <key>` | 弹出 1 条最旧 usage 记录 |
| `LPOP <key> <count>` | 弹出最多 `count` 条最旧 usage 记录 |
| `RPOP <key>` | 源码当前同样弹出最旧记录 |
| `RPOP <key> <count>` | 源码当前同样弹出最多 `count` 条最旧记录 |

注意：源码当前不校验 `<key>` 的名称，第二个参数只是为了兼容 Redis list 命令形状。实际数据源是全局 usage queue。

示例：

```bash
redis-cli -h 127.0.0.1 -p 8317
AUTH <MANAGEMENT_KEY>
LPOP usage 100
```

也可以直接用 HTTP：

```bash
curl -sS "http://127.0.0.1:8317/v0/management/usage-queue?count=100" \
  -H "Authorization: Bearer <MANAGEMENT_KEY>"
```

## 10. pprof 调试接口

pprof 是独立 HTTP server，不在主端口上。默认地址：

```text
127.0.0.1:8316
```

需要配置：

```yaml
pprof:
  enable: true
  addr: "127.0.0.1:8316"
```

可用路径：

| 路径 | 用途 |
| --- | --- |
| `/debug/pprof/` | pprof index |
| `/debug/pprof/cmdline` | cmdline |
| `/debug/pprof/profile` | CPU profile |
| `/debug/pprof/symbol` | symbol |
| `/debug/pprof/trace` | trace |
| `/debug/pprof/allocs` | allocs profile |
| `/debug/pprof/block` | block profile |
| `/debug/pprof/goroutine` | goroutine profile |
| `/debug/pprof/heap` | heap profile |
| `/debug/pprof/mutex` | mutex profile |
| `/debug/pprof/threadcreate` | threadcreate profile |

建议只绑定 localhost。

## 11. CPA Helper 对接建议

如果目的是采集本地用量，优先使用：

```text
GET /v0/management/usage-queue?count=<n>
```

原因：

- 这是源码直接暴露的 HTTP 接口，少一层 RESP 解析。
- 一次可以批量弹出多条记录。
- 记录里已包含 `provider`、`model`、`alias`、`endpoint`、`api_key`、`request_id` 和 token 明细。

需要在 CLIProxyAPI 侧确保：

```yaml
usage-statistics-enabled: true
remote-management:
  allow-remote: false   # 本机采集保持 false 即可
  secret-key: "..."     # 或使用 MANAGEMENT_PASSWORD
redis-usage-queue-retention-seconds: 60
```

CPA Helper 这类本机采集器一般只需要本机访问，不建议开启远程 management。若必须跨机器采集，应同时开启 `allow-remote`、使用强 management key，并限制网络访问范围。

CLIProxyAPI README 说明：从 v6.10.0 起，CLIProxyAPI/CPAMC 不再内置 usage statistics 仪表盘和持久化统计。也就是说，usage queue 仍然可以作为外部采集源，但长期存储、聚合和可视化需要由 CPA Helper 或其他 dashboard 完成。

## 12. 来源

- GitHub 仓库：https://github.com/router-for-me/CLIProxyAPI
- README：https://github.com/router-for-me/CLIProxyAPI/blob/main/README.md
- Management API 官方页：https://help.router-for.me/management/api
- 主路由源码：https://github.com/router-for-me/CLIProxyAPI/blob/ed1458aa6d3430ba59538aeb980b8934f0e80c1f/internal/api/server.go
- Amp 路由源码：https://github.com/router-for-me/CLIProxyAPI/blob/ed1458aa6d3430ba59538aeb980b8934f0e80c1f/internal/api/modules/amp/routes.go
- Management 鉴权源码：https://github.com/router-for-me/CLIProxyAPI/blob/ed1458aa6d3430ba59538aeb980b8934f0e80c1f/internal/api/handlers/management/handler.go
- Usage queue HTTP 源码：https://github.com/router-for-me/CLIProxyAPI/blob/ed1458aa6d3430ba59538aeb980b8934f0e80c1f/internal/api/handlers/management/usage.go
- Redis-compatible queue 源码：https://github.com/router-for-me/CLIProxyAPI/blob/ed1458aa6d3430ba59538aeb980b8934f0e80c1f/internal/api/redis_queue_protocol.go

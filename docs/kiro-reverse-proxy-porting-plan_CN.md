# Kiro 反代功能移植计划

## 背景

当前主项目没有内置 `kiro` provider。已调研两个参考实现：

- `HsnSaboor/CLIProxyAPIPlus`，提交 `cb8d8b3`：同为 Go/CLIProxyAPI 架构，已有完整 Kiro provider、OAuth、executor、translator、模型注册和测试，作为主要移植来源。
- `chaogei/Kiro-account-manager`，提交 `b780700`：Electron/TypeScript 实现，提供 OpenAI/Claude 兼容 API、账号轮询、Kiro API 请求格式和 AWS event-stream 解析，作为行为校验参考。

目标是在本项目中新增 `kiro` 作为一等 OAuth provider，使现有 `/v1/chat/completions`、`/v1/messages`、`/api/provider/kiro/...` 等兼容入口能够路由到 Kiro/AWS Q Developer 上游。

## 目标能力

1. 支持 `kiro` OAuth/file auth 账号加载、刷新、轮询和冷却。
2. 支持 Kiro CLI 原生 OAuth 登录，并支持导入 Kiro IDE token 文件。
3. 支持 OpenAI Chat Completions 到 Kiro 请求格式转换。
4. 支持 Claude Messages 到 Kiro 请求格式转换，包括 Claude Code 常用工具调用。
5. 支持 Kiro AWS event-stream 二进制流解析，并输出 OpenAI/Claude SSE。
6. 支持动态模型拉取，失败时回退静态 Kiro 模型表。
7. 支持 `oauth-model-alias`、`oauth-excluded-models`、`payload` 等现有配置能力接入 Kiro。

## 参考实现拆分

`CLIProxyAPIPlus` 中可参考的模块边界：

- 认证：`internal/auth/kiro/*`、`sdk/auth/kiro.go`、`internal/cmd/kiro_login.go`
- 执行器：`internal/runtime/executor/kiro_executor.go`
- 翻译器：`internal/translator/kiro/common/*`、`internal/translator/kiro/claude/*`、`internal/translator/kiro/openai/*`
- 模型：`internal/registry/model_definitions.go` 中 `GetKiroModels`，以及 `internal/registry/kiro_model_converter.go`
- 配置合成：`internal/config/config.go` 的 `KiroKey` 配置，`internal/watcher/synthesizer/config.go` 的 `synthesizeKiroKeys`
- 服务注册：`sdk/cliproxy/service.go` 的 executor 注册、模型刷新、动态模型拉取
- 登录入口：`internal/cmd/run.go`、`internal/cmd/auth_manager.go`

`Kiro-account-manager` 中可参考的行为点：

- 上游端点：`codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse` 与 `q.us-east-1.amazonaws.com/generateAssistantResponse`
- Header 指纹：`User-Agent`、`X-Amz-User-Agent`、`X-Amz-Target`、`x-amzn-kiro-agent-mode`
- 消息规则：history/currentMessage 拆分、空消息补齐、tool_use/tool_result 配对、模型映射、agentic/thinking 注入
- event-stream：`assistantResponseEvent`、`toolUseEvent`、`messageMetadataEvent`、`meteringEvent`、`reasoningContentEvent`

## 当前进度

- 2026-05-10：阶段 1 已完成。已新增 `kiro` provider 常量、配置结构、配置合成、静态模型表、模型注册链路、`oauth-model-alias`/`oauth-excluded-models` 的 `kiro` channel、配置示例，以及返回 501 的占位 executor。
- 2026-05-10：阶段 2 已开始。已落地 Kiro CLI OAuth 登录、Kiro IDE token 导入、refresh lead 注册、executor refresh 接入和 CLI 参数 `--kiro-cli-login` / `--kiro-import`。
- 2026-05-10：按 Plus 实现补齐 `incognito-browser`、`oauth-endpoint-overrides`、Kiro fingerprint 管理、CodeWhisperer userinfo fallback、AWS SSO OIDC Builder ID/IDC device-code 登录与刷新分支，并新增 `--kiro-idc-login` / `--kiro-idc-start-url` / `--kiro-idc-region` / `--kiro-idc-flow`。
- 2026-05-10：按 Plus 实现继续补齐 Kiro `FileTokenRepository`、AWS SSO OIDC Builder ID/IDC auth-code flow、PKCE/state、本地 callback server、auth-code token exchange，并将 `--kiro-idc-flow` 默认切到 auth-code，`device` 保留 device-code。
- 2026-05-10：按 Plus 实现整块移植 `internal/translator/kiro/{common,openai,claude}` 并注册 `OpenAI -> Kiro`、`Claude -> Kiro` 翻译器；已通过 `go test ./internal/translator/kiro/...`。
- 2026-05-10：按 Plus 实现替换 Kiro placeholder executor，移植真实上游请求、endpoint fallback、401/403 refresh retry、429/5xx/socket retry、AWS event-stream 解析、流式/非流式聚合、web search fallback、usage 估算、限速/冷却/jitter helper，并补 Kiro executor 单测。
- 2026-05-10：按 Plus 实现补齐 Kiro 动态模型注册：`ListAvailableModels` auth client、Kiro API 模型转换器、静态 metadata merge、agentic 变体生成、service 启动后每 3 小时刷新、动态失败 fallback 静态表；同时按 Plus 行为修正 `oauth-model-alias` 生成模型的 `ExecutionTarget` 和真实模型 ID 冲突处理。

## 实施阶段

### 阶段 1：最小 provider 骨架

- [x] 在 `internal/constant/constant.go` 新增 `Kiro = "kiro"`。
- [x] 在 `internal/config/config.go` 增加 `KiroKey`、`KiroFingerprintConfig`、`kiro-preferred-endpoint` 配置结构。
- [x] 在 `config.example.yaml` 增加 `kiro:` 示例和 `oauth-model-alias`/`oauth-excluded-models` 的 Kiro channel 说明。
- [x] 在 watcher synthesizer 增加 `kiro` 配置项合成 Auth。
- [x] 在 file auth 合成链路确认 `{"type":"kiro"}` 可以被识别并保留 Kiro metadata。

验收：

- [x] `go test ./internal/config ./internal/watcher/...`
- [x] 启动后 `/v1/models` 能看到静态 Kiro 模型，但请求可先返回未实现。

### 阶段 2：认证与刷新

- [x] 移植 `internal/auth/kiro` 中 token 类型、Kiro CLI OAuth、Kiro IDE token 导入读取、基础刷新器。
- [x] 移植 `internal/auth/kiro` 中 SSO OIDC、fingerprint 和 token repository 的完整实现。
  - [x] Plus 版 fingerprint 管理、OIDC header、runtime header 已移植。
  - [x] Plus 版 AWS SSO OIDC Builder ID/IDC device-code 登录与刷新分支已移植。
  - [x] Plus 版 `incognito-browser` 和 `oauth-endpoint-overrides` 配置能力已移植。
  - [x] Plus 版 token repository、Builder ID/IDC auth-code flow、callback server、PKCE/state、auth-code token exchange 已移植。
- [x] 移植 `sdk/auth/kiro.go` 并注册到 `sdk/auth/refresh_registry.go`、`internal/cmd/auth_manager.go`。
- [x] 增加 CLI 参数：
  - [x] `--kiro-cli-login`
  - [x] `--kiro-import`
  - [x] `--kiro-idc-login`、`--kiro-idc-start-url`、`--kiro-idc-region`
  - [x] `--kiro-idc-flow`（默认 auth-code；传 `device` 使用 device-code）
- [x] 保存 auth 文件字段：`type`、`access_token`、`refresh_token`、`expires_at`、`auth_method`、`client_id`、`client_secret`、`region`、`start_url`、`profile_arn`、`email`。

验收：

- [ ] 无浏览器模式和浏览器模式都能完成 Kiro CLI 登录。代码路径已接入，仍需真实账号实测。
- [ ] IDC auth-code/device-code 和 Builder ID auth-code/device-code 真实账号登录实测。代码路径和请求形状已用单测覆盖。
- [x] 过期 token 能自动刷新并写回 auth 文件。已接入通用 auth manager refresh 链路，需真实账号验证上游 refresh 响应。
- [x] 并发请求只触发一次实际刷新。复用现有 auth manager 的 refresh pending/backoff 机制。

### 阶段 3：翻译器

- [x] 移植 `internal/translator/kiro/common`。
- [x] 移植 OpenAI -> Kiro：
  - [x] system 合并到首个 user/currentMessage
  - [x] 多轮 history 构造
  - [x] image data URL 转 Kiro image
  - [x] OpenAI tool_calls/tool messages 转 Kiro toolUses/toolResults
  - [x] 空内容占位和孤儿 tool_result 过滤
- [x] 移植 Claude -> Kiro：
  - [x] Claude text/image/tool_use/tool_result 转 Kiro schema
  - [x] Claude Code 工具名清洗、工具描述长度截断
  - [x] thinking header/model suffix 检测
- [x] 注册 `OpenAI -> Kiro`、`Claude -> Kiro` 翻译器。

验收：

- [x] 加单测覆盖普通文本、system、图片、tool call、tool result、空 assistant、长工具描述、thinking/agentic。当前移植了 Plus 的 common/OpenAI translator 单测；Claude translator 仍需后续补专门单测。
- [ ] 对照 `Kiro-account-manager` 的 payload 结构做样例比对。

### 阶段 4：Kiro executor

- [x] 新增 `internal/runtime/executor/kiro_executor.go`。
- [x] 实现上游请求：
  - [x] CodeWhisperer endpoint
  - [x] AmazonQ endpoint
  - [x] `preferred-endpoint` 配置和 429/配额 fallback
  - [x] 401/403 token 刷新重试
  - [x] 502/503/504 和 socket 错误指数退避
- [x] 实现 AWS event-stream 解析：
  - [x] 帧边界、最大帧保护、malformed/fatal 错误分类
  - [x] assistant text、tool use 增量、metadata usage、metering credit、reasoning content
- [x] 非流式请求通过聚合流式结果实现。
- [x] 接入现有 usage reporter、request logging、proxy-aware HTTP client。

验收：

- [x] `go test ./internal/runtime/executor -run Kiro`
- [ ] 流式 OpenAI、非流式 OpenAI、流式 Claude、非流式 Claude 均可返回合法响应。代码路径已接入，仍需真实 Kiro 账号实测。
- [ ] 上游 401/403/429/5xx 的重试行为有单测或可复现实测脚本。当前代码已移植 Plus 行为，已补 event-stream/model/count_tokens 单测，错误重试仍需后续专门覆盖。

### 阶段 5：模型注册与动态刷新

- [x] 在 registry 增加静态 Kiro 模型：
  - [x] `kiro-auto`
  - [x] `kiro-claude-sonnet-4-5`
  - [x] `kiro-claude-opus-4-5`
  - [x] `kiro-claude-haiku-4-5`
  - [x] `*-agentic` 变体
  - [x] 按 Plus 补齐 `kiro-claude-opus-4-6`、`kiro-claude-sonnet-4-6`、`kiro-claude-sonnet-4`、DeepSeek、MiniMax、GLM、Qwen、GPT 静态条目。
- [x] 增加 Kiro API 模型转换器，将 `ListAvailableModels` 结果合并静态 metadata。
- [x] 在 `sdk/cliproxy/service.go` 注册 executor，并在 `registerModelsForAuth` 中接入动态模型拉取，失败时 fallback 静态模型。
- [x] 支持定时刷新模型注册，间隔 3 小时。
- [x] `oauth-model-alias` 生成的别名模型保留上游 `ExecutionTarget`，并避免别名覆盖同名真实模型。

验收：

- [x] 静态 fallback 下 `/v1/models` 能列出 Kiro 模型。代码路径已由 registry/service 单测覆盖。
- [ ] 动态账号下 `/v1/models` 能列出 Kiro 动态模型。代码路径已接入，仍需真实账号实测。
- [x] `oauth-model-alias.kiro` 和 `oauth-excluded-models.kiro` 热更新后生效。别名注册路径已补回归测试；真实热更新仍随配置 watcher 集成实测。

### 阶段 6：管理 API 与文档

- [x] 管理 API 的 runtime auth、auth file patch/delete/batch 无需单独分支时应天然支持；已补 Kiro runtime/auth-files 列表脱敏回归测试。
- [x] README/README_CN 增加 Kiro 能力说明。
- [x] `config.example.yaml` 增加完整 Kiro 配置示例。
- [x] 新增用户文档：
  - [x] Kiro CLI 登录
  - [x] Kiro IDE token 导入
  - [x] Claude Code/OpenAI-compatible 客户端配置
  - [x] 常见错误：401、403、429、region/start_url、端口占用

验收：

- [x] 管理端可看到 `kiro` auth 状态，且列表接口不会暴露 `access_token`、`refresh_token`、`client_secret`、`profile_arn` 等敏感/内部字段。
- [ ] 文档中的命令可直接跑通。命令已写入 README/README_CN，仍需真实 Kiro 账号实测。

## 建议文件清单

新增：

- `internal/auth/kiro/*`
- `sdk/auth/kiro.go`
- `internal/cmd/kiro_login.go`
- `internal/runtime/executor/kiro_executor.go`
- `internal/translator/kiro/common/*`
- `internal/translator/kiro/claude/*`
- `internal/translator/kiro/openai/*`
- `internal/registry/kiro_model_converter.go`
- `internal/thinking/provider/kiro/apply.go`（如需要 Kiro 特定 thinking 行为）

修改：

- `internal/constant/constant.go`
- `internal/config/config.go`
- `config.example.yaml`
- `internal/watcher/synthesizer/config.go`
- `sdk/cliproxy/service.go`
- `sdk/auth/refresh_registry.go`
- `internal/cmd/run.go`
- `internal/cmd/auth_manager.go`
- `internal/translator/init.go`
- `internal/registry/model_definitions.go`
- `internal/runtime/executor/thinking_providers.go`
- README/README_CN

## 测试计划

优先补单测：

- `internal/auth/kiro`: token 解析、刷新分支、filename/identifier、fingerprint。
- `internal/translator/kiro/openai`: OpenAI 请求转换、工具调用、图片、thinking。
- `internal/translator/kiro/claude`: Claude 请求转换、Claude Code 工具流、tool_result 配对。
- `internal/runtime/executor`: event-stream parser、重试、401 refresh、429 endpoint fallback。
- `internal/registry`: 动态模型转换和静态 metadata merge。

集成测试：

- 使用导入的 Kiro auth 文件跑 `/v1/chat/completions` 非流式。
- 使用 `stream: true` 跑 OpenAI SSE。
- 使用 `/v1/messages` 跑 Claude Messages SSE。
- 使用工具调用客户端验证 tool_use/tool_result 循环。
- 验证多账号轮询、冷却和 token 刷新写回。

## 风险与决策点

- Kiro 上游指纹敏感，优先移植 Plus 的 Kiro CLI OAuth/fingerprint，而不是简单复用通用 OAuth。
- Kiro API 端点和模型列表可能随 AWS/Kiro 变动，需要保留动态模型拉取和静态 fallback。
- Social Google/GitHub 登录可能受 AWS Cognito 限制；首版建议只承诺 Kiro CLI OAuth、AWS Builder ID/IDC、Kiro IDE token 导入。
- `Kiro-account-manager` 的 Electron 反代包含 UI、机器码和 MITM 逻辑；本项目不建议移植 MITM/K-Proxy，只保留 API provider 能力。
- `agentic` 系统提示会影响模型行为，建议默认只对 `*-agentic` 模型启用。
- Plus 和本仓库版本可能存在架构差异，移植时必须逐文件对齐当前主线，不能整目录覆盖。

## 推荐执行顺序

1. 先移植配置、常量、模型静态表和 service 注册，让 `kiro` 出现在模型注册链路。
2. 再移植认证与刷新，保证 auth 文件生命周期稳定。
3. 再移植 translator 和 executor，优先跑通 OpenAI 非流式。
4. 然后补流式、Claude Messages、工具调用和 usage。
5. 最后接动态模型刷新、管理端展示、文档和完整测试。

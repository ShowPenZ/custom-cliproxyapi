# 上游同步与搬运计划

更新时间：2026-04-18（已同步第二波局部兼容修复与 auth-index 读侧稳定化）

## 0. 当前进展

### 0.1 已完成

第一波低风险搬运已完成并通过定向测试，已落地内容如下：

- `7b03f046`
  - 调整 request execution metadata 行为
  - 当客户端未显式提供 `Idempotency-Key` 时，不再自动生成并下发
  - 保持 `requested_model`、`execution_session_id` 等执行 metadata 链路正常工作
- `d4a6a5ae`
  - Antigravity 的 Claude 请求转换中，过滤 `x-anthropic-billing-header:` 系统块
  - 避免内部 billing header 被透传给上游
- `5dcca69e`
  - 模型表加入 `claude-opus-4-7`
- `d9499211`
  - 为 Claude、Codex、Kimi、iFlow 的 auth 构造器补齐 proxy override 入口
  - refresh 流程优先使用 `auth.ProxyURL`
- `1dba2d0f`
  - 管理面删除 Gemini / Claude / Codex / VertexCompat key 时，若同一 `api-key` 匹配多条记录，则要求显式传 `base-url`
  - Gemini key 清洗改为按 `api-key + base-url` 去重
- `5ab9afac`（连同其所依赖的 Claude OAuth tool remap 逻辑一并本地化吸收）
  - Claude OAuth 请求会将 OpenCode 风格的小写工具名映射为 Claude Code 风格 TitleCase
  - 仅当请求侧确实发生过工具名改写时，才在响应侧做反向恢复
  - 避免客户端本来就发送 `Bash` / `Read` 等官方工具名时，被错误反转成小写
- `0ab1f541`（结合本地 Qwen 旧语义一并收口）
  - Qwen 429 错误现在会优先透传上游 `Retry-After`
  - 当 `disable-cooling` 生效且 quota exhausted 又没有返回 header 时，补一个最小默认退避
  - 同时移除了 Qwen quota 错误上“直接冷却到次日”的旧长退避语义，避免全局重试调度被超长等待压死

### 0.2 已新增测试

- `sdk/api/handlers/handlers_metadata_test.go`
- `internal/translator/antigravity/claude/antigravity_claude_request_test.go`
- `internal/api/handlers/management/config_lists_delete_keys_test.go`
- `internal/config/gemini_keys_test.go`
- `internal/auth/codex/openai_auth_test.go` 增补 proxy override 覆盖
- `internal/runtime/executor/claude_executor_test.go` 增补 Claude OAuth tool remap 边界覆盖
- `internal/runtime/executor/qwen_executor_test.go` 增补 Qwen 429 / Retry-After / quota exhausted 覆盖

### 0.3 已验证通过

已执行并通过下列定向回归：

```bash
go test ./sdk/api/handlers/... \
  ./internal/translator/antigravity/claude \
  ./internal/registry \
  ./internal/auth/... \
  ./internal/runtime/executor \
  ./internal/config \
  ./internal/api/handlers/management
```

### 0.4 当前结论

第一波低风险项已不再处于“候选”状态，而是已完成。接下来不应重复评估这些项，而应进入第二波搬运选择。

### 0.5 第二波预研结论

经过对下一批候选提交的代码级预研，当前建议如下：

- 最适合进入第二波的是 `auth-index` 管理增强
  - 主要价值不是改调度，而是让管理端 `GetGeminiKeys` / `GetClaudeKeys` / `GetCodexKeys` / `GetVertexCompatKeys` / `GetOpenAICompat` 直接返回 `auth-index`
  - 本地已经具备 `Auth.EnsureIndex()`、`authByIndex()`、`runtime_auths` / `auth_files` 中的 `auth_index` 输出能力
  - 因此这组能力和本地现状是衔接的，冲突主要集中在 `config_lists.go` 的返回结构，而不是核心执行路径
- 第二优先级是 `5ab9afac`
  - 它修的是 Claude OAuth 工具名 remap 的反向恢复边界
  - 如果当前线上存在 Claude Code / OAuth 工具名大小写、重命名、下游回显不一致问题，则应提前搬
  - 如果没有对应故障样本，可以排在 auth-index 之后
- `session-affinity` 仍不建议直接搬
  - 它会进入 `selector.go` 与选择策略主路径
  - 本地已有 `requestedAccountGroup`、`codexAllowPro`、`AuthAllowedForMetadata`、`provider-strategy`
  - 直接套上游实现会把选择语义搅乱，后续应以“本地版 session affinity 设计”方式单独评估

### 0.6 第二波当前进展

第二波已开始，当前已完成三项内容：

- `auth-index` 管理增强第一阶段
  - 新增独立辅助文件，按配置项推导并回填运行时 `auth-index`
  - 已将下列管理读取接口切换为返回带 `auth-index` 的结构：
  - `GetGeminiKeys`
  - `GetClaudeKeys`
  - `GetCodexKeys`
  - `GetVertexCompatKeys`
  - `GetOpenAICompat`
- 已补回归测试，覆盖：
  - `gemini-api-key` 返回 `auth-index`
  - `openai-compatibility.api-key-entries` 返回 `auth-index`
- `auth-index` 读侧稳定化
  - 已为 `Handler` 的 `cfg` / `authManager` 热更新入口补锁
  - `config_auth_index.go` 读取 live auth 与 config 时已改为受锁保护
  - 已补更强测试，覆盖：
  - 相同 `api-key` 不同 `base-url` 的 Gemini 条目应映射到不同 `auth-index`
  - OpenAI Compatibility 的 per-entry `auth-index` 映射
  - manager 中缺失 auth 时返回空 `auth-index`
- `Claude OAuth tool remap` 兼容修复
  - 已为 OAuth 请求补齐工具名映射与响应反向恢复
  - 已加入 rename detection，避免对本来就是官方 TitleCase 的工具名误反转
  - 已补 `Bash` / `bash` 两类边界测试
- `Qwen 429 / Retry-After` 稳定性修复
  - 已补 `Retry-After` 响应头解析
  - 已兼容 `disable-cooling` 下 quota exhausted 的最小默认退避
  - 已去除旧的“直接冷却到次日”长退避逻辑
  - 已补非流式 / 流式 / quota exhausted 三类测试

已验证通过：

```bash
go test ./internal/runtime/executor ./internal/api/handlers/management
```

当前判断：

- `auth-index` 这组不需要整组 cherry-pick
- 其中“管理端输出能力”与“读侧映射稳定化”已经吸收完成
- 剩余未吸收部分主要是 `config_lists.go` 写侧的大面积持锁与 `persistLocked` 改造，收益明确但改面偏大，暂缓
- `5ab9afac` 已不再是候选项，Claude OAuth 工具名兼容性修复已落地
- `0ab1f541` 已完成本地化吸收，且根据本地 Qwen 旧语义额外补齐了前置差异
- 下一步应转向继续筛选其他局部兼容/稳定性更新，而不是重复处理同一条 executor 链路

## 1. 背景与现状

当前仓库 `/opt/cliproxyapi` 不是从上游仓库直接保留提交历史分叉出来的标准 Git fork，而是以一次源码快照导入后继续独立开发。

已确认事实：

- 本地主线最新提交：`43646cc729302b6517819dea072ae270d1c1d28d`
- 上游仓库：`https://github.com/router-for-me/CLIProxyAPI`
- 上游主线最新提交：`c6baa64b4e3cf85f8400e2cc9d9bd3d7040db187`
- 本地 `HEAD` 与 `upstream/main` 没有共同祖先，无法直接按 merge-base 做标准 rebase / merge
- 按源码内容和时间窗口粗略判断，本地 `init` 更接近上游 `v6.9.2` 左右的代码快照
- 因此本次同步应按“手工挑选 commit / 手工移植功能块”的方式进行，而不是直接整仓合并

## 2. 本地分叉的核心方向

本地分叉不是泛化地追随上游所有 provider 变化，而是集中强化了下面几类能力：

- 面向调用方开放的诊断接口
- 面向运维的账号、配额与订阅观察能力
- Codex 账号分组与访问策略控制
- 面向公网 API key 的权限分层与限流
- Antigravity 运维脚本与自动化工具链
- usage 统计持久化、导出导入与调用方自助查看

因此，上游同步的原则不能是“尽量追平”，而应是：

- 保留本地管理与运维产品化能力
- 只搬运能提升稳定性、兼容性、调度质量、安全性的上游更新
- 不引入会削弱当前业务模型的裁剪型改动

## 3. 同步目标

本轮同步目标不是一次性追平到上游 `v6.9.29`，而是完成一轮低风险、高收益的增量吸收：

1. 吸收上游对现有主路径有帮助的修复和增强
2. 不破坏本地自定义管理 API、脚本体系和 Codex 分组策略
3. 为下一轮更大规模同步先铺好结构和测试基础

## 4. 搬运原则

### 4.1 优先搬运

- 稳定性修复
- 兼容性修复
- 执行链路的小范围增强
- 模型表更新
- 管理接口的局部增强

### 4.2 条件搬运

- 会触碰调度器、selector、auth manager 的功能增强
- 与本地 request metadata / account group / public endpoint 策略有交叉的改动
- 会引入较多测试和重构文件的改动

### 4.3 暂不搬运

- 删除 provider 的裁剪性提交
- 需要大面积替换本地管理面逻辑的提交
- 不能明确验证收益、但合并冲突明显的提交

## 5. 候选搬运清单

### 5.1 第一优先级：本轮已完成

#### A. `d9499211` feat(auth): add proxy URL override support to auth constructors and executors

收益：

- 完善 auth 构造器与 executor 层的代理覆盖逻辑
- 对本地多脚本、多入口刷新链路更实用
- 与本地管理面和调度逻辑耦合低

风险评估：

- 低

处理结果：

- 已完成手工移植并接线到 refresh 执行链路

#### B. `7b03f046` fix(handlers): include execution session metadata and skip idempotency key when absent

收益：

- 修正 request metadata 的边界行为
- 本地仓库已经在 metadata 中叠加了 `requestedAccountGroup`、`codexAllowPro`，此修复正好属于同一链路

风险评估：

- 低

处理结果：

- 已完成手工移植
- 已补 metadata 回归测试

#### C. `d4a6a5ae` fix(antigravity): strip billing header from system instruction before upstream call

收益：

- 避免 `x-anthropic-billing-header` 被错误透传到 Antigravity 上游
- 属于明确兼容性修复

风险评估：

- 低

处理结果：

- 已完成手工移植
- 已补 translator 回归测试

#### D. `5dcca69e` feat(models): add Claude Opus 4.7 model entry to registry JSON

收益：

- 几乎零风险的模型表更新

风险评估：

- 极低

处理结果：

- 已完成模型表更新

#### E. `1dba2d0f` fix(handlers): add base URL validation and improve API key deletion tests

收益：

- 增强管理 API 配置安全性
- 与本地管理面方向一致

风险评估：

- 低到中

处理结果：

- 已完成核心逻辑移植
- 已补 management / config 回归测试

### 5.2 第二优先级：值得评估，但不建议第一批直接搬

#### F. `d1508ca0` / `7c24d54c` feat(session-affinity): add session-sticky routing for multi-account load balancing

收益：

- 对多账号池可显著提升会话一致性
- 与本地 Codex 分组、路由策略增强形成互补

风险评估：

- 高

原因：

- 会触碰 `sdk/cliproxy/auth/selector.go`
- 会影响本地 `AuthAllowedForMetadata`、请求级 account group 过滤、provider strategy 行为
- 与本地调度定制存在实质性交叉

处理方式：

- 不建议直接 cherry-pick
- 先单独做设计评审，再决定是否提炼为“本地版 session affinity”

#### G. `c6baa64b` / `894baad8` / `c26936e2` auth-index 管理增强

收益：

- 完善管理接口中 auth index 的暴露与映射稳定性
- 对本地已产品化的管理能力是有价值的增强

风险评估：

- 中到高

原因：

- 上游管理面结构与本地已明显分叉
- 本地已有 `/v1/api/*`、自定义 quota / subscription / account group 逻辑

处理状态：

- 已完成第一阶段：管理读取接口输出 `auth-index`
- 已完成第二阶段中的“读侧映射稳定化”
- 暂未继续吸收 `config_lists.go` 写侧整组持锁改造
- 当前没有证据表明必须继续整组搬运剩余写侧重构

#### H. `5ab9afac` fix(executor): handle OAuth tool name remapping with rename detection and add tests

收益：

- 如果本地 Claude OAuth / Claude Code 使用中遇到工具重命名问题，这会直接提升兼容性

风险评估：

- 中

处理结果：

- 已完成本地化移植
- 未直接照搬上游整段实现，而是按本地 executor 结构补齐 remap 和 rename detection
- 已补单测覆盖 TitleCase / lowercase 两类边界

### 5.3 第三优先级：按使用情况决定

#### I. `0ab1f541` fix(executor): handle 429 Retry-After header and default retry logic for quota exhaustion

收益：

- 完善 429 行为

风险评估：

- 取决于受影响 provider 是否仍在本地重度使用

处理结果：

- 已完成本地化移植
- 同时把本地旧版 `wrapQwenError` 的“冷却到次日”语义收敛到与上游一致
- 已补 Qwen 429 / retry-after / quota exhausted 回归测试

### 5.4 暂不搬运

#### J. `f5dc6483` chore: remove iFlow-related modules and dependencies

结论：

- 暂不搬

原因：

- 本地仍保留 iFlow 支持
- 这是产品裁剪，不是缺陷修复

#### K. `8fac2963` chore: remove Qwen support from SDK and internal components

结论：

- 暂不搬

原因：

- 本地仍保留 Qwen 支持
- 本次目标不是缩减 provider 面

## 6. 推荐搬运顺序

### Phase 0：准备阶段

目标：

- 固化分析结论
- 降低后续搬运冲突风险

动作：

1. 将本文件提交到仓库文档中
2. 新建同步工作分支，例如 `sync/upstream-v6.9.29-wave1`
3. 记录当前本地特性基线
4. 先不动用户未提交的工作区修改

### Phase 1：低风险修复吸收（已完成）

已完成顺序：

1. `7b03f046`
2. `d4a6a5ae`
3. `d9499211`
4. `5dcca69e`
5. `1dba2d0f`

验收结果：

- 定向测试通过
- 核心 API handlers 回归通过
- 本地新增管理接口未回退
- Codex 分组策略保持原语义

### Phase 2：管理面增强评估

候选：

- `auth-index` 三连提交

执行策略：

- 不整提交导入
- 先抽象出可复用的数据结构和映射逻辑
- 必须确认不会破坏本地 `auth-files` / `runtime-auths` / 自定义公开诊断接口

### Phase 3：调度能力评估

候选：

- `session-affinity`

执行策略：

- 先做设计文档
- 对比以下本地能力是否会发生语义冲突：
  - `requestedAccountGroup`
  - `codexAllowPro`
  - `provider-strategy`
  - `AuthAllowedForMetadata`

只有在这些点有明确兼容方案时才进入实现

## 7. 推荐合并方式

### 7.1 不推荐

- 直接 merge `upstream/main`
- 直接 rebase 到上游
- 大批量 cherry-pick 多个相邻 commit 不审查冲突

原因：

- 当前仓库与上游不是标准同源历史
- 管理面、脚本面、调度面已经明显分叉

### 7.2 推荐

按下面三类策略组合：

- 小提交且改动集中：手工对照式移植
- 数据文件更新：直接内容同步
- 设计型增强：先做本地化重写，不直接照搬实现

## 8. 关键冲突点

以下区域属于高冲突地带，后续同步时应优先人工审查：

- `internal/api/server.go`
- `sdk/api/handlers/handlers.go`
- `sdk/cliproxy/auth/conductor.go`
- `sdk/cliproxy/auth/scheduler.go`
- `sdk/cliproxy/auth/selector.go`
- `internal/api/handlers/management/*`
- `config.example.yaml`

原因：

- 本地对公开接口、权限策略、请求 metadata、调度过滤、provider strategy 都做了定制

## 9. 本地能力保护清单

以下功能在任何同步过程中都不能被上游覆盖掉：

- `/v1/api/oauth-quota`
- `/v1/api/antigravity-quota`
- `/v1/api/codex-subscription-status`
- `codex-pro-api-keys`
- `proxy-only-api-keys`
- `claude-only-api-keys`
- `requestedAccountGroup` 与 `codexAllowPro` 的 request metadata 传递
- `AuthAllowedForMetadata`
- usage 持久化与导出导入
- Antigravity / OAuth 运维脚本链路

## 10. 本轮建议结论

本轮建议执行的搬运范围如下：

### 10.1 已完成

- `d9499211`
- `7b03f046`
- `d4a6a5ae`
- `5dcca69e`
- `1dba2d0f`
- `5ab9afac`
- `0ab1f541`

### 10.2 下一步优先评估

- `c26936e2`
- `d1508ca0`
- `7c24d54c`

### 10.3 明确不搬

- `f5dc6483`
- `8fac2963`

## 11. 下一步执行建议

下一步应按下面顺序推进：

1. 保持当前第一波改动稳定，不再重复修改同一批文件
2. `auth-index` 已完成“管理端输出 + 读侧稳定化”，后续只在出现并发写入或持久化一致性问题时，再继续追 `config_lists` 写侧锁改造
3. 下一步优先继续筛 Claude / Codex / management 相关的小范围稳定性修复，而不是直接跳到高冲突调度改造
4. 若仍要继续增强调度，`session-affinity` 必须单独做设计评审，不直接进入实现

如果继续推进，建议下一轮先继续筛 `session-affinity` 之外的局部修复项；`auth-index` 剩余写侧重构先不主动扩大范围，暂不进入 `session-affinity` 这类高冲突调度改动。

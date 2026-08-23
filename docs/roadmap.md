# Hydra 实现路线图

> 日期：2025-01-17
> 更新：2025-08-22
> 状态：P0/P1/P2 已完成

## 核心重点

| # | 重点 | 优先级 | 当前状态 |
|---|------|--------|----------|
| 1 | 协议翻译引擎（IR 架构） | P0 | ✅ 已完成 — IR 包已接入全部 handler，旧翻译函数已清理 |
| 2 | 上游伪装（版本同步） | P0 | ✅ 已完成 — version 包统一 UA |
| 3 | 模型路由（统一注册表） | P1 | ✅ 已完成 — registry 包 + handler 集成 |
| 4 | Agent 易用性（能力元数据） | P1 | ✅ 已完成 — /v1/models 返回能力元数据 |
| 5 | 账号池增强 | P2 | ✅ 已完成 — 配额预警 + 账号分组 + 并发控制 |
| 6 | 使用成本简化（TUI + OAuth） | P1 | ✅ 已完成 — TUI 内 OAuth 添加账号 + Provider 管理 |
| 7 | Bedrock Converse 协议 | P2 | ✅ 已完成 — IR 编解码 + 测试 |
| 8 | 多 key failover | P2 | ✅ 已完成 — 同 model 多 provider 优先级轮换 |
| 9 | OS keychain 迁移 | P2 | ✅ 已完成 — 密钥从 DB 迁移到 OS keychain |
| 10 | 旧配对翻译函数清理 | P2 | ✅ 已完成 — 删除 2527 行死代码 |

## 已完成工作摘要

### P0 — 协议翻译引擎（IR 架构）

- ✅ `internal/ir` 包：IR 核心结构 + OpenAI/Anthropic/Responses/Gemini 编解码器
- ✅ 生产 handler 全部接入 IR（`ir_adapter.go`、`ir_stream.go`）
- ✅ Failover 逻辑抽取到 `failover.go`，三个 handler 使用统一 `failoverLoop`
- ✅ P0 缺口修复：`local_shell` 工具链路 + provider 流式 Anthropic `tool_calls`
- ✅ 旧配对翻译函数已清理（TransformRequest/TransformResponse 等已删除，IR 为唯一生产路径）

### P0 — 上游伪装（版本同步）

- ✅ `internal/version` 包统一 User-Agent
- ✅ 每账号独立 machine_id（DB 持久化）
- ✅ 环境变量 `HYDRA_UPSTREAM_CLIENT_VERSION` 覆盖
- ✅ OAuth 凭据已恢复（开箱即用）

### P1 — 模型路由（统一注册表）

- ✅ `internal/registry` 包：运行时模型注册表
- ✅ `internal/provider` 包：DB-backed provider CRUD
- ✅ `/v1/models` 从注册表生成，包含能力元数据
- ✅ `provider_health.go` 健康检查

### P1 — Agent 易用性

- ✅ `/v1/models` 返回 `context_window`、`max_output_tokens`、`supports`、`display_name`
- ✅ Codex catalog 端点

### P1 — 使用成本简化（部分）

- ✅ OAuth Device Flow 已实现（`oauth.go` 中 `DeviceCodeFlow`）
- ✅ CLI provider/model 管理命令
- ✅ TUI 错误提示改进（不再静默吞错误）
- ✅ TUI 内添加账号（OAuth Device Flow 在 TUI 内完成）
- ✅ TUI Providers tab（添加 provider：多步输入 name→URL→API key）
- ❌ TUI 模型映射管理
- ❌ 配置写回机制（TUI 修改 → config.toml）

---

## 1. 协议翻译引擎（IR 架构）

### 问题

当前是 N×N 配对翻译，5 对翻译函数分散在 mapper.go、responses.go、anthropic.go、apikey_provider.go 中。已知缺口：

- Codex local_shell 工具链路完全断裂（P0）
- Provider 流式 Anthropic 响应丢失 tool_calls（P0）
- 14 种 Responses API item 类型未处理（P1）
- Anthropic document block 未处理（P1）
- Gemini 响应方向 6 种 part 类型未处理（P2）

### 实现方向

引入中间表示（IR），把 N×N 降为 N×2：

```
入站协议 → decode → IR → encode → 出站协议
出站响应 → decode → IR → encode → 入站协议
```

**IR 核心结构**：

```go
type IRRequest struct {
    Model         string
    System        string
    Messages      []IRMessage
    Tools         []IRTool
    Stream        bool
    MaxTokens     int64
    Temperature   *float64
    TopP          *float64
    Reasoning     *IRReasoning
    ResponseFormat *IRResponseFormat
    StopSequences []string
    ToolChoice    any
    Metadata      map[string]any  // 协议特有字段
}

type IRMessage struct {
    Role    string       // system/user/assistant/tool
    Content []IRContent
}

type IRContent struct {
    Type       string  // text/thinking/image/audio/tool_call/tool_result
    Text       string
    Image      *IRImage
    Audio      *IRAudio
    ToolCall   *IRToolCall
    ToolResult *IRToolResult
    Thinking   *IRThinking
}

type IRTool struct {
    Name        string
    Description string
    Schema      map[string]any
    Kind        string  // function/local_shell/shell/apply_patch/mcp/computer
}

type IRToolCall struct {
    ID   string
    Name string
    Args map[string]any  // 统一用 object，编解码时转 string/object
}

type IRToolResult struct {
    ID      string
    Name    string
    Content string
    IsError bool
}

type IRThinking struct {
    Text      string
    Signature string
    Redacted  bool
}

type IRStreamEvent struct {
    Type       string  // text_delta/thinking_delta/tool_call_delta/tool_call_done/usage/done
    Delta      string
    ToolCall   *IRToolCall
    Usage      *IRUsage
}

type IRUsage struct {
    Prompt     int64
    Completion int64
    Cached     int64
    Thought    int64
}
```

**每个协议一个编解码器对**：

| 协议 | decode | encode | 文件 |
|------|--------|--------|------|
| OpenAI Chat | `DecodeChat(req) IRRequest` | `EncodeChat(IRRequest) req` | `codec_openai.go` |
| OpenAI Responses | `DecodeResponses(req) IRRequest` | `EncodeResponses(IRRequest) req` | `codec_responses.go` |
| Anthropic | `DecodeAnthropic(req) IRRequest` | `EncodeAnthropic(IRRequest) req` | `codec_anthropic.go` |
| Gemini | `DecodeGemini(resp) IRResponse` | `EncodeGemini(IRRequest) req` | `codec_gemini.go` |
| Provider | 复用 OpenAI Chat | 复用 OpenAI Chat | — |

**特殊工具映射**（编码器内部处理）：

```
Responses local_shell tool def
  → EncodeGemini: 包装为 functionDeclarations{name:"local_shell"}
  → EncodeOpenAI: 保留为 tools[]{type:"local_shell"}

Gemini functionCall{name:"local_shell"}
  → EncodeResponses: 还原为 local_shell_call item
  → EncodeOpenAI:    还原为 tool_calls
```

**流式翻译**：

```
Gemini SSE → DecodeGeminiStream → IRStreamEvent[] → EncodeXxxStream → 入站协议 SSE
```

每个 encode 流式函数维护自己的状态机，把统一的 IRStreamEvent 合成为协议特定的生命周期事件。

### 验收标准（最低）

- [ ] IR 结构定义完成，能无损表达 6 种协议的所有已处理语义
- [ ] OpenAI Chat 编解码器实现，现有测试全部通过
- [ ] Anthropic 编解码器实现，现有测试全部通过
- [ ] Responses 编解码器实现，现有测试全部通过
- [ ] Gemini 编解码器实现，现有测试全部通过
- [ ] `local_shell_call` + `local_shell_call_output` 在 Responses → Gemini → Responses 全链路翻译正确
- [ ] Provider 流式 Anthropic 响应正确传递 tool_calls
- [ ] `go test ./...` 全绿
- [ ] 旧的配对翻译函数全部删除，无残留调用

---

## 2. 上游伪装（版本同步）

### 问题

- `upstream.go`: UA/client version = `4.3.0`
- `oauth.go`: OAuth UA = `4.4.7`
- `quota.go`: quota UA = `4.3.0`
- `utls.go`: Chrome 120 指纹
- 三处 UA 不一致，TLS 指纹偏旧

### 实现方向

**统一版本源**：

```go
// internal/proxy/version.go
const antigravityVersion = "4.4.7"  // 单一真相源

func antigravityUserAgent() string {
    return fmt.Sprintf("Antigravity/%s (Macintosh; Intel Mac OS X 10_15_7) Chrome/132.0.6834.160 Electron/39.2.3", antigravityVersion)
}
```

所有使用 UA 的地方（upstream.go、oauth.go、quota.go）统一调用 `antigravityUserAgent()`。

**TLS 指纹对齐**：UA 中声明 Chrome/132，uTLS 使用 `HelloChrome_133`（最接近 132 的可用指纹）。

**machine ID 隔离**：每个账号独立 machine ID，存入 DB。

**session ID 策略**：每请求生成新 UUID（模拟真实 IDE 行为）。

**版本可覆盖**：支持环境变量 `HYDRA_UPSTREAM_CLIENT_VERSION` 覆盖，方便快速更新。

### 验收标准（最低）

- [ ] UA/client version/OAuth UA/quota UA 四处统一为同一版本
- [ ] TLS 指纹与 UA 中声明的 Chrome 版本匹配
- [ ] 每个账号有独立的 machine ID（DB 持久化）
- [ ] session ID 每请求生成
- [ ] 支持环境变量覆盖版本号
- [ ] `go test ./...` 全绿
- [ ] 实际请求 Antigravity 上游不返回 403（需手动验证）

---

## 3. 模型路由（统一注册表）

### 问题

- 模型列表分散在 config.toml、AGY 动态获取、provider 注册三处
- 路由逻辑硬编码在 handler 里
- 没有统一的模型能力描述

### 实现方向

**统一模型注册表**：

```go
// internal/proxy/registry.go
type ModelEntry struct {
    ID            string
    DisplayName   string
    Provider      string  // "antigravity" | provider name
    ContextWindow int
    MaxOutput     int64
    Capabilities  ModelCapabilities
    // antigravity 特有
    AGYMappedName string
    ThinkingModel bool
    // provider 特有
    ProviderBaseURL string
}

type ModelCapabilities struct {
    Streaming bool
    Tools     bool
    Thinking  bool
    Vision    bool
    Audio     bool
}

type ModelRegistry struct {
    mu      sync.RWMutex
    entries map[string]*ModelEntry  // key: lowercase model ID
}
```

**注册表来源**：
- 静态：`config.toml` 中的 `[[models]]` → 启动时加载
- 动态：AGY `fetchAvailableModels` → 运行时更新
- 合并：同 ID 的条目合并，动态数据覆盖静态默认值

**路由逻辑**：

```go
func (r *ModelRegistry) Route(model string) (*ModelEntry, error) {
    entry := r.Lookup(model)
    if entry == nil {
        // 未注册 → 默认走 Antigravity
        return defaultAntigravityEntry(model), nil
    }
    return entry, nil
}
```

### 验收标准（最低）

- [ ] ModelRegistry 实现，支持注册/查询/动态更新
- [ ] config.toml 模型在启动时加载到注册表
- [ ] AGY 动态模型在 fetchAvailableModels 后更新到注册表
- [ ] 路由逻辑从 handler 提取到注册表
- [ ] `/v1/models` 从注册表生成响应
- [ ] `go test ./...` 全绿

---

## 4. Agent 易用性（能力元数据）

### 问题

- `/v1/models` 只返回 id/object/owned_by，没有能力信息
- Agent 不知道哪个模型支持工具/推理/视觉
- 用户切换模型时无法预知能力差异

### 实现方向

**扩展 `/v1/models` 响应**：

```json
{
  "object": "list",
  "data": [
    {
      "id": "gemini-3-pro",
      "object": "model",
      "owned_by": "antigravity",
      "created": 0,
      "context_window": 1000000,
      "max_output_tokens": 65536,
      "supports": {
        "streaming": true,
        "tools": true,
        "thinking": true,
        "vision": true,
        "audio": false
      },
      "display_name": "Gemini 3 Pro"
    }
  ]
}
```

**能力来源**：
- Antigravity 模型：AGY `fetchAvailableModels` 返回的 `supportsThinking`、`supportsImages` + 静态默认值
- Provider 模型：config.toml 中可配置，默认 `streaming:true, tools:true, thinking:false`

**保持兼容**：额外字段被标准 OpenAI 客户端忽略，不影响现有使用。

### 验收标准（最低）

- [ ] `/v1/models` 每个模型条目包含 `context_window`、`max_output_tokens`、`supports`、`display_name`
- [ ] Antigravity 模型能力从 AGY 动态元数据获取
- [ ] Provider 模型能力从 config 获取，有合理默认值
- [ ] 标准 OpenAI 客户端（不识别额外字段）仍能正常使用
- [ ] `go test ./...` 全绿

---

## 5. 账号池增强

### 问题

- 配额用完才 429，没有预警
- 所有账号等同对待，没有按模型权限分组
- 同一账号并发不限，可能触发上游限流

### 实现方向

**配额预警**：

```go
// 账号配额 < 20% 时降权（调度优先级降低）
if account.QuotaRemaining < totalQuota * 0.2 {
    account.Priority = PriorityLow
}
```

**账号分组**：

```go
type AccountGroup struct {
    Name    string
    Models  []string  // 该组账号可用的模型
    Accounts []*Account
}
```

按 AGY 返回的 `protectedModels` 自动分组。

**并发控制**：

```go
type AccountSemaphore struct {
    sems map[int64]chan struct{}  // accountID → semaphore
}

// 每个账号最多 N 个并发请求
func (s *AccountSemaphore) Acquire(accountID int64) { ... }
func (s *AccountSemaphore) Release(accountID int64) { ... }
```

### 验收标准（最低）

- [ ] 配额低于阈值时账号调度降权
- [ ] 账号按可用模型分组，调度时只选有权限的账号
- [ ] 同一账号并发请求有上限，超限等待
- [ ] `go test ./...` 全绿

---

## 6. 使用成本简化（TUI + OAuth）

### 问题

| 操作 | CLI | TUI | 配置文件 |
|------|-----|-----|----------|
| 添加 Google 账号 | ✅ 手动复制 URL | ❌ 提示去终端 | — |
| 管理 API key | ✅ 完整 | ✅ 完整 | — |
| 配置 Provider | ❌ | ❌ | ✅ 唯一方式 |
| 配置模型映射 | ❌ | ❌ | ✅ 唯一方式 |
| 修改端口/调度/健康检查 | ❌ | ⚠️ 仅调度模式 | ✅ 唯一方式 |

核心痛点：
- Provider 和模型配置只能编辑 TOML 文件
- 添加账号需要离开 TUI 去终端手动复制 URL

### 成熟项目参考

| 项目 | 做法 | hydra 可借鉴点 |
|------|------|----------------|
| **gh CLI** | OAuth Device Flow：显示 code → 自动打开浏览器 → 轮询 token | 替换当前手动复制 URL 的流程 |
| **k9s** | 配置文件为真相源，TUI 内按 `o` 用 `$EDITOR` 打开 | 配置编辑快捷方式 |
| **lazygit** | TUI 内按 `e` 用 `$EDITOR` 打开配置，保存后热重载 | 配置编辑 + 热重载 |

### 设计原则

```
配置文件 = 真相源（持久化）
CLI     = 脚本化操作（自动化、CI）
TUI     = 交互式操作（日常使用）
三者操作同一份数据，TUI/CLI 修改后写回配置文件
```

### 实现方向

#### 6.1 OAuth Device Flow（替换手动复制 URL）

当前流程：打印 URL → 用户手动打开浏览器 → 用户复制 redirect URL → 粘贴回终端

目标流程：显示 code → 自动打开浏览器 → 轮询等待 token → 完成

Google 支持 OAuth Device Flow（`https://oauth2.googleapis.com/device/code`）。改造 `internal/account/oauth.go`：

```go
// 1. 请求 device code
// POST https://oauth2.googleapis.com/device/code
//   → {device_code, user_code, verification_url, expires_in, interval}

// 2. 显示 code + 自动打开浏览器
fmt.Printf("Code: %s\nOpening browser...", user_code)
browser.OpenURL(verification_url)

// 3. 轮询等待 token
// POST https://oauth2.googleapis.com/token (每 interval 秒)
//   → {access_token, refresh_token}
```

好处：
- 不需要用户复制 URL
- 可以在 TUI 中执行（只需要一个确认提示 + 自动打开浏览器）
- SSH/远程环境也能用（显示 code + URL，用户在本地浏览器打开）

#### 6.2 TUI 内添加账号

在 Accounts tab 按 `a`：
1. TUI 暂停，显示 OAuth Device Flow 提示
2. 显示 code + 自动打开浏览器（或显示 URL 供远程用户）
3. 后台轮询 token（goroutine）
4. 成功后自动添加账号 + 刷新列表
5. 失败则显示错误，返回 Accounts tab

技术要点：Bubble Tea 支持 `tea.Cmd` 返回异步消息，OAuth 轮询放在 goroutine 里，完成后发消息回 TUI。

#### 6.3 TUI 内配置 Provider

新增 Providers tab：

```
┌─ Providers ──────────────────────────────────────┐
│ Name     Base URL                    Models  Key │
│ deepseek https://api.deepseek.com        2    ●  │
│ zhipu    https://open.bigmodel.cn/...    1    ●  │
│                                                  │
│ [a] Add  [e] Edit  [D] Delete  [Enter] Test     │
└──────────────────────────────────────────────────┘
```

按 `a` 添加 Provider：
1. 表单输入：name、base_url、api_key
2. 保存后写回 config.toml
3. 可选：按 `Enter` 发测试请求验证连通性

按 `e` 编辑：修改 base_url 或 api_key。

#### 6.4 TUI 内配置模型映射

在 Providers tab 选中 provider 后按 `m` 进入模型列表：

```
┌─ Models for deepseek ────────────────────────────┐
│ ID              Display Name     Context  MaxOut  │
│ deepseek-chat   DeepSeek Chat    128000   4096    │
│ deepseek-reason DeepSeek R1      128000   4096    │
│                                                    │
│ [a] Add  [e] Edit  [D] Delete                     │
└────────────────────────────────────────────────────┘
```

#### 6.5 CLI 补全

```bash
# Provider 管理
hydra provider add deepseek --base-url https://api.deepseek.com --api-key sk-...
hydra provider list
hydra provider remove deepseek
hydra provider test deepseek

# 模型管理
hydra model add deepseek-chat --provider deepseek --display-name "DeepSeek Chat" --context 128000
hydra model list
hydra model remove deepseek-chat

# 配置编辑（快捷方式）
hydra config edit    # 用 $EDITOR 打开 config.toml
```

#### 6.6 配置写回机制

当前 `config.Save()` 已存在。需要确保：
- TUI 修改配置后调用 `config.Save()` 写回 TOML
- CLI 修改配置后同样写回
- 运行中的 `hydra serve` 能感知配置变更（文件监听或手动重载）

### 验收标准（最低）

- [ ] OAuth Device Flow 实现，添加账号不需要手动复制 URL
- [ ] TUI Accounts tab 按 `a` 可直接添加账号（OAuth 流程在 TUI 内完成）
- [ ] TUI 新增 Providers tab，可添加/编辑/删除 provider
- [ ] TUI 可管理 provider 下的模型映射
- [ ] TUI 修改配置后写回 config.toml
- [ ] CLI 新增 `hydra provider add/list/remove/test`
- [ ] CLI 新增 `hydra model add/list/remove`
- [ ] CLI 新增 `hydra config edit`
- [ ] `hydra accounts add` 保留兼容（仍可用旧流程）
- [ ] `go test ./...` 全绿

---

## 实施顺序

```
阶段 1 (P0): 上游伪装版本同步 ✅ 已完成
  → internal/version 包统一 UA
  → 每账号 machine_id 持久化

阶段 2 (P0): IR 架构 + 编解码器 ✅ 已完成
  → internal/ir 包 + 全部 handler 接入
  → failover 抽取 + P0 缺口修复

阶段 3 (P1): 模型注册表 ✅ 已完成
  → internal/registry + internal/provider 包
  → /v1/models 从注册表生成

阶段 4 (P1): 能力元数据 ✅ 已完成
  → /v1/models 返回 context_window/max_output_tokens/supports

阶段 5 (P1): 使用成本简化 ⚠️ 部分完成
  → ✅ OAuth Device Flow
  → ✅ CLI provider/model 命令
  → ✅ TUI 错误提示
  → ❌ TUI 内 OAuth 添加账号
  → ❌ TUI Providers tab
  → ❌ 配置写回机制

阶段 6 (P2): 账号池增强 — 待实现
  → 配额预警 + 账号分组 + 并发控制

阶段 7 (P2): Bedrock Converse — 待实现
  → AWS Bedrock Converse API 作为新的上游协议
  → IR 编解码器扩展

阶段 8 (P2): 多 key failover — 待实现
  → 同一 provider 名配多个 key 轮换
  → 需要解除 provider_models.model_id 的 UNIQUE 约束

阶段 9 (P2): Keychain 迁移 — 待实现
  → API key / OAuth token 存储到 OS keychain
  → macOS Keychain / Linux secret-service / Windows Credential Manager
```

## 验收总标准

所有阶段完成后：

- [x] `go test ./...` 全绿
- [x] `go vet ./...` 无警告
- [x] `gofmt -l .` 无输出
- [ ] Codex CLI 通过 hydra 执行 shell 命令正常工作（需端到端验证）
- [ ] Claude Code 通过 hydra 使用 Antigravity 模型正常工作（需端到端验证）
- [ ] Claude Code 通过 hydra 使用 provider 模型正常工作（需端到端验证）
- [x] 切换模型不需要改 agent 配置
- [x] `/v1/models` 返回完整能力元数据
- [ ] Antigravity 上游不返回 403（需手动验证）
- [ ] 旧配对翻译代码全部删除（保留作为 fallback）
- [ ] TUI 内可完成添加账号、配置 provider、配置模型映射
- [ ] 添加账号不需要手动复制 URL（OAuth Device Flow 已实现，TUI 集成待做）
- [x] CLI 可脚本化管理 provider 和模型

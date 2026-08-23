# Hydra 协议翻译缺口分析

> 日期：2025-01-17
> 范围：hydra 代理的 5 种通信格式之间的翻译路径全量审计

## 0. 官方协议文档索引

每种格式都有官方权威文档，以下是实现翻译时应该对照的原始参考：

### 0.1 OpenAI Chat Completions API

| 文档 | URL |
|------|-----|
| **API Reference (Chat)** | https://developers.openai.com/api/reference/resources/chat |
| **Streaming Guide** | https://developers.openai.com/api/docs/guides/streaming-responses |
| **How to Stream Completions** | https://developers.openai.com/cookbook/examples/how_to_stream_completions |
| **Function Calling Guide** | https://developers.openai.com/api/docs/guides/function-calling |
| **Migrate to Responses** | https://developers.openai.com/api/docs/guides/migrate-to-responses |

**关键特性**：
- 消息容器：`messages[]`，角色 system/user/assistant/tool
- 工具调用：`tool_calls[]`，参数为 JSON string
- 工具结果：`role: "tool"` + `tool_call_id`
- 流式：`data: {json}\n\n`，结束 `data: [DONE]`
- 推理：`reasoning_content` 字段（非标准，部分 provider 支持）

### 0.2 OpenAI Responses API

| 文档 | URL |
|------|-----|
| **API Reference (Responses)** | https://developers.openai.com/api/reference/resources/responses/ |
| **Create Response** | https://developers.openai.com/api/reference/resources/responses/methods/create/ |
| **Streaming Events** | https://developers.openai.com/api/reference/resources/responses/streaming-events/ |
| **Local Shell Tool** | https://developers.openai.com/api/docs/guides/tools-local-shell |
| **Shell Tool (new)** | https://developers.openai.com/api/docs/guides/tools-shell |
| **Apply Patch Tool** | https://developers.openai.com/api/docs/guides/tools-apply-patch |
| **MCP & Connectors** | https://developers.openai.com/api/docs/guides/tools-connectors-mcp |
| **MCP Cookbook** | https://developers.openai.com/cookbook/examples/mcp/mcp_tool_guide |

**关键特性**：
- 输入容器：`input` (string 或 item array)，输出容器：`output` (item array)
- 系统提示：顶层 `instructions` 字段
- Item 类型（完整列表）：
  - `message` — 文本消息
  - `reasoning` — 推理内容（含 `encrypted_content`）
  - `function_call` — 函数调用
  - `function_call_output` — 函数结果
  - `custom_tool_call` / `custom_tool_call_output` — 自定义工具
  - `local_shell_call` / `local_shell_call_output` — 本地 shell（Codex CLI）
  - `shell_call` / `shell_call_output` — 新版 shell 工具（GPT-5.1+）
  - `apply_patch_call` / `apply_patch_call_output` — 文件补丁
  - `mcp_call` / `mcp_list_tools` — MCP 工具
  - `mcp_approval_request` / `mcp_approval_response` — MCP 审批
  - `computer_call` / `computer_call_output` — Computer Use
  - `web_search_call` — 网页搜索
  - `file_search_call` — 文件搜索
  - `image_generation_call` — 图片生成
  - `code_interpreter_call` — 代码解释器
  - `compaction` / `compaction_trigger` / `context_compaction` — 上下文压缩
  - `item_reference` — 引用历史 item
- 工具定义类型：`function`、`local_shell`、`shell`、`apply_patch`、`mcp`、`web_search`、`file_search`、`computer_use`、`code_interpreter`
- 流式：`event: type\ndata: {json}\n\n`，语义化事件类型
- 流式事件生命周期：`response.created` → `output_item.added` → `content_part.added` → `*.delta` → `*.done` → `output_item.done` → `response.completed`

### 0.3 Anthropic Messages API

| 文档 | URL |
|------|-----|
| **Messages API** | https://platform.claude.com/docs/en/api/messages |
| **Messages Python SDK** | https://platform.claude.com/docs/en/api/python/messages |
| **Streaming Guide** | https://platform.claude.com/docs/en/build-with-claude/streaming |
| **Tool Use Guide** | https://platform.claude.com/docs/en/build-with-claude/tool-use |
| **Extended Thinking** | https://platform.claude.com/docs/en/build-with-claude/extended-thinking |

**关键特性**：
- 消息容器：`messages[]`，角色 user/assistant
- 系统提示：顶层 `system` 字段（string 或 block array）
- Content Block 类型（完整列表）：
  - `text` — 文本
  - `image` — 图片（base64）
  - `tool_use` — 工具调用（参数为 object，非 string）
  - `tool_result` — 工具结果（可含 `is_error`）
  - `thinking` — 推理内容（含 `signature`）
  - `redacted_thinking` — 加密推理
  - `document` — 文档（PDF 等）
  - `server_tool_use` — 服务端工具调用
  - `tool_reference` — 工具引用（在 tool_result 内）
  - `search_result` — 搜索结果
- 工具定义：`tools[].input_schema`（JSON Schema）
- 流式：`event: type\ndata: {json}\n\n`
- 流式事件生命周期：`message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta` → `message_stop`
- 推理：`thinking` block + `signature`（用于多轮验证）

### 0.4 Google Gemini API（两套协议）

Google 有**两套** API 协议：旧的 `generateContent` 和新的 `Interactions API`。

#### 0.4a generateContent API（旧，hydra 当前使用）

| 文档 | URL |
|------|-----|
| **API Reference** | https://ai.google.dev/api/generate-content |
| **Function Calling** | https://ai.google.dev/gemini-api/docs/generate-content/function-calling |

**关键特性**：
- 消息容器：`contents[]`，角色 user/model
- 系统提示：`systemInstruction`（独立字段）
- Part 类型（完整列表）：
  - `text` — 文本
  - `thought` (bool) — 标记为推理内容的 text part
  - `functionCall` — 函数调用（`name`、`args`、`id`）
  - `functionResponse` — 函数响应（`name`、`response`、`id`）
  - `inlineData` — 内联数据（`mimeType`、`data`）
  - `fileData` — 文件引用（`mimeType`、`fileUri`）
  - `executableCode` — 可执行代码
  - `codeExecutionResult` — 代码执行结果
  - `videoMetadata` — 视频元数据
  - `thoughtSignature` — 思考签名（用于多轮验证）
- 工具定义：`tools[].functionDeclarations[]`
- 流式：SSE `data: {json}\n\n`（`:streamGenerateContent?alt=sse`）
- 推理：`thought: true` 标记 + `thinkingConfig.thinkingBudget`
- **无状态**：每次请求需传完整 `contents[]`
- **注意**：Antigravity v1internal 是 generateContent API 的私有变体，不是公开 API。v1internal 的实际行为（如 `thoughtSignature`、`requestType: "agent"` 等字段）在公开文档中没有描述。

#### 0.4b Interactions API（新，2026年6月 GA）

| 文档 | URL |
|------|-----|
| **Overview** | https://ai.google.dev/gemini-api/docs/interactions/interactions-overview |
| **Streaming** | https://ai.google.dev/gemini-api/docs/interactions/streaming |
| **Function Calling** | https://ai.google.dev/gemini-api/docs/interactions/function-calling |
| **Migration Guide** | https://ai.google.dev/gemini-api/docs/migrate-to-interactions |
| **Breaking Changes (May 2026)** | https://ai.google.dev/gemini-api/docs/interactions-breaking-changes-may-2026 |
| **GA Blog Post** | https://blog.google/innovation-and-ai/technology/developers-tools/interactions-api-general-availability/ |

**关键特性**：
- 核心资源：`Interaction`（一次对话 turn）
- **有状态**：`previous_interaction_id` 保留对话历史，服务端管理 context
- **Steps 模型**：不再是 `contents[]` + `parts[]`，而是 `steps[]` 时间线
- Step 类型：
  - `user_input` — 用户输入（仅 GET 返回，POST 响应不包含）
  - `model_output` — 模型输出（text/image/audio delta）
  - `thought` — 推理（thought_signature/thought_summary delta）
  - `function_call` — 函数调用（arguments_delta）
  - `function_result` — 函数结果
  - `google_search_call` / `google_search_result` — Google 搜索
  - `code_execution_call` / `code_execution_result` — 代码执行
- 流式事件生命周期：
  - `interaction.created` → 一系列 `step.start` → `step.delta`(s) → `step.stop` → `interaction.completed`
- **背景执行**：`background=true` 用于长时间任务（Deep Think、Deep Research）
- **不支持**：自定义 safety settings、Batch API、显式 caching（有隐式 caching）
- **未来方向**：Google 表示新前沿能力将越来越多地只在 Interactions API 上发布

**与 generateContent 的关键差异**：
| 特性 | generateContent | Interactions API |
|------|----------------|------------------|
| 状态管理 | 无状态，每次传完整 contents | 有状态，`previous_interaction_id` |
| 数据模型 | `contents[].parts[]` (flat) | `steps[]` (typed timeline) |
| 流式格式 | `data: {json}` (无 event type) | `event: step.start\ndata: {json}` (语义化) |
| 函数调用流式 | 完整 functionCall 在一个 chunk | arguments 逐字符流式 `arguments_delta` |
| 推理流式 | thought text 在 text part 中 | 独立 `thought` step 类型 |
| 工具结果 | `functionResponse` part in contents | `function_result` step |
| 背景执行 | 不支持 | `background=true` |
| Safety settings | 支持 | 不支持 |

**对 hydra 的影响**：
- hydra 当前使用 generateContent（v1internal 变体）
- 如果 Antigravity 上游未来迁移到 Interactions API，hydra 的翻译层需要大幅重写
- Interactions API 的 step 模型与 OpenAI Responses API 的 item 模型更接近，翻译会更直观

### 0.5 OpenAI-compatible Provider（穿透）

无独立官方文档。这是事实标准——任何声称兼容 OpenAI Chat Completions API 的 provider（DeepSeek、Zhipu、Moonshot 等）都遵循 [0.1 OpenAI Chat Completions API](#01-openai-chat-completions-api) 的格式。

各 provider 的差异主要在：
- 支持的参数子集（如 `response_format`、`tool_choice`）
- 工具调用支持的完整度
- 流式 chunk 格式的细微差异
- 推理/思考字段的非标准扩展（如 DeepSeek 的 `reasoning_content`）

## 1. 背景

Hydra 作为单端点代理，需要在 **6 种通信协议** 之间做双向翻译（Google 有两套）：

| # | 格式 | 方向 | 端点 | hydra 使用 |
|---|------|------|------|------------|
| 1 | OpenAI Chat Completions | 入站 | `/v1/chat/completions` | ✅ |
| 2 | OpenAI Responses API | 入站 | `/responses`, `/v1/responses` | ✅ |
| 3 | Anthropic Messages | 入站 | `/v1/messages` | ✅ |
| 4a | Gemini generateContent | 出站上游 | `v1internal:generateContent` | ✅ (v1internal 变体) |
| 4b | Gemini Interactions API | 出站上游 | `/interactions` | ❌ 未使用 |
| 5 | OpenAI-compatible Provider | 出站穿透 | 配置的 API key provider | ✅ |

核心翻译路径：

```
入站协议 (1/2/3) ──请求──▶ Gemini v1internal (4)
入站协议 (1/2/3) ◀──响应── Gemini v1internal (4)
入站协议 (3) ──请求──▶ OpenAI-compatible (5) ──响应──▶ Anthropic (3)
入站协议 (2) ──请求──▶ OpenAI-compatible (5) ──响应──▶ Responses (2)
```

## 2. 已确认的翻译缺口

### 2.1 [P0] Codex local_shell 工具链路完全断裂

**影响客户端**：Codex CLI v0.149.0+

**根因**：Codex 使用 `local_shell` 工具类型执行 shell 命令。hydra 在请求和响应两个方向都没有正确翻译这个工具类型。

**请求方向缺口**：

| 环节 | 代码位置 | 问题 |
|------|----------|------|
| 工具定义 | `responses.go:248` `normalizeResponsesTools` | `type: "local_shell"` 不匹配 `type: "function"`，原样传给 `TransformRequest` |
| 工具定义 | `mapper.go:145-164` `TransformRequest` | 只处理 `t["function"]` 格式，`local_shell` 没有 `function` 字段 → **被跳过**，Gemini 不知道这个工具 |
| 工具调用 | `responses.go:189` `responsesItemToMessage` | `local_shell_call` 被显式跳过（return nil）→ **模型看不到自己之前的工具调用** |
| 工具结果 | `responses.go:186-192` | `local_shell_call_output` **完全不在 case 列表中** → 落入 default → 无 `role` 字段 → return nil → **命令输出被丢弃** |

**响应方向缺口**：

| 环节 | 代码位置 | 问题 |
|------|----------|------|
| 非流式 | `responses.go:405-421` `responsesPartsToOutputItems` | Gemini 的 `functionCall` → `function_call` 类型，但 Codex 期望 `local_shell_call` 类型 |
| 流式 | `responses.go:895-924` `processGeminiChunk` | 同上，SSE 事件类型不匹配 |

**实际表现**：
- Codex 执行 `cat ~/.agent-rules.md` 后，结果作为 `local_shell_call_output` 发回 hydra
- hydra 丢弃了这个 item，模型完全看不到命令输出
- 模型在无上下文的情况下继续生成，产生异常输出
- SSE 事件类型不匹配导致 Codex 客户端渲染异常（`}` 漏出）

**修复方向**：
1. 请求方向：把 `local_shell` 工具定义转成 Gemini `functionDeclarations`（包装为 `run_shell` 函数）
2. 请求方向：把 `local_shell_call` 转成 assistant 消息带 `tool_calls`
3. 请求方向：把 `local_shell_call_output` 转成 `tool` 角色消息
4. 响应方向：把 Gemini 返回的 `functionCall`（name=`run_shell`）转回 `local_shell_call` 类型

### 2.2 [P0] Provider 穿透流式 Anthropic 响应丢失 tool_calls

**代码位置**：`apikey_provider.go:850-996` `convertOpenAIStreamToAnthropic`

**问题**：该函数只处理 `reasoning_content` 和 `content` delta，**不处理 `tool_calls` delta**。

**影响**：当 Anthropic 客户端（如 Claude Code）通过 hydra 使用配置的 OpenAI-compatible provider 时，如果 provider 返回流式 tool call，tool call 不会被翻译成 Anthropic 的 `content_block_start (tool_use)` + `input_json_delta` 事件。客户端收不到工具调用。

### 2.3 [P1] Responses API 未处理的 item 类型

以下 item 类型在 `responsesItemToMessage` 中**完全不在 case 列表中**（不是显式跳过，是根本没出现）：

| Item 类型 | 来源 | 影响 |
|-----------|------|------|
| `local_shell_call_output` | Codex shell 执行结果 | **命令输出被丢弃**（2.1 的组成部分） |
| `mcp_tool_call` | MCP 工具调用 | 工具调用历史丢失 |
| `mcp_tool_call_output` | MCP 工具结果 | 工具结果丢失 |
| `mcp_list_tools` | MCP 工具列表 | 丢失 |
| `mcp_approval_request` | MCP 审批请求 | 丢失 |
| `mcp_approval_response` | MCP 审批响应 | 丢失 |
| `shell_call` | 新版 shell 工具 | 丢失 |
| `shell_call_output` | 新版 shell 结果 | 丢失 |
| `apply_patch_call` | Codex patch 工具 | 丢失 |
| `apply_patch_call_output` | Codex patch 结果 | 丢失 |
| `item_reference` | 引用历史 item | 丢失 |
| `computer_call` | Computer Use | 丢失 |
| `computer_call_output` | Computer Use 结果 | 丢失 |
| `file_search_call` | 文件搜索 | 丢失 |

这些 item 落入 `default` 分支，因为没有 `role` 字段，全部返回 `nil`，被静默丢弃。

### 2.4 [P1] Anthropic `document` content block 未处理

**代码位置**：`anthropic.go:149-254` `anthropicMessageToContent`

**问题**：Anthropic API 支持 `document` 类型的 content block（用于 PDF 等），hydra 没有处理。

**影响**：Claude Code 发送包含 document 的消息时，document 内容被丢弃。

### 2.5 [P2] Gemini 响应方向未处理的 part 类型

以下 Gemini part 类型在**所有响应翻译路径**中都被忽略：

| Gemini Part | 含义 | 影响 |
|-------------|------|------|
| `inlineData` | 图片/音频输出 | 模型生成的图片/音频丢失 |
| `fileData` | 文件引用 | 丢失 |
| `executableCode` | 代码执行 | 丢失 |
| `codeExecutionResult` | 代码执行结果 | 丢失 |
| `videoMetadata` | 视频元数据 | 丢失 |
| `thoughtSignature` | 思考签名 | 响应方向不传递（仅请求方向使用） |

**当前影响较小**：Antigravity 上游目前主要返回 text/thought/functionCall。但如果上游开始返回代码执行结果或图片，这些内容会丢失。

### 2.6 [P2] Provider 穿透 normalizeTools 丢弃非 function 工具类型

**代码位置**：`apikey_provider.go:118-145` `normalizeTools`

**问题**：过滤只保留 `type: "function"` 的工具定义，丢弃 `custom`、`web_search`、`file_search`、`local_shell` 等类型。

**影响**：这是**有意设计**——DeepSeek、Zhipu 等 provider 只支持 function 工具。但需要在文档中明确说明。

### 2.7 [P2] Schema 标准化丢失大量 JSON Schema 特性

**代码位置**：`mapper.go:534-597` `normalizeSchemaTypes`

**被移除的字段**：`format`, `strict`, `$schema`, `definitions`, `exclusiveMinimum`, `exclusiveMaximum`, `default`, `examples`, `pattern`, `multipleOf`, `minLength`, `maxLength`, `minItems`, `maxItems`, `minProperties`, `maxProperties`, `uniqueItems`, `const`, `enum`, `title`, `$ref`, `additionalProperties`, `propertyNames`, `oneOf`, `anyOf`, `allOf`, `not`

**影响**：工具的 JSON Schema 被简化后传给 Gemini。Gemini 不支持这些字段，所以移除是正确的，但复杂的工具定义可能丢失语义约束。

## 3. 翻译路径全景图

### 3.1 请求方向（入站 → Gemini 上游）

```
OpenAI Chat ──TransformRequest──────────────▶ Gemini
              mapper.go:46
              ✅ system/user/assistant/tool
              ✅ tool_calls → functionCall
              ✅ reasoning_content → thought
              ✅ image_url/input_audio → inlineData
              ✅ tools → functionDeclarations
              ⚠️ schema 简化

Responses ──responsesRequestToOpenAI──openAIMessageToContent──▶ Gemini
            responses.go:26          mapper.go:361
            ✅ message/function_call/function_call_output
            ✅ custom_tool_call/custom_tool_call_output
            ✅ reasoning
            ❌ local_shell_call (跳过)
            ❌ local_shell_call_output (未处理)
            ❌ mcp_*/shell_call/apply_patch_call 等 (未处理)
            ⚠️ local_shell 工具定义未转成 function

Anthropic ──AnthropicTransformRequest──────────▶ Gemini
            anthropic.go:9
            ✅ text/thinking/redacted_thinking
            ✅ image/tool_use/tool_result
            ✅ tools → functionDeclarations
            ❌ document (未处理)
```

### 3.2 响应方向（Gemini → 入站协议）

```
Gemini ──TransformResponse──────────────▶ OpenAI Chat
         mapper.go:599
         ✅ text → content
         ✅ thought → reasoning_content
         ✅ functionCall → tool_calls
         ❌ inlineData/fileData/executableCode/codeExecutionResult

Gemini ──streamOpenAISSE────────────────▶ OpenAI Chat SSE
         server.go:1416
         ✅ text/thought/functionCall (同上)
         ❌ 同上

Gemini ──transformResponsesResponse──────▶ Responses
         responses.go:288
         ✅ text → message (output_text)
         ✅ thought → reasoning
         ✅ functionCall → function_call
         ❌ inlineData 等
         ⚠️ function_call 类型，Codex 期望 local_shell_call

Gemini ──streamResponsesSSE──────────────▶ Responses SSE
         server.go:1590 + responses.go:823
         ✅ text/thought/functionCall (同上)
         ❌ 同上
         ⚠️ 同上

Gemini ──AnthropicTransformResponse──────▶ Anthropic
         anthropic.go:413
         ✅ text → text block
         ✅ thought → thinking block
         ✅ functionCall → tool_use block
         ❌ inlineData 等

Gemini ──streamAnthropicSSE──────────────▶ Anthropic SSE
         server.go:1692 + anthropic.go:615
         ✅ text/thought/functionCall (同上)
         ❌ 同上
```

### 3.3 Provider 穿透方向

```
Anthropic ──anthropicRequestToOpenAIChat──▶ OpenAI Chat ──▶ Provider
            apikey_provider.go:344
            ✅ text/image/tool_use/tool_result
            ✅ tools/tool_choice
            ⚠️ tool_use/tool_result 在 content 级别被忽略
                (在 message 级别处理)

Provider ──openAIChatToAnthropic──────────▶ Anthropic (非流式)
           apikey_provider.go:516
           ✅ content → text block
           ✅ reasoning_content → thinking block
           ✅ tool_calls → tool_use block

Provider ──convertOpenAIStreamToAnthropic─▶ Anthropic SSE
           apikey_provider.go:850
           ✅ content → text_delta
           ✅ reasoning_content → thinking_delta
           ❌ tool_calls → tool_use (未实现!)

Provider ──openAIChatToResponses──────────▶ Responses (非流式)
           apikey_provider.go:619
           ✅ content → message (output_text)
           ✅ reasoning_content → reasoning
           ✅ tool_calls → function_call

Provider ──convertOpenAIStreamToResponses─▶ Responses SSE
           apikey_provider.go:743
           ✅ content → output_text.delta
           ✅ reasoning_content → reasoning_summary.delta
           ✅ tool_calls → function_call_arguments.delta
```

## 4. 五种格式特性对比

### 4.1 消息/内容结构

| 特性 | OpenAI Chat | Responses | Anthropic | Gemini | Provider |
|------|-------------|-----------|-----------|--------|----------|
| 消息容器 | `messages[]` | `input[]` (items) | `messages[]` | `contents[]` | `messages[]` |
| 系统提示 | `role: "system"` | `instructions` | `system` (独立字段) | `systemInstruction` | `role: "system"` |
| 角色名 | system/user/assistant/tool | user/assistant/system/developer | user/assistant | user/model | system/user/assistant/tool |
| 内容格式 | string 或 array | typed items | typed blocks | typed parts | string 或 array |
| 多模态 | image_url, input_audio | input_text, input_image | image (base64) | inlineData | image_url, input_audio |

### 4.2 工具调用

| 特性 | OpenAI Chat | Responses | Anthropic | Gemini |
|------|-------------|-----------|-----------|--------|
| 工具定义 | `tools[].function` | `tools[]` (flat) | `tools[].input_schema` | `functionDeclarations[]` |
| 工具调用 | `tool_calls[]` | `function_call` item | `tool_use` block | `functionCall` part |
| 工具结果 | `role: "tool"` msg | `function_call_output` item | `tool_result` block | `functionResponse` part |
| 工具 ID | `id` | `call_id` | `id` | `id` |
| 参数格式 | JSON string | JSON string | object | object |
| 特殊工具 | — | local_shell, mcp, web_search, file_search, computer, apply_patch | — | — |

### 4.3 推理/思考

| 特性 | OpenAI Chat | Responses | Anthropic | Gemini |
|------|-------------|-----------|-----------|--------|
| 推理内容 | `reasoning_content` | `reasoning` item | `thinking` block | `thought: true` part |
| 推理签名 | — | — | `signature` field | `thoughtSignature` field |
| 加密推理 | — | `encrypted_content` | `redacted_thinking` block | — |
| 推理级别 | `reasoning_effort` | `reasoning.effort` | `thinking.budget_tokens` | `thinkingConfig.thinkingBudget` |

### 4.4 流式事件

| 特性 | OpenAI Chat | Responses | Anthropic |
|------|-------------|-----------|-----------|
| 事件格式 | `data: {json}\n\n` | `event: type\ndata: {json}\n\n` | `event: type\ndata: {json}\n\n` |
| 结束标记 | `data: [DONE]` | `response.completed` event | `message_stop` event |
| 增量类型 | `delta.content` | `response.output_text.delta` | `text_delta` |
| 推理增量 | `delta.reasoning_content` | `response.reasoning_summary.delta` | `thinking_delta` |
| 工具增量 | `delta.tool_calls` | `response.function_call_arguments.delta` | `input_json_delta` |
| 生命周期 | 无 | output_item.added/done, content_part.added/done | content_block_start/stop |

## 5. 修复优先级

| 优先级 | 问题 | 影响面 | 修复复杂度 |
|--------|------|--------|------------|
| P0 | local_shell 工具链路断裂 | Codex CLI 无法执行 shell 命令 | 中 |
| P0 | Provider 流式 Anthropic 丢失 tool_calls | Claude Code + provider 流式工具调用失效 | 低 |
| P1 | Responses API 未处理 item 类型 | MCP/shell/patch 等工具历史丢失 | 中 |
| P1 | Anthropic document block 未处理 | PDF 等文档内容丢失 | 低 |
| P2 | Gemini 响应 part 类型未处理 | 代码执行/图片输出丢失 | 低 |
| P2 | Schema 标准化丢失特性 | 复杂工具定义语义弱化 | 已知限制 |
| P2 | normalizeTools 丢弃非 function 工具 | Provider 不支持的工具被过滤 | 有意设计 |

## 6. 附录：各翻译函数索引

### 请求方向

| 函数 | 文件 | 行号 | 路径 |
|------|------|------|------|
| `TransformRequest` | mapper.go | 46 | OpenAI Chat → Gemini |
| `openAIMessageToContent` | mapper.go | 361 | OpenAI msg → Gemini content |
| `transformToolOpenAI` | mapper.go | 515 | OpenAI tool → Gemini tool |
| `normalizeSchemaTypes` | mapper.go | 534 | JSON Schema → Gemini Schema |
| `responsesRequestToOpenAI` | responses.go | 26 | Responses → OpenAI Chat |
| `responsesItemToMessage` | responses.go | 95 | Responses item → Chat msg |
| `normalizeResponsesTools` | responses.go | 248 | Responses tools → Chat tools |
| `AnthropicTransformRequest` | anthropic.go | 9 | Anthropic → Gemini |
| `anthropicMessageToContent` | anthropic.go | 149 | Anthropic block → Gemini part |
| `transformToolAnthropic` | anthropic.go | 329 | Anthropic tool → Gemini tool |
| `anthropicRequestToOpenAIChat` | apikey_provider.go | 344 | Anthropic → OpenAI Chat (provider) |

### 响应方向

| 函数 | 文件 | 行号 | 路径 |
|------|------|------|------|
| `TransformResponse` | mapper.go | 599 | Gemini → OpenAI Chat |
| `streamOpenAISSE` | server.go | 1416 | Gemini SSE → OpenAI Chat SSE |
| `buildOpenAISSEChunk` | server.go | 1497 | Gemini chunk → OpenAI SSE chunk |
| `transformResponsesResponse` | responses.go | 288 | Gemini → Responses |
| `responsesPartsToOutputItems` | responses.go | 353 | Gemini parts → Responses items |
| `streamResponsesSSE` | server.go | 1590 | Gemini SSE → Responses SSE |
| `processGeminiChunk` | responses.go | 823 | Gemini chunk → Responses SSE events |
| `AnthropicTransformResponse` | anthropic.go | 413 | Gemini → Anthropic |
| `streamAnthropicSSE` | server.go | 1692 | Gemini SSE → Anthropic SSE |
| `ProcessChunk` | anthropic.go | 615 | Gemini chunk → Anthropic SSE events |
| `openAIChatToAnthropic` | apikey_provider.go | 516 | OpenAI Chat → Anthropic (provider) |
| `openAIChatToResponses` | apikey_provider.go | 619 | OpenAI Chat → Responses (provider) |
| `convertOpenAIStreamToAnthropic` | apikey_provider.go | 850 | OpenAI SSE → Anthropic SSE (provider) |
| `convertOpenAIStreamToResponses` | apikey_provider.go | 743 | OpenAI SSE → Responses SSE (provider) |

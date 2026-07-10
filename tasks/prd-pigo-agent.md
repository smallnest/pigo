# PRD: pigo — 用 Go 复刻 pi Agent Harness

## Introduction

pigo 是用 Go 语言重新实现的 pi agent harness。目标是把 pi（一套 TypeScript/Node.js 实现的、可自我扩展的编码 agent）的**设计思路和核心控制流严格复刻到 Go**，形成一个原生编译、单二进制分发、无 Node 运行时依赖的编码 agent。

pi 本身是一个 monorepo，分为五层：`pi-ai`（多厂商 LLM 统一 API）、`pi-agent-core`（agent 运行时 / loop）、`pi-tui`（终端 UI）、`pi-coding-agent`（面向用户的 `pi` CLI）、`pi-orchestrator`（多 agent 编排）。pigo 将逐层对标这套分层。

关键约束（源自前期技术讨论）：
- **agent loop 严格复刻 pi**：双层循环 + 三段式工具执行（prepare → execute → finalize）+ 完整钩子体系（beforeToolCall / afterToolCall / prepareNextTurn / shouldStopAfterTurn / getSteeringMessages / getFollowUpMessages）。这是 pi 最核心、最值得原样保留的资产。
- **语言差异需重新设计而非直译**：TS 的 async generator → Go channel；Promise.all 保序并行 → goroutine + 按 index 回填；discriminated union → interface + type switch；typebox 运行时泛型校验 → JSON Schema 运行时校验。
- **TUI 参考 zero（Gitlawb/zero，Go 实现的类似 agent），不自研差分渲染**，底层可用 charmbracelet/bubbletea 生态。
- **Provider 层的接口设计参考 pi，具体 Go 实现可参考 zero**。

读者假设：本文面向初级开发者或 AI agent，术语首次出现时给出解释。

参考代码：
- pi agent: /Users/chaoyuepan/ai/pi
- zero (go coding agent): https://github.com/Gitlawb/zero

## Goals

- 用 Go 实现一个可运行的编码 agent，命令行下能完成多轮对话 + 工具调用（读写文件、执行命令、搜索）。
- **严格复刻 pi 的 agent loop 控制流**：双层循环、三段式工具执行、全部六个钩子的语义与调用时机与 pi 一致。
- 提供统一的 `Provider` 抽象，支持多厂商 LLM（对齐 pi 的 provider 覆盖面），首要保证流式响应正确。
- 工具参数用 **JSON Schema 运行时校验**，对齐 pi 的 typebox 设计意图（模型给的参数在执行前被校验/拒绝）。
- 提供交互式 TUI（参考 zero 的 Go 实现）与 headless/stdio 两种运行模式。
- 全量对标 pi：会话持久化、配置系统、多 agent 编排（orchestrator）、MCP、sandbox/安全等能力纳入路线图（分阶段交付）。
- 单二进制分发，无 Node 运行时依赖。

## User Stories

> 编号规则：US-NNN。每个 US 尽量可在一个专注的开发会话内完成，且可独立实现。UI 相关 story 需包含"可视化验证"。pigo 是命令行/TUI 项目，"浏览器验证"不适用，改为"在真实终端运行验证"。

### 阶段一：核心 loop 与类型基座（对标 pi-agent-core）

### US-001: 定义核心消息与上下文类型
**Description:** 作为开发者，我需要在 Go 中定义与 pi 对齐的核心类型，作为整个 loop 的数据基座。

**Acceptance Criteria:**
- [ ] 定义 `Message` 相关类型：user / assistant / toolResult 三种角色，assistant 消息 content 支持 text / thinking / toolCall 块（对应 pi 的 discriminated union，用 interface + type switch 或 tagged struct 实现）
- [ ] 定义 `AgentContext`（systemPrompt / messages / tools）、`AgentTool`、`AgentToolCall`、`AgentToolResult`、`AgentEvent`
- [ ] `AgentEvent` 覆盖 pi 的全部事件类型：agent_start / agent_end / turn_start / turn_end / message_start / message_update / message_end / tool_execution_start / tool_execution_update / tool_execution_end
- [ ] `AgentToolResult` 含 content / details / terminate 字段，语义与 pi 一致
- [ ] `go build ./...` 通过，`go vet` 无告警

### US-002: 实现流式事件通道（对标 EventStream / async generator）
**Description:** 作为开发者，我需要一个基于 channel 的事件流机制，替代 pi 的 async generator，用于 loop 向外发送事件。

**Acceptance Criteria:**
- [ ] 提供 `EventSink`（emit 单个事件）与事件流读取端，基于 `chan AgentEvent` 实现
- [ ] 支持 context 取消（`context.Context` / abort signal 等价物）
- [ ] loop 结束时能返回最终 `[]AgentMessage`（对应 pi EventStream 的 result）
- [ ] 有单元测试覆盖：正常结束、取消中断两种路径
- [ ] `go test ./...` 通过

### US-003: 实现单个 assistant 响应的流式处理
**Description:** 作为开发者，我需要复刻 pi 的 `streamAssistantResponse`，把上下文转换为 provider 请求并流式回填 partial 消息。

**Acceptance Criteria:**
- [ ] 调用前执行 `transformContext`（可选）与 `convertToLlm`，与 pi 顺序一致
- [ ] 通过 `getApiKey` 动态解析 API key（支持过期 token 场景），解析不到时回退到静态 key
- [ ] 流式过程中把 partial assistant 消息实时更新进 context，并发出 message_start / message_update / message_end 事件
- [ ] done / error 事件后返回最终 assistant 消息（含 stopReason）
- [ ] 有单元测试用 fake provider 覆盖流式回填
- [ ] `go test ./...` 通过

### US-004: 实现工具调用三段式（prepare → execute → finalize）
**Description:** 作为开发者，我需要严格复刻 pi 的工具执行三段式，含 beforeToolCall / afterToolCall 钩子。

**Acceptance Criteria:**
- [ ] `prepare`：查工具注册表 → `prepareArguments`（可选）→ JSON Schema 校验参数 → `beforeToolCall` 钩子（返回 block 则不执行，产出 error tool result）
- [ ] `execute`：调用工具，支持通过 update callback 发出 tool_execution_update；抛错时转为 error tool result 而非 panic
- [ ] `finalize`：`afterToolCall` 钩子可按字段覆盖 content / details / isError / terminate（无深合并，与 pi 一致）
- [ ] 工具未找到、参数校验失败、abort 均产出对应的 error tool result
- [ ] 有单元测试覆盖：正常执行、block、校验失败、afterToolCall 覆盖、terminate
- [ ] `go test ./...` 通过

### US-005: 实现并行与串行工具执行
**Description:** 作为开发者，我需要复刻 pi 的两种工具执行模式，并保证并行时结果保序。

**Acceptance Criteria:**
- [ ] `sequential` 模式：逐个 prepare→execute→finalize，遇 abort 中断
- [ ] `parallel` 模式：先顺序 prepare，再并发执行允许的工具（goroutine），最终 tool-result 消息按 assistant 源顺序输出（保序）
- [ ] 任一工具的 `executionMode == "sequential"` 或全局配置为 sequential 时，整批走串行
- [ ] `terminate`：仅当整批每个 finalized 结果都 terminate=true 时才提示终止（与 pi 一致）
- [ ] 有单元测试验证并行保序与整批 terminate 语义
- [ ] `go test ./...` 通过

### US-006: 实现双层主循环（runLoop）
**Description:** 作为开发者，我需要严格复刻 pi 的双层 agent 循环，串起流式响应、工具执行与钩子。

**Acceptance Criteria:**
- [ ] 内层循环：处理 pending/steering 消息 → 流式 assistant 响应 → 执行工具调用 → 回灌结果，直到无更多 tool call
- [ ] 外层循环：内层结束后拉取 `getFollowUpMessages`，有则作为 pending 继续，无则退出
- [ ] 每轮 turn_end 后调用 `prepareNextTurn`（可替换 context / model / thinkingLevel）与 `shouldStopAfterTurn`（返回 true 则发 agent_end 并退出）
- [ ] `getSteeringMessages` 在每轮工具执行后被拉取并注入下一轮之前
- [ ] `stopReason == "length"`（输出被 token 上限截断）时，该消息的所有 tool call 一律判失败，提示模型重发（复刻 `failToolCallsFromTruncatedMessage`）
- [ ] stopReason 为 error / aborted 时发 turn_end + agent_end 并退出
- [ ] 提供 `agentLoop`（新 prompt 启动）与 `agentLoopContinue`（从现有 context 续跑）两个入口，续跑入口校验最后一条消息不是 assistant
- [ ] 有集成测试用 fake provider 驱动一次"文本→工具调用→文本"的完整多轮
- [ ] `go test ./...` 通过

### 阶段二：Provider 层（对标 pi-ai）

### US-007: 定义 Provider 接口与流式契约
**Description:** 作为开发者，我需要一个统一的 `Provider` 接口，屏蔽各厂商差异。

**Acceptance Criteria:**
- [ ] 定义 `Provider` 接口，核心方法为流式 completion（输入 model + context + options，输出事件流）
- [ ] 流式契约与 pi 的 `StreamFn` 一致：不通过抛错/reject 表达请求失败，而是在流内以事件 + 终态 assistant 消息（stopReason=error/aborted + errorMessage）表达
- [ ] 定义 `Model` 元数据结构（provider / id / 能力标记如是否支持 thinking / 上下文窗口等）
- [ ] `go build ./...` 通过

### US-008: 实现 Anthropic provider（流式）
**Description:** 作为用户，我希望 pigo 能通过 Anthropic Claude 完成对话与工具调用。

**Acceptance Criteria:**
- [ ] 实现 Anthropic Messages API 的流式请求，正确解析 text / thinking / tool_use 增量
- [ ] 正确上报 usage（输入/输出 token）
- [ ] 正确映射 stopReason（含 length / tool_use / end_turn）
- [ ] 网络错误、限流、鉴权失败以流内终态 error 消息表达而非 panic
- [ ] 有针对 SSE 解析的单元测试（用录制的 fixture）
- [ ] `go test ./...` 通过

### US-009: 实现 OpenAI 兼容 provider（流式）
**Description:** 作为用户，我希望 pigo 支持 OpenAI 及 OpenAI 兼容接口（含大量第三方网关）。

**Acceptance Criteria:**
- [ ] 实现 OpenAI Chat Completions（或 Responses）流式，解析 tool_calls 增量与文本增量
- [ ] 支持自定义 base URL（覆盖 OpenAI 兼容的第三方 provider）
- [ ] stopReason / usage 映射正确
- [ ] 有 SSE 解析单元测试
- [ ] `go test ./...` 通过

### US-010: 实现 Google Gemini provider（流式）
**Description:** 作为用户，我希望 pigo 支持 Google Gemini。

**Acceptance Criteria:**
- [ ] 实现 Gemini 流式 generateContent，解析文本与 functionCall
- [ ] stopReason / usage 映射正确
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### US-011: 模型注册表与 provider 目录
**Description:** 作为开发者，我需要一个模型注册表，把 model id 解析到对应 provider 与能力元数据。

**Acceptance Criteria:**
- [ ] 提供 `model-registry`，支持按 id 查询 Model 元数据与其 Provider
- [ ] 模型清单可由数据文件生成（对标 pi 的 `models.generated`），支持内置默认清单
- [ ] 未知 model id 返回明确错误
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### US-012: OAuth 与 API key 解析
**Description:** 作为用户，我希望 pigo 支持 API key 与 OAuth 两种鉴权，并能处理短时 token 过期。

**Acceptance Criteria:**
- [ ] 支持从环境变量与配置文件读取 API key（按 provider）
- [ ] 支持 OAuth 流程（对标 pi 的 oauth；至少覆盖一个 OAuth provider 作为样例）
- [ ] token 过期时 `getApiKey` 能刷新并返回新 token
- [ ] 敏感值不写入日志（按 key 名引用而非明文）
- [ ] `go test ./...` 通过

### US-013: 扩展更多 provider（对齐 pi 覆盖面）
**Description:** 作为用户，我希望 pigo 覆盖 pi 支持的主要 provider（Bedrock、Mistral、DeepSeek、xAI、Groq、OpenRouter 等）。

**Acceptance Criteria:**
- [ ] 每个新增 provider 复用统一 `Provider` 接口，仅实现差异部分
- [ ] 至少再交付 3 个 provider（优先 Bedrock、OpenRouter、DeepSeek），其余登记在 Open Questions/路线图
- [ ] 每个 provider 有最小流式单元测试
- [ ] `go test ./...` 通过

### 阶段三：工具系统（对标 coding-agent/core/tools）

### US-014: 工具注册表与 JSON Schema 校验
**Description:** 作为开发者，我需要工具注册表，并在执行前用 JSON Schema 校验模型给的参数。

**Acceptance Criteria:**
- [ ] 提供 `Registry`：按 name 注册/查询工具
- [ ] 每个工具声明 JSON Schema 参数定义；执行前用 schema 校验库（如 santhosh-tekuri/jsonschema）校验
- [ ] 校验失败产出明确的 error tool result（含字段级错误信息）
- [ ] 有单元测试覆盖：合法参数、缺字段、类型错误
- [ ] `go test ./...` 通过

### US-015: read 工具
**Description:** 作为用户，我希望 agent 能读取文件内容。

**Acceptance Criteria:**
- [ ] 支持按路径读取，支持行 offset/limit（对标 pi 的 read）
- [ ] 输出带行号；超大文件截断并提示
- [ ] 路径越界/不存在返回明确错误
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### US-016: write 工具
**Description:** 作为用户，我希望 agent 能创建/覆盖文件。

**Acceptance Criteria:**
- [ ] 支持写入指定路径，必要时创建父目录
- [ ] 覆盖已存在文件前的行为与 pi 一致（记录/提示）
- [ ] 写入失败返回明确错误
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### US-017: edit 工具（含 diff）
**Description:** 作为用户，我希望 agent 能对文件做精确的字符串替换编辑。

**Acceptance Criteria:**
- [ ] 支持 old_string → new_string 精确替换，old_string 不唯一时报错
- [ ] 支持 replace_all 选项
- [ ] 返回 diff 供 UI 渲染
- [ ] 有单元测试覆盖：唯一匹配、非唯一匹配报错、replace_all
- [ ] `go test ./...` 通过

### US-018: bash 工具
**Description:** 作为用户，我希望 agent 能执行 shell 命令。

**Acceptance Criteria:**
- [ ] 执行命令并流式返回 stdout/stderr（通过 tool_execution_update）
- [ ] 支持超时与取消（context 取消能杀掉子进程）
- [ ] 退出码非 0 时结果标记 isError 并携带输出
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### US-019: grep / find / ls 搜索工具
**Description:** 作为用户，我希望 agent 能搜索文件内容与遍历目录。

**Acceptance Criteria:**
- [ ] grep：按 pattern 搜索文件内容，支持 glob 过滤
- [ ] find：按文件名 glob 查找
- [ ] ls：列目录，区分文件/目录
- [ ] 尊重 .gitignore（对标 pi 用 ignore 库的行为）
- [ ] 有单元测试
- [ ] `go test ./...` 通过

### 阶段四：CLI / 交互模式（对标 coding-agent）

### US-020: headless / stdio 运行模式
**Description:** 作为用户，我希望能以非交互方式（print / stdio）运行 pigo，便于脚本与 CI 集成。

**Acceptance Criteria:**
- [ ] 提供 print 模式：接收一个 prompt，运行 agent，输出最终结果到 stdout
- [ ] 提供 stream-json / stdio 协议模式（对标 pi 的 rpc / print-mode），事件以行分隔 JSON 输出
- [ ] 退出码正确反映成功/失败
- [ ] 有端到端测试（用 fake provider）
- [ ] 在真实终端运行验证：`pigo -p "读取 README 并总结"` 能产出结果

### US-021: 系统提示词与上下文装配
**Description:** 作为开发者，我需要复刻 pi 的系统提示词装配，包括注入项目级 AGENTS.md。

**Acceptance Criteria:**
- [ ] 组装 system prompt：基础指令 + 环境信息（cwd / OS / 时间等）
- [ ] 从根目录到子目录按"通用→具体"顺序注入 AGENTS.md（对标 zero/pi 的 monorepo 行为）
- [ ] 有单元测试验证 AGENTS.md 注入顺序
- [ ] `go test ./...` 通过

### US-022: 交互式 TUI（参考 zero）
**Description:** 作为用户，我希望有一个交互式终端界面来与 agent 对话。

**Acceptance Criteria:**
- [ ] 基于成熟 Go TUI 库（bubbletea 生态或参考 zero 的实现），不自研差分渲染
- [ ] 显示对话 transcript：用户输入、assistant 文本、工具调用、工具结果
- [ ] 支持流式增量渲染（订阅 loop 的 message_update / tool_execution_update 事件）
- [ ] 支持中断当前运行（Ctrl+C 触发 abort，agent 优雅停止）
- [ ] 支持运行中输入 steering 消息（对接 getSteeringMessages）
- [ ] 在真实终端运行验证：能完成一次含工具调用的多轮对话
- [ ] `go build ./...` 通过

### US-023: 配置系统
**Description:** 作为用户，我希望通过分层配置控制 pigo 的行为（provider、model、工具开关等）。

**Acceptance Criteria:**
- [ ] 支持分层配置解析（默认 < 全局 < 项目 < 环境变量/命令行），对标 pi/zero 的 config resolution
- [ ] 配置项含：默认 model、provider 凭据、工具执行模式、思考等级
- [ ] 配置文件缺失/字段非法给出明确报错
- [ ] 有单元测试覆盖分层覆盖顺序
- [ ] `go test ./...` 通过

### US-024: 会话持久化
**Description:** 作为用户，我希望 pigo 能保存并恢复会话历史。

**Acceptance Criteria:**
- [ ] 会话以本地文件（如 JSONL）持久化，含消息与元数据
- [ ] 支持列出、恢复、续跑历史会话（对接 agentLoopContinue）
- [ ] 恢复后 transcript 能在 TUI 正确重放
- [ ] 有单元测试覆盖写入/读取/续跑
- [ ] `go test ./...` 通过

### 阶段五：全量对标（对标 orchestrator / MCP / sandbox）

### US-025: MCP 客户端支持
**Description:** 作为用户，我希望 pigo 能连接 MCP（Model Context Protocol）服务器以扩展工具。

**Acceptance Criteria:**
- [ ] 实现 MCP 客户端，支持 stdio 传输
- [ ] 能发现 MCP server 暴露的工具并注册进 Registry
- [ ] MCP 工具调用走统一的三段式执行路径
- [ ] 有集成测试（用一个最小 MCP server）
- [ ] `go test ./...` 通过

### US-026: 沙箱 / 权限约束
**Description:** 作为用户，我希望对文件、进程、网络访问有可配置的权限约束（pi 本身无内置权限系统，此处参考 zero 的 sandbox 设计增强）。

**Acceptance Criteria:**
- [ ] 提供权限策略配置（允许/拒绝的路径、命令、网络）
- [ ] 通过 beforeToolCall 钩子接入权限检查，拒绝时产出 error tool result
- [ ] 支持 secret redaction（工具输出中的密钥打码）
- [ ] 有单元测试覆盖：拒绝越权路径、拒绝越权命令
- [ ] `go test ./...` 通过

### US-027: 多 agent 编排（对标 orchestrator）
**Description:** 作为用户，我希望能编排多个 agent 协作（sub-agent / swarm），标记为实验性。

**Acceptance Criteria:**
- [ ] 支持派生 sub-agent（独立 context + 工具集），父 agent 通过工具调用触发
- [ ] sub-agent 的最终结果作为工具结果回灌父 agent
- [ ] 支持并发运行多个 sub-agent
- [ ] 有集成测试（用 fake provider 驱动一次父→子→父）
- [ ] `go test ./...` 通过

## Functional Requirements

- FR-1: 系统必须实现双层 agent 循环，内层处理"流式响应→工具调用→结果回灌"，外层处理 follow-up 消息，控制流与 pi 的 `runLoop` 一致。
- FR-2: 系统必须以三段式（prepare → execute → finalize）执行每个工具调用。
- FR-3: 系统必须在 prepare 阶段用 JSON Schema 校验工具参数，校验失败时产出 error tool result 而非执行工具。
- FR-4: 系统必须支持 beforeToolCall 钩子，返回 block 时阻止工具执行并产出 error tool result。
- FR-5: 系统必须支持 afterToolCall 钩子，按字段（content/details/isError/terminate）覆盖工具结果，不做深合并。
- FR-6: 系统必须支持 prepareNextTurn 钩子，可替换下一轮的 context / model / thinkingLevel。
- FR-7: 系统必须支持 shouldStopAfterTurn 钩子，返回 true 时发出 agent_end 并退出循环。
- FR-8: 系统必须支持 getSteeringMessages，在每轮工具执行后注入消息到下一轮之前。
- FR-9: 系统必须支持 getFollowUpMessages，在内层循环结束后决定是否继续。
- FR-10: 当 assistant 消息 stopReason 为 length（token 截断）时，系统必须将该消息的全部 tool call 判为失败并提示模型重发。
- FR-11: 系统必须支持 sequential 与 parallel 两种工具执行模式，parallel 模式下 tool-result 消息按 assistant 源顺序输出。
- FR-12: 系统必须仅在整批工具结果全部 terminate=true 时才提示提前终止。
- FR-13: 系统必须提供统一 Provider 接口，请求失败通过流内终态 error/aborted 消息表达，而非抛错。
- FR-14: 系统必须以流式方式处理 assistant 响应，实时更新 partial 消息并发出 message_update 事件。
- FR-15: 系统必须支持通过 getApiKey 动态解析 API key，以处理短时 token 过期。
- FR-16: 系统必须提供模型注册表，将 model id 解析到 Provider 与能力元数据。
- FR-17: 系统必须实现 read / write / edit / bash / grep / find / ls 内置工具。
- FR-18: 系统必须支持 headless（print / stdio）与交互式 TUI 两种运行模式。
- FR-19: 系统必须装配系统提示词，并按"通用→具体"顺序注入根到子目录的 AGENTS.md。
- FR-20: 系统必须支持分层配置解析（默认 < 全局 < 项目 < 环境/命令行）。
- FR-21: 系统必须支持会话本地持久化与恢复续跑。
- FR-22: 系统必须支持 context 取消（abort），中断时 loop 与运行中的工具/子进程优雅停止。
- FR-23: 系统必须编译为单二进制，无 Node 运行时依赖。
- FR-24: 系统必须提供基于 channel 的事件流机制，覆盖 pi 的全部 AgentEvent 类型。
- FR-25: 系统应支持 MCP 客户端（stdio 传输）以扩展工具。
- FR-26: 系统应提供可配置的权限/沙箱约束，通过 beforeToolCall 接入。
- FR-27: 系统应支持多 agent 编排（sub-agent / swarm，实验性）。

## Non-Goals (Out of Scope)

- 不复刻 pi 的自研差分渲染 TUI（改用成熟 Go TUI 库 / 参考 zero）。
- 不直接翻译 pi 的 TypeScript 代码（语言特性差异导致直译不可行；只复刻设计与控制流）。
- 首版不追求 provider 数量 100% 对齐 pi（先核心厂商 + 至少 6 个 provider，其余登记路线图）。
- 不实现 pi 的 HTML 导出、图像生成/处理（photon）、终端图片渲染等外围能力（后续可选）。
- 不实现浏览器相关验证（pigo 为 CLI/TUI，验证在真实终端进行）。
- 不提供托管服务或云端 orchestrator（orchestrator 仅本地多进程/多 agent）。
- 不承诺与 pi 的会话文件格式二进制兼容。

## Design Considerations

- **分层与 pi 一一对应**，便于对照移植：
  | pi (TS) | pigo (Go) |
  |---|---|
  | packages/ai | internal/llm（Provider 接口 + 各厂商实现，流式） |
  | packages/agent | internal/agent（loop.go：双层循环 + 三段式工具） |
  | coding-agent/core/tools | internal/tools（Registry + read/bash/edit/write/grep/find/ls） |
  | packages/tui | internal/tui（bubbletea / 参考 zero，不自研差分渲染） |
  | coding-agent 会话/配置 | internal/sessions、internal/config |
  | packages/orchestrator | internal/swarm（实验性） |
- **语言映射约定**：async generator → chan；Promise.all 保序 → goroutine + index 回填；discriminated union → interface + type switch；typebox 泛型校验 → JSON Schema 运行时校验；可选回调 → struct 中的 func 字段（nil 判断）。
- **可直接逐段翻译的参考**：pi 的 `packages/agent/src/agent-loop.ts`（runLoop / streamAssistantResponse / prepare/execute/finalize / failToolCallsFromTruncatedMessage）与 `packages/agent/src/types.ts`（AgentContext / AgentTool / AgentEvent / AgentLoopConfig 的钩子契约）。
- **Go 工程实现的第二参考**：Gitlawb/zero（Go 实现的类 agent），其 provider 流式 HTTP transport、TUI、sandbox 已有成熟 Go 实现。

## Technical Considerations

- 语言：Go（建议 1.22+）。目标单二进制、跨平台。
- JSON Schema 校验库候选：`santhosh-tekuri/jsonschema`。
- TUI 库候选：`charmbracelet/bubbletea` + `lipgloss` / `bubbles`，或参考 zero 的实现选型。
- 流式：HTTP + SSE 解析需自实现或用轻量库，注意断流重连与超时（对标 pi 的 streaming resilience）。
- 并发：工具并行执行用 goroutine + WaitGroup，结果按 index 回填保序；全程贯穿 `context.Context` 以支持取消。
- 安全：密钥仅按 key 名引用，禁止写入日志；工具输出支持 secret redaction。
- 目录约定：代码实现位于 `/Users/chaoyuepan/ai/pigo`，建议采用 `cmd/pigo`（入口）+ `internal/*`（各层）的标准 Go 布局。
- 测试：全程用 fake/faux provider（对标 pi 的 `providers/faux.ts`）驱动 loop 集成测试，避免依赖真实 API key。

## Success Metrics

- pigo 能在真实终端完成一次"读文件 → 执行命令 → 编辑文件"的多轮任务，全程工具调用正确。
- agent loop 的六个钩子与截断保护行为，均有单元/集成测试覆盖并通过。
- 至少 6 个 provider 的流式响应通过 fixture 单元测试。
- 单二进制在 macOS 与 Linux 上均可构建运行，无 Node 依赖。
- 核心包（internal/agent、internal/tools、internal/llm）测试覆盖率达到团队约定阈值（建议 ≥70%）。

## Open Questions

- provider 覆盖面的优先级排序：除 Anthropic/OpenAI/Gemini 外，先做哪 3 个（当前假设 Bedrock / OpenRouter / DeepSeek）？
- 会话文件格式是否需要与 pi 的 JSONL 保持字段级可互读，还是仅需 pigo 内部自洽？
- MCP 是否需要支持 HTTP/SSE 传输，还是首版仅 stdio 即可？
- sandbox 的隔离强度：仅进程内策略检查（beforeToolCall），还是需要引入 OS 级隔离（namespace / seccomp / 容器）？
- 多 agent 编排是否需要跨进程 IPC（对标 pi orchestrator 的 rpc-process），还是首版单进程内 goroutine 即可？
- 是否需要复刻 pi 的 skills / plugins / slash-commands 扩展体系，还是纳入后续独立 PRD？
- thinking/reasoning 等级如何跨 provider 归一（不同厂商对 thinking 的支持差异较大）？

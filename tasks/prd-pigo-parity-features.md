# PRD: pigo 对齐 pi Agent 的九项能力补齐

## Introduction

pigo 是用 Go 复刻的 pi coding agent。当前 pigo 已具备 agent loop、7 个内置工具（read/write/edit/bash/find/grep/ls）、多 provider（Anthropic/OpenAI/OpenRouter 等）、OAuth、hooks、skills、单进程 goroutine 子 agent、声明式 slash 命令、JSONL 会话持久化与 resume、headless/print/stream-json 模式。

本 PRD 覆盖 pigo 相较原始 pi agent（`/Users/chaoyuepan/ai/pi/packages`）仍缺失的九项能力。九项分两类来源：

- **从 pi 移植**：#1 上下文压缩、#5 会话管理增强、#6 多模态输入、#7 编排器、#8 插件系统、#9 项目信任。这些在 pi 中有成熟实现，Go 版按其算法与数据结构落地。
- **对标 Claude Code 新增**：#2 MCP 客户端、#3 TODO 任务工具、#4 WebFetch/WebSearch。这三项 pi 本身没有（全局检索仅在 OAuth/vendor 命中），是 pigo 为兼容 Claude Code 开放能力而新增。

面向读者可能是初级工程师或 AI agent，术语已尽量解释。

## Goals

- 让 pigo 支持长会话不撑爆上下文窗口（自动 + 手动压缩）。
- 补齐 pi 已有的会话管理体验：导出、导入、fork、clone、分支树、复制、会话统计。
- 提供图像输入能力，支持把图片喂给多模态模型。
- 提供跨语言插件机制，让业务团队能定制专属工具/命令（贴合 pigo "支持业务平台团队定制" 的定位）。
- 补齐 Claude Code 生态能力：MCP 客户端、TODO 任务追踪、联网抓取/搜索。
- 保留 pigo "单进程 goroutine 子 agent" 的既有取舍，编排器仅把多进程隔离作为可选增强。
- 每个 User Story 独立可实现，附带 Go 落地的技术考量（包名 / 接口 / 复用点）。

## User Stories

### US-001: 上下文 token 会计与压缩触发判定
**Description:** 作为 agent loop，我需要在每轮结束后估算当前上下文 token 数并判断是否需要压缩，这样长会话才不会超出模型窗口。

**Acceptance Criteria:**
- [ ] 新增 `internal/compaction` 包，提供 `EstimateTokens(msg agentcore.Message) int`（对标 pi utils.ts 的 estimateTokens，按字符数近似）
- [ ] 提供 `ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool`，逻辑对标 pi：`contextTokens > contextWindow - reserveTokens`
- [ ] `CompactionSettings` 含 `ReserveTokens`（默认 16384）、`KeepRecentTokens` 字段
- [ ] `contextWindow` 取自 `provider.ModelInfo.ContextWindow`；实际 token 数优先用 provider 返回的 `Usage.InputTokens`，缺失时回退到估算
- [ ] 单元测试覆盖阈值边界（等于、略超、远超）
- [ ] `go build ./...` 与 `go test ./internal/compaction/...` 通过

### US-002: 会话切点计算（cut point）
**Description:** 作为压缩流程，我需要找到一个安全的切分点，把旧消息交给摘要、保留最近若干 token 的消息，且不能把工具调用与其结果拆开。

**Acceptance Criteria:**
- [ ] 实现 `FindCutPoint(msgs []agentcore.Message, keepRecentTokens int) CutPointResult`，算法对标 pi `findCutPoint`：从最新往回累加 token，达到 `keepRecentTokens` 后落在最近的合法切点
- [ ] 合法切点定义：user 消息或 assistant 消息，**绝不**切在 toolResult 上（工具结果必须紧跟其 toolCall）
- [ ] `CutPointResult` 含 `FirstKeptIndex`、`TurnStartIndex`、`IsSplitTurn`（对标 pi）
- [ ] 单测覆盖：无可切点、切在 turn 边界、切在 turn 中间（split turn）三种情况
- [ ] `go test` 通过

### US-003: 摘要生成与压缩条目落盘
**Description:** 作为用户，我希望长会话被自动压缩成结构化摘要并持久化，这样重开会话时上下文连续且不超窗口。

**Acceptance Criteria:**
- [ ] 定义 `SUMMARIZATION_SYSTEM_PROMPT` 与结构化摘要模板（Goal / Constraints / Progress / Key Decisions / Next Steps / Critical Context），对标 pi
- [ ] 摘要调用复用现有 provider stream（`provider.Provider`），maxTokens 取 `min(0.8*reserveTokens, model.MaxOutputTokens)`
- [ ] 摘要覆盖从上次压缩点到 cut point 之间的消息，并从 read/write/edit 工具调用中提取 `readFiles` / `modifiedFiles` 列表附于摘要
- [ ] 压缩结果作为一条 `CompactionEntry` 写入 session JSONL；`SessionHeader.Version` 递增并保证旧文件可读（迁移逻辑）
- [ ] 压缩后 loop 用「摘要 + 保留的最近消息」重建 `AgentContext`
- [ ] `go test ./internal/compaction/... ./internal/session/...` 通过

### US-004: `/compact` 手动命令 + 自动压缩接入 loop
**Description:** 作为用户，我希望能手动触发压缩，同时 loop 在超阈值时自动压缩。

**Acceptance Criteria:**
- [ ] `/compact` slash 命令注册（对标 pi），执行后在 REPL 显示压缩前后 token 数与保留消息条数
- [ ] `runtime.StartRun` 每轮结束调用 `ShouldCompact`，为真时自动压缩并在事件流发出 `CompactionEvent`
- [ ] 压缩失败（provider 报错）不中断会话，向用户报告错误并保留原始上下文
- [ ] headless / stream-json 模式下 `CompactionEvent` 也被序列化输出
- [ ] `go test ./internal/runtime/...` 通过

### US-005: 会话树结构（id/parentId）与迁移
**Description:** 作为会话存储，我需要给每条 entry 增加 `id`/`parentId`，形成树结构，这是 fork/clone/tree 的前置。

**Acceptance Criteria:**
- [ ] `session` 包每条持久化 entry 增加 `ID string` 与 `ParentID string`（对标 pi SessionEntryBase）
- [ ] 提供 v→v+1 迁移：旧无 id 文件加载时按顺序补 `id` 并把 `parentId` 指向前一条（对标 pi migrate v1→v2）
- [ ] 提供 `PathToLeaf(entries, leafId) []Entry`：沿 parentId 回溯得到一条线性上下文路径
- [ ] 旧会话文件仍能正常 load 与 resume（回归测试）
- [ ] `go test ./internal/session/...` 通过

### US-006: `/fork` 与 `/clone` 命令
**Description:** 作为用户，我想从历史中某条 user 消息分叉出新对话（fork），或原样复制当前会话到新分支（clone）。

**Acceptance Criteria:**
- [ ] `/fork` 列出历史 user 消息供选择，选定后以其为父创建新 leaf，后续对话挂在新分支
- [ ] `/clone` 在当前位置复制出一条新分支，不影响原分支
- [ ] fork/clone 后 `resume` 走到正确的 leaf 路径
- [ ] 单测覆盖 fork 后两分支互不污染
- [ ] `go test` 通过

### US-007: `/tree` 分支导航
**Description:** 作为用户，我想查看会话的分支树并切换到任意分支继续。

**Acceptance Criteria:**
- [ ] `/tree` 以缩进/连线文本形式打印 entry 树，标出当前 leaf 与各分支点
- [ ] 支持选择某个节点作为新的活动 leaf，之后对话从该节点延续
- [ ] 树渲染在纯行式 REPL 下可读（无需 TUI）
- [ ] `go test` 通过

### US-008: `/export` 与 `/import` 会话
**Description:** 作为用户，我想把会话导出为可分享格式，或从文件导入并 resume。

**Acceptance Criteria:**
- [ ] `/export` 默认导出 JSONL；`/export path.html` 导出自包含 HTML（可读转录，含角色/工具调用高亮）
- [ ] `/import path.jsonl` 读取会话文件并 resume 为新会话
- [ ] 导出的 JSONL 可被 `/import` 无损往返（round-trip 测试）
- [ ] HTML 导出不依赖外部网络资源（样式内联）
- [ ] `go test ./internal/session/...` 通过

### US-009: `/copy` 与 `/session` 命令
**Description:** 作为用户，我想复制上一条 agent 回复到剪贴板，并查看当前会话统计。

**Acceptance Criteria:**
- [ ] `/copy` 把最近一条 assistant 文本写入系统剪贴板（macOS `pbcopy` / Linux `xclip`/`wl-copy`，缺失时降级为打印并提示）
- [ ] `/session` 显示会话名、消息条数、累计 token、模型、创建时间、压缩次数
- [ ] `go test` 通过

### US-010: 图像输入（多模态）
**Description:** 作为用户，我想在 prompt 中引用本地图片路径，让多模态模型看到图片。

**Acceptance Criteria:**
- [ ] `agentcore.Content` 增加 image 块类型（含 base64 数据 + media type），对标 Anthropic/OpenAI 多模态消息格式
- [ ] Anthropic provider 将 image 块编码为 `image` content block；OpenAI provider 编码为 `image_url`（data URI）
- [ ] REPL/headless 支持在输入中用语法（如 `@image:./a.png` 或 `![](path)`）引用本地图片，自动读取并编码
- [ ] 非多模态模型收到图片输入时给出清晰报错而非静默失败
- [ ] 单测覆盖两家 provider 的图片块序列化
- [ ] `go test ./internal/provider/...` 通过
- [ ] （本次 Non-Goal：图像生成）

### US-011: TODO 任务追踪工具
**Description:** 作为 agent，我想维护一个结构化任务清单来编排多步骤工作，用户也能看到进度。

**Acceptance Criteria:**
- [ ] 新增 `todo` 内置工具，注册进 `agenttool` registry，schema 含任务列表（每项 `content`/`status: pending|in_progress|completed`）
- [ ] 工具写入的任务清单存于当前会话状态；REPL 在清单更新时渲染当前进度
- [ ] 系统提示中加入使用 todo 工具的引导（何时该用）
- [ ] 单测覆盖：新增、状态流转、渲染
- [ ] `go test ./internal/agenttool/...` 通过

### US-012: WebFetch 工具
**Description:** 作为 agent，我想抓取一个 URL 的正文并转成 markdown，以便回答需要网页内容的问题。

**Acceptance Criteria:**
- [ ] 新增 `webfetch` 工具：输入 URL + 可选 prompt，输出抓取后的正文（HTML→markdown 简化）
- [ ] HTTP 升级为 HTTPS；跨域重定向返回给调用方而非自动跟随（安全默认，对标 Claude Code）
- [ ] 有超时与响应大小上限，防止拉取超大页面
- [ ] 抓取失败（超时/非 2xx）返回结构化错误而非 panic
- [ ] `go test ./internal/agenttool/...` 通过

### US-013: WebSearch 工具
**Description:** 作为 agent，我想按关键词搜索网络并拿到结果标题与 URL。

**Acceptance Criteria:**
- [ ] 新增 `websearch` 工具：输入 query，可选 allowed/blocked 域名过滤，返回结果条目（标题 + URL）
- [ ] 搜索后端通过配置项指定（API key + endpoint），未配置时工具返回明确的 "未配置搜索后端" 提示而非报错崩溃
- [ ] 单测用 faux HTTP 后端覆盖结果解析
- [ ] `go test ./internal/agenttool/...` 通过

### US-014: MCP 客户端 — 连接与工具发现
**Description:** 作为 pigo，我想作为 MCP 客户端连接外部 MCP server，把其暴露的工具接入 agent 的工具集，兼容 Claude Code 生态。

**Acceptance Criteria:**
- [ ] 新增 `internal/mcp` 包，实现 MCP client 的 stdio transport（子进程 + JSON-RPC 2.0）
- [ ] 支持从配置文件读取 MCP server 列表（命令 + 参数 + env），对标 Claude Code `mcpServers` 配置
- [ ] 连接后执行 `initialize` 握手并 `tools/list` 发现工具
- [ ] 发现的工具以 `mcp__<server>__<tool>` 命名注册进 `agenttool` registry
- [ ] 连接失败的 server 被跳过并告警，不阻塞启动
- [ ] `go test ./internal/mcp/...` 通过

### US-015: MCP 客户端 — 工具调用与资源
**Description:** 作为 agent，我想调用 MCP server 暴露的工具并拿到结果。

**Acceptance Criteria:**
- [ ] MCP 工具被模型调用时，client 发 `tools/call` 并把结果（text/image content）转成 `agentcore` 工具结果
- [ ] 支持 `resources/list` 与 `resources/read`（最小实现），供工具读取 MCP 资源
- [ ] 调用超时/错误被捕获为工具错误结果返回给 loop
- [ ] 进程退出时优雅关闭所有 MCP 子进程
- [ ] `go test ./internal/mcp/...` 通过

### US-016: 插件系统 — 子进程 JSON-RPC 协议
**Description:** 作为业务团队，我想用任意语言写一个可执行文件作为插件，向 pigo 注册自定义工具与命令，无需改动 pigo 源码。

**Acceptance Criteria:**
- [ ] 新增 `internal/plugin` 包，定义插件协议：pigo 以子进程启动插件可执行文件，走 stdio JSON-RPC（复用 US-014 的 JSON-RPC 基础设施）
- [ ] 握手阶段插件声明其提供的 tools（name/description/schema）与 slash commands
- [ ] 插件注册的工具被调用时，pigo 通过 RPC 转发参数、回传结果
- [ ] 插件从配置目录（如 `~/.pigo/plugins/`）自动发现
- [ ] 插件崩溃被隔离：不影响主进程，工具调用返回错误
- [ ] `go test ./internal/plugin/...` 通过

### US-017: 插件系统 — 生命周期事件订阅
**Description:** 作为插件，我想订阅 agent 生命周期事件（run 开始/结束、工具调用前后），实现审计或自定义逻辑。

**Acceptance Criteria:**
- [ ] 插件握手时可声明订阅的事件类型；pigo 在对应时机通过 RPC 通知插件
- [ ] 事件通知为单向（fire-and-forget），插件慢或挂掉不阻塞主 loop（带超时）
- [ ] 复用现有 `agentcore` 事件流作为事件源
- [ ] 单测覆盖事件投递与超时隔离
- [ ] `go test ./internal/plugin/...` 通过

### US-018: 项目信任（Project Trust）
**Description:** 作为用户，我希望首次在某个项目目录运行 pigo 时被询问是否信任该目录，信任决策被持久化，避免在不受信目录自动执行工具。

**Acceptance Criteria:**
- [ ] 新增 `internal/trust` 包，对标 pi trust-manager：信任存储为 `path → bool|null` 的 JSON 文件（`~/.pigo/trust.json`）
- [ ] 提供 `NearestTrustDecision(cwd)`：向上查找最近的已保存信任决策
- [ ] 首次进入未决策目录时，REPL 询问「信任 / 仅本次 / 拒绝」，并可选择保存到该目录或父目录
- [ ] `/trust` 命令保存当前项目信任决策
- [ ] 未信任目录下，具有副作用的工具（bash/write/edit）默认需确认（与现有 permission 机制打通）
- [ ] `go test ./internal/trust/...` 通过

### US-019: 编排器多进程隔离（可选增强）
**Description:** 作为用户，我想在需要强隔离时把子 agent 跑在独立进程里，而默认仍用现有的单进程 goroutine 方案。

**Acceptance Criteria:**
- [ ] `SubAgentSpec` 增加 `Isolation` 选项：`goroutine`（默认，现有行为不变）| `process`
- [ ] `process` 模式下 pigo 以子进程启动自身的 headless/rpc 模式跑子 agent，通过 stdio JSON-RPC（复用 US-014 基础设施）转发 prompt 与回传最终结果
- [ ] 子进程崩溃被父捕获为工具错误，不影响父 loop
- [ ] 默认 `goroutine` 模式下无任何行为变化（回归测试）
- [ ] `go test ./internal/runtime/...` 通过

## Functional Requirements

- FR-1: 系统必须提供上下文 token 估算函数，优先使用 provider 返回的 usage。
- FR-2: 系统必须在上下文超过 `contextWindow - reserveTokens` 时判定需要压缩。
- FR-3: 系统必须计算安全切点，且绝不把 toolCall 与其 toolResult 拆分。
- FR-4: 系统必须用结构化模板生成摘要并作为 CompactionEntry 落盘。
- FR-5: 系统必须提供 `/compact` 手动命令并在 loop 中自动触发压缩。
- FR-6: 系统必须为每条 session entry 维护 id/parentId 树结构，并迁移旧文件。
- FR-7: 系统必须提供 `/fork`、`/clone`、`/tree` 分支操作命令。
- FR-8: 系统必须提供 `/export`（JSONL/HTML）与 `/import`（JSONL）命令。
- FR-9: 系统必须提供 `/copy` 与 `/session` 命令。
- FR-10: 系统必须支持在消息中携带 image 内容块，并按 provider 编码。
- FR-11: 系统必须提供 `todo` 内置工具并渲染任务进度。
- FR-12: 系统必须提供 `webfetch` 工具，默认 HTTPS 且不自动跟随跨域重定向。
- FR-13: 系统必须提供 `websearch` 工具，后端可配置，未配置时给出明确提示。
- FR-14: 系统必须实现 MCP 客户端 stdio transport，发现并注册 MCP 工具。
- FR-15: 系统必须支持调用 MCP 工具并读取 MCP 资源。
- FR-16: 系统必须提供子进程 JSON-RPC 插件协议，支持注册工具与命令。
- FR-17: 系统必须允许插件订阅生命周期事件，且插件故障不阻塞主 loop。
- FR-18: 系统必须持久化项目信任决策并在未信任目录对副作用工具要求确认。
- FR-19: 系统必须支持子 agent 的 `process` 隔离模式作为可选项，默认保持 goroutine。

## Non-Goals (Out of Scope)

- 图像生成（image generation）——本次仅做图像输入。
- 完整移植 pi 的多进程 supervisor/IPC 编排器；仅做可选的 process 隔离。
- 富 TUI（编辑器组件、fuzzy、kill-ring 等）——pigo 已刻意移除（见 `prd-remove-tui.md`），维持行式 REPL。
- `/share`（GitHub gist 分享）、`/scoped-models`、themes 主题系统、telemetry 遥测、`/changelog`、`/reload` 热重载——不在本轮九项内。
- 与 pi 会话文件的 wire 级兼容——pigo 自有 schema（见现有 session.go 说明）。

## Design Considerations

- 纯行式 REPL 前提下，`/tree` 用缩进 + 连线字符渲染，`/session` 用对齐的键值表。
- 图片输入语法沿用 markdown 风格 `![](path)` 或显式 `@image:path`，二选一在 spec 阶段定稿。
- HTML 导出样式内联，参考 pi `export-html`（但不移植其 vendor highlight.js，用轻量方案）。

## Technical Considerations

- **压缩**（#1）：新增 `internal/compaction`，纯函数为主（EstimateTokens / ShouldCompact / FindCutPoint / Summarize），I/O 交给 `session` 与 `runtime`；接入点是 `runtime.StartRun` 每轮末尾。复用 `provider.ModelInfo.ContextWindow` 与 `agentcore.Usage`。
- **会话管理**（#5）：在现有 `internal/session` 上加 id/parentId 与迁移；`SchemaVersion` 已有版本钩子。命令加在 `internal/runtime/slashcommand.go` + `cmd/pigo/repl.go`。
- **多模态**（#6）：扩展 `agentcore.Content`，两家 provider（`internal/provider/anthropic.go`、`openai.go`）分别编码。
- **JSON-RPC 复用**（#2/#7/#8/#19）：MCP client、插件协议、process 子 agent 共用同一套 stdio JSON-RPC 2.0 基础设施，建议先抽 `internal/jsonrpc` 再被三者复用，降低重复。
- **工具**（#3/#4）：全部注册进现有 `internal/agenttool` registry，遵循现有 Tool 接口（Name/Schema/Execute）。
- **信任**（#9）：`internal/trust` 与现有 permission（`grep permission` 命中 2 文件）打通。
- 依赖新增需 pin 版本；HTML 导出、markdown 转换优先用标准库或轻量成熟库。

## Success Metrics

- 长会话（超过模型窗口）可连续对话不报超窗错误，压缩后关键上下文（文件路径/决策）保留。
- 会话可 fork/clone/tree 切换且分支互不污染，导出 JSONL 能无损 import 往返。
- 业务团队用非 Go 语言写的插件可注册并被模型调用，插件崩溃不影响主进程。
- MCP server 工具在 pigo 中可被发现与调用，命名与 Claude Code 一致（`mcp__server__tool`）。
- 未信任目录下副作用工具默认需确认。

## Open Questions

- 压缩的 `KeepRecentTokens` 默认值取多少（pi 未在本次读取的片段中给出常量，spec 阶段定）？
- WebSearch 默认后端选哪个（自建 endpoint / 第三方 API），是否需要在 pigo 内置一个默认？
- 插件协议是否需要版本协商字段以便未来演进？
- `/export` HTML 是否需要支持图片内联（与 #6 多模态联动）？
- process 隔离子 agent 与未来编排器是否要共用同一进程管理层？

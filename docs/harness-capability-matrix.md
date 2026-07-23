# Harness 能力矩阵与对标基线

> 状态：living document（活文档）。本文由 US-001（#247）产出，是 harness engineering 路线图的对标基线。后续每个 US（#248–#253）在落地时应回到本文更新对应行的"现状"与"缺口"状态。
>
> 关联文档：
> - PRD：`tasks/prd-harness-engineering.md`（六个维度的原始盘点与 User Story 定义）
> - pi 源码映射：`tasks/research-pi-agent-loop-go-mapping.md`（TS→Go 控制流与钩子契约）

## 什么是 harness engineering

一个 Agent 的质量不只取决于底层大模型，还取决于包裹在模型外面那层"脚手架"（harness）：上下文如何组织、工具结果如何裁剪、失败如何重试、任务如何被引导（steering）、长会话如何压缩、危险操作如何设护栏。本文中 **harness 层** 指：

- `internal/runtime`——loop（两层循环 + 六钩子）、prompt（系统提示分层）、compaction（自动压缩）；
- `internal/agenttool`——tool executor（三段式执行）与各具体工具；
- `internal/provider/transport`——连接期重试与流式失败模型。

即所有"非模型推理本身"的编排逻辑。

## 对标来源约定

- **pi**：源码精读自 `/Users/chaoyuepan/ai/pi`（映射见 `research-pi-agent-loop-go-mapping.md`）。当无法定位到精确 pi 源码行时，本文描述"被对标的行为"而非杜撰文件路径。
- **Claude Code (CC)**：以其公开可观察的 harness 行为为准（system-reminder 语义、工具输出裁剪、结构化遥测等），不引用非公开实现。

## 能力矩阵总览

| # | 维度 | 现状 | 已验证的现有实现（pigo 源码） | 主要缺口 | 优先级 | US / Issue |
|---|------|------|------------------------------|----------|--------|------------|
| 1 | 上下文管理 | 部分 | 系统提示三层分层；`GetSteeringMessages` / `PrepareNextTurn` / `ShouldStopAfterTurn` / `GetFollowUpMessages` 钩子；阈值自动压缩 | 无通用 `system-reminder` 动态注入机制；AGENTS.md 为静态一次性注入 | **P0** | US-002 → #248 |
| 2 | 工具结果裁剪 | 部分 | `read` 行数上限；`search` 1000 条上限；`webfetch` 字节 + Markdown 上限 | `bash` 只有超时上限、**无输出字节上限**；executor 层无统一工具结果预算 | **P0** | US-003 → #249（bash）<br>US-004 → #250（executor 统一预算） |
| 3 | 韧性与重试 | 部分 | 传输层连接期重试（仅 429/503/529，尊重 Retry-After，绝不重放已消费流） | 无工具执行失败的分类/有限重试 | **P1** | US-006 → #252 |
| 4 | Steering & 中断 | 部分 | `GetSteeringMessages` 每轮工具执行后注入 | steering 触发/来源较薄；无标准化运行时中断—引导 UX | P2 | 记录于本文（见 §Steering），并入 US-002 注入机制协同 |
| 5 | 子 Agent 编排 | 较完整 | `SubAgentTool`、goroutine/process 两种隔离、`RunSubAgentOnce` | 基本满足，本轮不作为重点 | — | 不在本轮范围（PRD Non-Goal） |
| 6 | 可观测性 | 薄弱 | 事件流（10 种 `AgentEvent` + `CompactionEvent`）；headless stream-json 导出；plugin `OnEvent` 钩子 | 无结构化 harness 遥测：轮次数、工具耗时、截断次数、上下文占用率等无统一采集 | **P1** | US-005 → #251 |
| 7 | 安全护栏 | 部分 | trust 三态（目录信任 + 副作用工具确认）；`--approve` | 与 harness 其他机制未打通；无按工具/操作粒度的统一策略入口 | P2 | 记录于本文（见 §安全护栏），协同设计留作后期 US |

> 优先级判据：**P0** = 影响面最大、是长任务上下文溢出/CC 标志能力的直接诱因；**P1** = 支撑鲁棒性度量与错误恢复；**P2** = 次优先级，本文仅记录、协同设计后期展开。

---

## 维度 1：上下文管理

### 现有实现（已验证）

- **系统提示三层分层** —— `internal/runtime/prompt.go` 的 `BuildSystemPrompt`：
  1. base instruction（含 todo 引导），
  2. 环境块（工作目录、`GOOS/GOARCH`、日期），
  3. 从 `Root` 到 `WorkingDir` 的 AGENTS.md 链，general→specific 拼接，
  外加 `AppendInstructions`（`--append-system-prompt`）。
- **每轮钩子** —— `internal/runtime/loop.go`：
  - `GetSteeringMessages`（`loop.go` `afterTurn`，`hadToolExecution` 为真时于每轮工具执行后拉取并注入下一轮之前），
  - `PrepareNextTurn`（返回 `TurnUpdate`，可换 Messages / SystemPrompt / Tools / Model / ThinkingLevel），
  - `ShouldStopAfterTurn`、`GetFollowUpMessages`（外层循环）。
- **自动压缩** —— `loop.go` `maybeAutoCompact` + `internal/compaction/compact.go`：按 `ShouldCompact(tokens, ContextWindow, settings)` 触发，阈值为 `contextWindow - ReserveTokens`（默认 `ReserveTokens = 16384`，见 `internal/compaction/tokens.go`）；压缩失败非致命，发 `CompactionEvent{ErrorMessage}` 后保留原上下文继续。

### 缺口

**无通用 `system-reminder` 动态上下文注入机制。** 当前无法按轮次注入待办状态、文件状态变化、预算告警等"易失上下文"。AGENTS.md 经 `BuildSystemPrompt` 一次性拼进系统提示后即固定，`prompt.go` 不含任何按轮次刷新的注入路径。

- **对标出处**：Claude Code 的 `<system-reminder>` 机制——按轮次注入被明确标注为"背景上下文而非用户指令"的易失消息（todo 列表状态、文件变更提示等），且不污染持久化会话历史。pi 侧对应"每轮工具执行后的 steering/上下文回调"语义（`research-pi-agent-loop-go-mapping.md` §3.2、§4 的 `getSteeringMessages` / `transformContext` 钩子契约）。
- **优先级**：**P0**（影响面最大，是 CC harness 的标志性能力）。
- **落地**：**US-002 → #248**。技术方向：复用 loop.go 已有的 `GetSteeringMessages` / `PrepareNextTurn` 钩子注入，避免新增并行注入路径；reminder 内容须标注为背景上下文，且与 compaction 协同——压缩时可安全丢弃易失 reminder，不被误纳入结构化摘要。

---

## 维度 2：工具结果裁剪

### 现有实现（已验证）

各工具自带的特化上限：

| 工具 | 上限常量（源码） | 值 |
|------|------------------|----|
| `read` | `readToolMaxLines`（`read_tool.go`） | 2000 行；单行 `readToolMaxLineLen` = 2000 字节 |
| `grep`/`find`/`ls` | `searchMaxResults`（`search_tool.go`） | 1000 条匹配/条目 |
| `webfetch` | `webFetchMaxBytes` / `webFetchMaxMarkdown`（`webfetch_tool.go`） | 响应体 5 MiB / 渲染 Markdown 100 KiB |

### 缺口

**缺口 2a：`bash` 工具输出无字节上限。** `internal/agenttool/bash_tool.go` 只有 `bashDefaultTimeout`（2min）与 `bashMaxTimeout`（10min）两个**时间**上限；`streamWriter` 把 stdout/stderr 全量累积进 `combined bytes.Buffer` 并原样作为结果内容返回，**没有任何字节上限或 head/tail 截断**。单条命令的海量输出（如 `cat` 大文件、冗长构建日志）可直接撑爆上下文。

- **对标出处**：pi 的工具输出裁剪——超阈值时保留 head + tail 预览、中间以明确的 `[truncated N bytes]` 标注（被对标行为，参见 `research-pi-agent-loop-go-mapping.md` 对工具结果整形的描述；pi 对流式工具输出统一做尾部保护）。
- **优先级**：**P0**（长任务上下文溢出的直接诱因）。
- **落地**：**US-003 → #249**。方向：新增 `bashMaxOutputBytes` 常量（可经配置层调整），超限时保留首尾预览 + `[truncated N bytes]` 标注，截断须发生在结果进入上下文之前。

**缺口 2b：executor 层无统一工具结果预算。** `internal/agenttool/tool_executor.go` 的 `prepare→execute→finalize` 三段式中，`finalizeToolCall` 只做 `afterToolCall` 字段级覆盖，**不对结果内容施加任何字节预算**。裁剪逻辑散落在各工具自身，任何未自带上限的工具（现在的 `bash`，以及未来新增工具）都能绕过裁剪。

- **对标出处**：pi 的统一结果整形——在执行器/批处理的结果整形阶段施加统一上限，各工具特化上限作为更严格的内层约束（`research-pi-agent-loop-go-mapping.md` §3.4 finalize 阶段 + PRD Technical Considerations "裁剪单点"）。
- **优先级**：**P0**。
- **落地**：**US-004 → #250**。方向：在 `tool_executor.go` 的结果整形阶段实现单一裁剪点，新增可配置的单条工具结果字节预算；`read` 行数、`search` 条数、`webfetch` 字节等特化上限作为更严格的内层约束继续生效。

---

## 维度 3：韧性与重试

### 现有实现（已验证）

- **连接期重试** —— `internal/provider/transport.go` 的 `connect`：仅在 **429 / 503 / 529**（`statusTooManyRequestsCF`）时重试，尊重 `Retry-After`（`retryAfter` 解析秒数或 HTTP-date），默认 `defaultMaxConnectRetries = 2`。网络层错误经 `isRetryableNetErr` 仅对超时类重连。
- **绝不重放已消费的流** —— `NewRequest` 每次连接重新构造请求；一旦 `pump` 开始流式读取，所有运行时失败都编码为终态 `StreamErrorEvent`（双失败模型），不再作为 Go error 返回、更不重放。
- **截断保护** —— `loop.go` `failToolCallsFromTruncatedMessage`：`stopReason == length` 时把该消息所有 tool call 判失败并回喂固定文案，让模型重发。

### 缺口

**无工具执行失败的分类与有限重试。** `tool_executor.go` 的 `runTool` 把工具错误（及 panic）一律转成 `IsError` 结果一次性返回，**不区分瞬态（临时 IO/网络）与终态错误，也不重试**。传输层的连接期重试语义与工具层无关联。

- **对标出处**：pi 的错误恢复——工具执行错误按"瞬态可重试 vs 终态不可重试"分类，可重试类别按有限次数重试（`research-pi-agent-loop-go-mapping.md` §4 钩子契约中"不得抛错"→安全回退语义 + PRD FR-8）。
- **优先级**：**P1**。
- **落地**：**US-006 → #252**。约束：可重试类别按有限次数重试、绝不无限循环；传输层现有连接期重试语义必须保持不变（仍仅 429/503/529、尊重 Retry-After、绝不重放已消费的流，见 PRD FR-9）。

---

## 维度 4：Steering & 中断

### 现有实现（已验证）

- `GetSteeringMessages` 在 `loop.go` `afterTurn` 中，于每轮工具执行后（`hadToolExecution` 为真）拉取并注入下一轮之前——对标 pi 的"agent 干活时插话"的 per-turn steering 语义。
- `GetFollowUpMessages` 在内层循环settled 后（外层循环）拉取——对标 pi 的"排队等 agent 干完再继续"语义。两者走同一注入路径，捞取时机不同。

### 缺口

steering 的触发/来源较薄；无标准化的运行时中断—引导 UX。

- **对标出处**：pi 的 steering/follow-up 双时机注入（`research-pi-agent-loop-go-mapping.md` §3.2）与 Claude Code 运行中可插话/引导的交互模型。
- **优先级**：P2。
- **落地**：本文记录，不单独开 US；与 **US-002（#248）** 的 system-reminder 注入机制协同——两者都经 `GetSteeringMessages` / `PrepareNextTurn` 注入路径，实现时统一考虑，避免注入路径分裂。

---

## 维度 5：子 Agent 编排

### 现有实现（已验证）

`internal/runtime/subagent.go`：`SubAgentTool` 将 `SubAgentSpec` 适配为普通 `AgentTool`；支持 **goroutine**（默认，进程内）与 **process**（`pigo --subagent-rpc` 子进程 + stdio JSON-RPC）两种隔离；`RunSubAgentOnce` 为两者共享的执行核心；子 Agent 作为并行工具可并发运行，失败作为 tool error 回传父循环。

### 状态

基本满足需求。**不在本轮范围**（PRD Non-Goals 明确：不改动 subagent.go）。此处仅记录现状，无缺口条目、无 US 编号。

---

## 维度 6：可观测性

### 现有实现（已验证）

- **事件流** —— `internal/agentcore/event.go`：10 种 `AgentEvent`（`agent_start/end`、`turn_start/end`、`message_start/update/end`、`tool_execution_start/update/end`）+ `CompactionEvent`（含 `TokensBefore` / `TokensAfter` / `SummarizedCount` / `KeptCount` / `ErrorMessage`）。
- **导出途径** —— `internal/runtime/headless.go`：stream-json 模式把每个事件序列化输出（`CompactionEvent` 已导出 `tokensBefore` / `tokensAfter` 字段）；plugin `OnEvent` 钩子观察每个事件。
- **token 计量** —— `agentcore.Usage{InputTokens, OutputTokens}`（`message.go`）挂在 assistant 消息上。

### 缺口

**无结构化 harness 遥测的统一采集。** 现有事件流虽能承载数据，但缺少统一采集：每轮工具耗时、截断发生次数、压缩发生次数（`CompactionEvent` 可数但未聚合）、上下文 token 占用率（`Usage` 已有原料但未换算为占用率）都没有统一的度量点或 summary 导出。没有度量就无法验证 PRD 4C-b 的鲁棒性指标。

- **对标出处**：Claude Code 的结构化遥测/会话度量（轮次、工具耗时、上下文利用率）——公开可观察的 headless 度量行为。pi 侧对应 `AgentEvent` 事件族承载 usage/turn 信息（`research-pi-agent-loop-go-mapping.md` §1.3 事件全集）。
- **优先级**：**P1**（是 4C-b 鲁棒性指标的前提）。
- **落地**：**US-005 → #251**。方向：优先扩展现有 `AgentEvent` 事件族，headless 下经 stream-json 暴露；至少一项指标在 headless 模式下可被脚本读取；不引入外部遥测后端（Prometheus/OTLP 等属 Non-Goal）。

---

## 维度 7：安全护栏

### 现有实现（已验证）

- **trust 三态** —— `internal/trust/manager.go`：`Undecided` / `Trusted` / `Untrusted`，按目录持久化，控制副作用工具（bash/write/edit）是否需确认。
- **`--approve`** —— `cmd/pigo/run.go`：为启动目录授予 session 级信任，跳过首次信任提示与逐次确认（对标 pi 的 `--approve/-a`）。

### 缺口

trust 与 harness 其他机制（executor 裁剪、reminder 注入、遥测）未打通；无按工具/按操作粒度的统一策略入口。当前信任是目录级二元开关，无法表达"某工具需确认、某工具免确认"的细粒度策略。

- **对标出处**：Claude Code 按工具/操作粒度的权限策略入口（allow/deny/confirm 规则）；pi 的 `beforeToolCall` block 钩子作为统一策略挂载点（`research-pi-agent-loop-go-mapping.md` §4）。
- **优先级**：P2。
- **落地**：本文记录，本 PRD 不展开设计（PRD Non-Goal：不改动 trust 既有交互模型）。安全护栏与 harness 协同留作后期 US；若届时范围较大，可能需单独 PRD——此判断作为路线图待决项交由维护者，不属于本文可自行敲定的实现细节。

---

## 核心结论（缺口优先级）

1. **通用动态上下文注入（system-reminder，P0，#248）** —— 影响面最大，CC harness 标志能力。
2. **工具结果裁剪预算（P0，#249 bash 字节上限 + #250 executor 统一预算）** —— 长任务上下文溢出的直接诱因，尤以 bash 输出无字节上限为最。
3. **harness 可观测性（P1，#251）** —— 没有度量就无法验证鲁棒性指标。
4. **韧性重试增强（P1，#252）、steering 强化 / 安全护栏协同（P2，本文记录）** —— 次优先级。

## US / Issue 映射速查

| US | Issue | 维度 | 优先级 |
|----|-------|------|--------|
| US-002 | #248 | 上下文管理（system-reminder） | P0 |
| US-003 | #249 | 工具结果裁剪（bash 字节上限） | P0 |
| US-004 | #250 | 工具结果裁剪（executor 统一预算） | P0 |
| US-005 | #251 | 可观测性（结构化遥测） | P1 |
| US-006 | #252 | 韧性与重试（工具失败分类重试） | P1 |
| US-007 | #253 | 端到端鲁棒性验证场景（长会话压缩 + 大输出截断） | P1 |

> US-007（#253）是跨维度的端到端验证场景，断言压缩与截断机制正确触发、上下文未溢出，用 provider 桩/录制流、不依赖真实外部大模型。它验证维度 1（压缩）与维度 2（截断）的鲁棒性收益，故不单列进上表按维度归类，而在此单独标注。

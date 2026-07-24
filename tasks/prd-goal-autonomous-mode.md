# PRD: `/goal` 自主目标模式（对标 pi-goal / Claude Code goal）

## Introduction

pigo 的 REPL 目前每次只跑「一个 prompt → 助手回复（可含多轮工具调用）→ 回到提示符」的单轮闭环。用户希望像 Claude Code / [pi-goal](https://pi.dev/packages/@narumitw/pi-goal) 那样：给一个高层目标后，让 agent **自主续跑**，直到它认为目标达成（显式调用 `goal_complete`）、遇到真正的僵局（`goal_blocked`），或触发安全阀（最大轮次 / 无进展）或 token 预算上限才停下。

技术路线（已确认）：
- **范围** — 核心自主续跑 **+ token 预算**。不做目标队列、跨扩展 RPC、statusline。
- **持久化** — 目标状态**仅存活于当前 REPL 会话内存**，退出即清空，无需跨进程持久化。
- **无需改动核心 agent loop** — 复用现有接缝：`RunConfig.GetFollowUpMessages`（续跑驱动）、`ReminderProvider`（目标注入）、工具 `Terminate`（结束 run）、`AssistantMessage.Usage.OutputTokens`（token 预算）。

## Goals

- 用户在 REPL 输入 `/goal <objective>` 后，agent 自主朝目标推进，无需每轮手动确认。
- 目标达成由模型显式调用 `goal_complete` 声明；真正僵局由 `goal_blocked` 声明；两者都立即结束 run。
- 安全阀防止失控：最大自动轮次（默认 25）、连续无工具活动轮次（默认 3）、可选 token 预算。
- 目标始终作为 `<system-reminder>` 背景上下文每轮注入，不污染持久化历史。
- 复用 REPL 现有的 SIGINT 取消、会话持久化、Markdown 渲染。
- 目标状态并发安全（工具可能在 batch 中运行，reminder/REPL 每轮读取）。

## User Stories

### US-001: 目标状态与两个目标控制工具
**Description:** 作为使用者，我需要一个会话级目标状态，以及 `goal_complete` / `goal_blocked` 两个工具，让模型能声明目标完成或阻塞。

**Acceptance Criteria:**
- [x] `internal/agenttool/goal_tool.go` 定义 `GoalState`（`sync.RWMutex` 保护）与 `GoalStatus`（idle/active/paused/blocked/complete/budget_limited）
- [x] `GoalState` 方法：`Start`、`Snapshot`、`Clear`、`SetStatus`、`ID`、`RecordIteration(outputTokens, hadToolActivity)`、`MarkComplete`、`MarkBlocked`
- [x] `Clear` 逐字段清零（不整体替换 struct，避免覆盖持锁的 mutex）
- [x] `GoalCompleteTool`：schema 需 `summary`；拒绝空 summary 与"明显矛盾"的 summary（含 `not complete`/`tests still fail`/`未完成` 等英中短语）；成功后 `MarkComplete` 并返回 `Terminate=true`；`ExecutionMode=Sequential`
- [x] `GoalBlockedTool`：schema 需 `reason`+`evidence`；两者非空校验；`MarkBlocked` 并 `Terminate=true`
- [x] 复用 `decodeArgs` / `errorResult`；无效输入降级为 error result（非 Go error），模型可重试

### US-002: 目标提醒每轮注入
**Description:** 作为使用者，我需要目标在自主续跑期间每轮被重新提醒，让模型始终记得目标而不必把目标写进持久化历史。

**Acceptance Criteria:**
- [x] `internal/runtime/reminder.go` 新增 `GoalReminderProvider{State *agenttool.GoalState}`，实现 `ReminderProvider`
- [x] 仅当 `Status==active` 且 objective 非空时注入；paused/blocked/complete/idle 一律静默
- [x] 注入正文含目标 + 持久化指令（"完成后调用 goal_complete，遇僵局调用 goal_blocked，不要停下或询问用户是否继续"）
- [x] 遵循现有 `<system-reminder>` 语义：背景上下文，非用户指令，仅进入本轮请求，不写回持久化历史

### US-003: `/goal` 命令与自主续跑循环
**Description:** 作为使用者，我需要一个 `/goal` 命令来设置/查看/暂停/恢复/清空目标，并驱动自主续跑。

**Acceptance Criteria:**
- [x] `cmd/pigo/goal.go` 实现 `runGoal`，在 `cmd/pigo/repl.go` 主循环里拦截（与 `/compact`、`/fork` 同类，不走 slash Action 闭包），因为要跑 agent stream 且改共享状态
- [x] `replDeps` 增加 `goal *agenttool.GoalState` 字段；`runInteractive` 初始化为空 idle state
- [x] `/goal <objective>`：解析可选 `--tokens 100k/1m/N` 前缀（`parseGoalObjective`/`parseTokenBudget`）；`Start` 后调用 `runGoalLoop`
- [x] `/goal`（裸）：打印状态（objective、status、iterations、tokens used/budget、elapsed、summary/blocked）
- [x] `/goal pause`：标记 paused；`/goal resume`：从 paused/budget_limited 继续 `runGoalLoop`；`/goal clear`：清空
- [x] `runGoalLoop`：组装 run（基础工具 + 两个 goal 工具的临时 registry；goal+todo reminder 合并；`GetFollowUpMessages` 续跑），复用 `streamRun` 结构与 SIGINT 取消（`setCancel` + `context.WithCancel`）
- [x] `/help` 与拦截命令列表登记 `goal`

### US-004: 续跑判定与安全阀
**Description:** 作为使用者，我需要自主续跑在合适时机停下，避免失控或空转。

**Acceptance Criteria:**
- [x] `goalFollowUpDecision(snap) (cont bool, terminal GoalStatus)` 为纯函数，便于单测
- [x] 判定顺序：complete/blocked → 结束（不改状态）；`TokensUsed>=TokenBudget`（预算>0）→ budget_limited 结束；`Iterations>=25` → paused 结束；`NoProgress>=3` → paused 结束；否则续跑
- [x] 每次 settle 用 `goalTurnActivity(tail)` 累计新助手轮的 `Usage.OutputTokens` 与是否有工具活动，喂给 `RecordIteration`
- [x] SIGINT 中断时置 paused 并提示 `/goal resume`
- [x] 结束后按状态打印结果横幅（✓ 完成 / ⚠ 阻塞 / ⏸ 暂停或预算），并 `persistTurn`

## Non-Goals

- 目标队列（多目标排队）。
- 跨扩展 RPC / 子代理编排。
- statusline / 全屏进度渲染。
- 跨进程 / 重启持久化目标状态。

## Design Notes

关键复用点：
- 续跑驱动：`RunConfig.GetFollowUpMessages`（`internal/runtime/loop.go:232`）—— 内层循环 settle 后被调用，返回消息则继续外层循环，返回 nil 则结束 run。
- 目标注入：`ReminderProvider` / `TodoReminderProvider`（`internal/runtime/reminder.go`）。
- run 结束：工具 `Terminate`（`internal/agentcore/tool.go`）+ loop `allTerminate`（`loop.go:219`）。
- token：`AssistantMessage.Usage.OutputTokens`（`internal/agentcore/message.go:61`）。
- 命令拦截模式：`runManualCompact` / `runForkClone`（`cmd/pigo/repl.go`）。
- run 组装：`streamRun`（`cmd/pigo/repl.go`）。
- 工具骨架：`TodoTool` + `TodoStore`（`internal/agenttool/todo_tool.go`）。

安全阀常量：`goalMaxAutomaticTurns=25`、`goalMaxNoProgress=3`（`cmd/pigo/goal.go`）。

## Testing

- [x] `internal/agenttool/goal_tool_test.go`：goal_complete 拒绝空/矛盾 summary、正常 summary 置 complete+Terminate；goal_blocked 记录 reason、要求 evidence；`RecordIteration` 计数；`Clear` 复位。
- [x] `internal/runtime/reminder_test.go`：`GoalReminderProvider` 仅 active 时注入。
- [x] `cmd/pigo/goal_test.go`：`goalFollowUpDecision` 各分支、`parseGoalObjective`/`parseTokenBudget` 解析、`goalTurnActivity` 累计。
- [x] `go build ./...` + `go vet ./...` + `go test ./...` 全绿。
- [ ] 端到端手测：REPL `/goal --tokens 50k 在当前目录创建 hello.txt 并写入 hi` → 观察自主续跑、goal_complete 结束、`/goal` 显示 complete、`/goal clear` 清空。

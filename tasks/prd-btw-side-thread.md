# PRD: `/btw` 旁路侧问命令（Side-Thread Question）

## Introduction

在交互式 REPL 中，用户经常想问一个"临时的、跑题的"问题——比如让 Agent 解释某段代码、确认一个概念、或快速查一件事——但又不希望这些问答污染主对话上下文（主对话应保持任务主线的连贯，避免被无关问答挤占 token、干扰后续压缩与续跑）。

`/btw`（by the way）为此提供一个**临时侧线程（side thread）**：用户可以在不改变主会话消息历史的前提下，向模型提一个或多个旁路问题。侧线程以**当前主会话的上下文作为背景**（模型能看到主对话，从而给出相关回答），但侧线程里的提问与回答**不会写回主会话、也不会落盘**——关闭侧线程、切换会话或重启 pigo 后，这些内容全部丢弃。

本命令对标 Claude Code 的 `/btw`（v2.1.212 起支持无参数重开）与 pi agent 扩展 `@narumitw/pi-btw` 的设计。

**读者提示：** 本文默认读者可能是初级开发者或 AI agent。"主会话上下文（main context）"指 `deps.agentCtx.Messages`——REPL 维护的、会落盘续跑的消息列表；"侧线程"指一份独立的、临时的消息列表，问答只发生在其中。

## Goals

- 让用户在 REPL 中提出旁路问题，且**不向主会话追加任何消息**（不落盘、不参与主会话压缩）。
- 侧线程能**读取主会话上下文**作为背景，使回答与当前任务相关。
- 支持在同一次 `/btw` 调用内进行**多轮追问**，追问间共享侧线程内的历史。
- 支持**无参数重开**：`/btw`（不带问题）重新打开本次 pigo 会话中最近一次侧线程，以便浏览之前的问答；若本会话尚无侧线程，则提示用户输入一个问题。
- 侧线程默认**继承当前会话的 model 与 thinking level**，并允许通过可选配置文件覆盖（仅影响 `/btw`，不影响主会话）。
- `/btw` 结束后，REPL 回到主提示符，主会话状态（消息、token、分支、leaf）与调用前**完全一致**。

## User Stories

### US-001: `/btw <question>` 发起一次旁路提问且不污染主会话
**Description:** As a REPL 用户, I want 输入 `/btw <问题>` 直接向模型提一个旁路问题, so that 我能快速得到答案而不打断主任务、也不污染主对话历史。

**Acceptance Criteria:**
- [ ] 在 REPL 中输入 `/btw 这个函数为什么用指针接收者？` 会进入侧线程并立即把该问题发给模型，流式打印回答。
- [ ] 侧线程用一份主会话消息的**拷贝**作为背景发送给模型；模型回答能引用主对话内容。
- [ ] `/btw` 结束返回主提示符后，`deps.agentCtx.Messages` 的长度与内容与调用前**完全相同**（问题与回答均未追加）。
- [ ] `/btw` 期间与结束后，会话文件（store）内容不变——即 `deps.persisted` 不变，且未触发 `store.Save` 写入侧线程内容。
- [ ] 主会话的 `curLeaf`、`header.UpdatedAt` 不因 `/btw` 改变。
- [ ] Typecheck/lint 通过（`go build ./...` 与 `go vet ./...`）。
- [ ] Verify in a REPL（例如通过 `run` skill 启动 pigo 交互式会话实测）。

### US-002: 侧线程内多轮追问
**Description:** As a 用户, I want 在同一个侧线程里继续追问, so that 我能围绕同一话题深入而无需重复背景。

**Acceptance Criteria:**
- [ ] 进入侧线程后，直接输入文本并回车即可作为追问发送（无需再次输入 `/btw`）。
- [ ] 追问能看到本侧线程内**之前的问答**（侧线程内维护独立的消息累积）。
- [ ] 追问同样以主会话上下文为背景，且同样不写回主会话。
- [ ] 侧线程头部持续显示固定标识（如 `btw · side thread`），提示用户当前处于侧线程。
- [ ] Typecheck/lint 通过。
- [ ] Verify in a REPL。

### US-003: 退出侧线程回到主会话
**Description:** As a 用户, I want 用明确的操作退出侧线程, so that 我能回到主任务继续对话。

**Acceptance Criteria:**
- [ ] 在侧线程中按 `Ctrl+C`：若有正在生成的回答，先取消该回答；再次 `Ctrl+C`（或在空闲时按一次）退出侧线程回到主提示符。
- [ ] 在侧线程空行输入 `/exit`（或约定的退出词）也可退出侧线程。
- [ ] 退出后主提示符恢复，后续普通输入照常进入主会话。
- [ ] 侧线程被丢弃后，其消息不可再从主会话访问。
- [ ] Typecheck/lint 通过。
- [ ] Verify in a REPL。

### US-004: `/btw` 无参数重开最近侧线程
**Description:** As a 用户, I want 输入不带问题的 `/btw` 重开最近一次侧线程, so that 我能浏览本会话此前的旁路问答。

**Acceptance Criteria:**
- [ ] 本次 pigo 进程内**已有**至少一次 `/btw` 侧线程时，输入裸 `/btw` 重新打开最近一次侧线程，展示其历史问答，光标就绪可继续追问。
- [ ] 本次进程内**尚无**任何侧线程时，输入裸 `/btw` 提示用户输入一个问题（等同 US-001 的空线程起手），而非报错。
- [ ] 侧线程历史仅在**当前 pigo 进程/会话**内保留；切换会话、重启 pigo 后清空（不落盘）。
- [ ] Typecheck/lint 通过。
- [ ] Verify in a REPL。

### US-005: `/btw` 独立的 model / thinking 覆盖配置
**Description:** As a 用户, I want 为 `/btw` 单独配置模型与思考等级, so that 我能用更便宜/更快的模型处理旁路问题而不动主会话设置。

**Acceptance Criteria:**
- [ ] 默认无配置时，侧线程继承当前会话的 `live.model`、`live.providerName`、`live.thinkingLevel`。
- [ ] 存在配置文件（路径见 Technical Considerations）且指定了 `model` / `thinkingLevel` 时，侧线程使用覆盖值；未指定的字段回落到当前会话默认。
- [ ] 配置文件缺失、为空 `{}` 或字段缺省时，静默继承默认，不报错。
- [ ] 配置文件在**每次 `/btw` 调用时读取**，编辑后下次 `/btw` 即生效，无需重启。
- [ ] 覆盖的 model 无法解析/鉴权时，打印一行告警并回落到当前会话模型；若连会话模型也不可用，则 `/btw` 报错并中止。
- [ ] 该覆盖**只影响 `/btw`**，主会话的 model/thinking 不变。
- [ ] Typecheck/lint 通过。

### US-006: `/help` 与命令补全中登记 `/btw`
**Description:** As a 用户, I want 在 `/help` 与命令补全中看到 `/btw`, so that 我知道该命令存在及其用法。

**Acceptance Criteria:**
- [ ] `/help` 输出中包含 `/btw` 及一行描述（用法：`/btw [question]`）。
- [ ] REPL 行编辑器的斜杠命令补全能补出 `/btw`。
- [ ] Typecheck/lint 通过。

## Functional Requirements

- FR-1: 系统必须提供 REPL 命令 `/btw`，`/btw <text>` 立即以 `<text>` 作为侧线程首个提问，裸 `/btw` 依据是否已有侧线程分别执行"重开最近侧线程"或"起一个空侧线程并提示输入"。
- FR-2: 系统必须为侧线程构造主会话消息的**只读拷贝**作为背景上下文，并在其后追加侧线程自身的问答；**严禁**将侧线程的任何消息写回 `deps.agentCtx.Messages`。
- FR-3: 系统在 `/btw` 生命周期内**不得**调用主会话的 `store.Save`，不得修改 `deps.persisted`、`deps.curLeaf`、`deps.header.UpdatedAt`。
- FR-4: 系统必须支持侧线程内的多轮追问，追问共享侧线程内累积的消息历史。
- FR-5: 系统必须复用主会话相同的 SIGINT 取消机制（`setCancel`）：运行中 `Ctrl+C` 取消当前回答，空闲/再次 `Ctrl+C` 退出侧线程。
- FR-6: 系统必须在整个 pigo 进程内保留最近一次侧线程的消息，供裸 `/btw` 重开；切换会话或进程退出时丢弃。
- FR-7: 系统必须以侧线程覆盖配置（若存在）决定 model 与 thinkingLevel，否则继承当前会话值；每次调用读取配置。
- FR-8: 系统必须在覆盖模型不可用时告警并回落到会话模型，会话模型亦不可用时报错中止 `/btw`。
- FR-9: 系统必须在侧线程 UI 顶部显示固定标识 `btw · side thread`，并在生成回答时显示 `Answering…` 之类的进行中状态。
- FR-10: 系统必须在 `/help` 列表与斜杠命令补全中登记 `/btw`。
- FR-11: `/btw` 必须在 REPL 主循环中被拦截处理（与 `/compact`、`/goal`、`/fork` 同层），而非通过纯 `Action`/`Expand` 闭包实现——因为它需要驱动 agent 流并访问/隔离主会话上下文。

## Non-Goals (Out of Scope)

- **不**将侧线程问答持久化到磁盘或会话文件（明确的临时性设计）。
- **不**支持跨 pigo 进程/重启后恢复侧线程历史。
- **不**在无头模式（`-p`）下提供 `/btw`——它是交互式 REPL 专属特性。
- **不**引入侧线程专用的工具白名单/沙箱变化（侧线程是否可调用副作用工具，沿用主会话的信任与工具策略；本期不为其单独设计权限模型）。
- **不**实现将侧线程"提升/合并"回主会话的功能。
- **不**为侧线程实现独立的分支树（`/tree`/`/fork` 语义）。

## Design Considerations

- 侧线程 UI 参考 pi-btw：固定头部 `btw · side thread`，问答按时间从上到下滚动展示；页脚在可滚动时显示 `PgUp`/`PgDn` 提示，`Ctrl+C` 提示取消/退出。pigo 当前 REPL 为逐行流式输出（非全屏 TUI），本期可先以**行式简化实现**：进入侧线程后打印头部横幅，逐轮流式打印问答，`Ctrl+C`/`/exit` 退出；`PgUp/PgDn` 滚动浏览为可选增强（见 Open Questions）。
- 复用现有 `streamRun` 的流式渲染与 Markdown 渲染逻辑，但**目标消息列表指向侧线程的临时 `AgentContext`**，而非 `deps.agentCtx`。
- 复用 `renderMarkdown`、`renderToolResult`、`toolCallLabel` 等既有渲染函数，保持输出风格一致。

## Technical Considerations

- **拦截位置：** 在 `cmd/pigo/repl.go` 主循环中，与 `/goal`（`repl.go:262`）同层新增 `if line == "/btw" || strings.HasPrefix(line, "/btw ")` 分支，调用新函数 `runBtw(setCancel, out, &deps, line)`。守卫需避免把 `/btweak` 之类误匹配（用 `== "/btw"` 或前缀 `"/btw "`）。
- **上下文隔离：** 构造 `sideCtx := &agentcore.AgentContext{Messages: append([]agentcore.Message(nil), deps.agentCtx.Messages...)}`（深拷贝切片头，元素为值/接口，注意不要回写）。侧线程的提问/回答只追加到 `sideCtx.Messages`，永不赋回 `deps.agentCtx`。
- **进程级侧线程状态：** 在 `replDeps` 或 REPL 循环局部维护 `lastBtw *agentcore.AgentContext`（最近一次侧线程），供裸 `/btw` 重开；会话切换（`/fork`/`/clone`/`/import`）时清空。
- **运行配置：** 复用 `runtime.RunConfig`/`LoopConfig`（参考 `streamRun` at `repl.go:360`），但 `Model`/`ThinkingLevel`/`Provider` 取自侧线程配置解析结果。
- **配置文件：** 对标 pi-btw 的 `pi-btw.json`，在 pigo 中放置于 pigo 配置目录（如 `$PIGO_DIR/btw.json` 或 `~/.pigo/btw.json`，最终路径以项目现有配置目录约定为准）。字段：`{"model": "<id>", "thinkingLevel": "off|minimal|low|medium|high|xhigh"}`。文件可选、不自动创建、每次调用读取。
- **模型解析：** 复用 `resolveProvider`（见 `interactive.go:457`）解析覆盖 model；失败时告警并回落。
- **取消机制：** 复用 `setCancel` + `context.WithCancel`，与 `streamRun`/`runGoal` 一致，确保 `Ctrl+C` 行为统一。
- **命令登记：** 在 `registerLiveCommands`（`interactive.go:448`）的"仅供 /help 列出"循环中加入 `{"btw", "ask a quick side question without touching the main conversation: /btw [question]"}`；实际逻辑在主循环拦截。

## Success Metrics

- 侧线程一轮问答后，主会话消息数与 token 数零增长（自动化测试断言）。
- `/btw` 全流程不产生任何对会话文件的写入（测试断言 store 未被调用写入）。
- 用户可在侧线程内连续追问 ≥3 轮而主上下文不变。

## Open Questions

- 侧线程是否允许调用副作用工具（bash/write/edit）？若允许，是否沿用主会话信任确认？（本期建议：沿用主会话工具策略，不单独设计。）
- 行式实现 vs 全屏 overlay：pigo 当前无全屏 TUI，`PgUp/PgDn` 滚动浏览是否本期实现，还是留作增强？
- 配置文件的确切路径与文件名需与项目现有配置目录约定对齐（`$PIGO_DIR` 具体环境变量名待确认）。
- 会话切换时清空 `lastBtw`，还是按 sessionID 分别保留？（本期建议：进程内单一 `lastBtw`，切换即清空，最简。）

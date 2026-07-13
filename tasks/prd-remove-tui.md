# PRD: 移除全屏 TUI，简化为无界面交互

## Introduction

当前 pigo 的交互模式建立在 `charm.land/bubbletea/v2` 全屏 TUI 之上（`internal/tui` 包约 3000 行，含 model.go 834 行、state.go 413 行），逻辑复杂：包含流式渲染、transcript 状态机、斜杠命令弹出菜单、模型 picker、两段式 Ctrl+C、markdown 渲染、viewport 滚动等。

本次改动的目标是**删除整个 `internal/tui` 包及其依赖的 bubbletea/bubbles/lipgloss/glamour 等 UI 库**，让代码保持简洁。删除后，`pigo` 无 `-p` 参数、且 stdout 是终端时，不再进入全屏对话界面。

但以下三项能力必须在无 TUI 的前提下继续可用：
1. **斜杠命令 + 技能**：`/model`、`/help`、用户自定义命令、`~/.agents/skills` 的 `/skill-name` 调用。
2. **会话持久化**：`~/.pigo/sessions` 的保存/恢复/列表（`--resume`、`--continue`、`--list-sessions`）。
3. **流式输出**：assistant 回复增量打印，而非一次性输出完整结果。

面向读者可能是初级开发者或 AI agent，下文尽量避免术语堆砌。

## Goals

- 删除 `internal/tui` 整个包（含全部 `*.go` 与 `*_test.go`），移除对 bubbletea/bubbles/lipgloss/glamour/go-runewidth/reflow 等纯 UI 依赖。
- `cmd/pigo` 不再引用 `internal/tui`；`go build ./...` 与 `go mod tidy` 后无残留 UI 依赖。
- 保留一个**极简的无界面交互路径**（基于标准输入输出的行式 REPL）：读一行 → 跑 agent → 流式打印结果 → 循环。
- 在该 REPL 中保留斜杠命令、技能调用、会话持久化、流式输出。
- 代码总行数显著下降，交互逻辑不再依赖任何 TUI 框架。
- 所有保留功能有对应单元测试；删除的 TUI 测试同步移除。

## User Stories

### US-001: 删除 internal/tui 包及其测试
**Description:** As a maintainer, I want the entire `internal/tui` package removed so that the codebase no longer carries the complex full-screen TUI logic.

**Acceptance Criteria:**
- [ ] `internal/tui/` 目录及其下所有 `*.go`、`*_test.go` 文件被删除
- [ ] 代码库中无任何文件 `import "github.com/smallnest/pigo/internal/tui"`（`grep -rn "internal/tui" --include="*.go"` 返回空）
- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 通过

### US-002: 移除纯 UI 第三方依赖
**Description:** As a maintainer, I want the TUI-only third-party dependencies removed from go.mod so that the dependency graph stays minimal.

**Acceptance Criteria:**
- [ ] `go.mod` 中不再直接依赖 `charm.land/bubbletea/v2`、`charm.land/bubbles/v2`、`charm.land/lipgloss/v2`、glamour（若仅被 tui 使用）
- [ ] 运行 `go mod tidy` 后 go.mod / go.sum 无上述条目残留（除非被其他非 UI 代码间接需要）
- [ ] `go build ./...` 通过
- [ ] `go mod verify` 通过

### US-003: 极简行式 REPL 替代全屏交互
**Description:** As a user, I want a simple line-based REPL when I run `pigo` without `-p` on a terminal, so that I can still hold a multi-turn conversation without the full-screen UI.

**Acceptance Criteria:**
- [ ] 无 `-p`、stdout 为终端时，进入行式 REPL：打印提示符 → 读取一行输入 → 运行 agent → 打印回复 → 回到提示符
- [ ] 多轮对话共享同一 `AgentContext`，历史在轮次间累积
- [ ] 输入空行被忽略，继续等待下一行
- [ ] 输入 EOF（Ctrl+D）或 `/exit`（或 `/quit`）干净退出，退出码 0
- [ ] 无 `-p` 且 stdout 非终端（管道/CI）且无 `--resume` 时，报错退出（保持现有行为）
- [ ] Typecheck/lint passes

### US-004: REPL 中的流式输出
**Description:** As a user, I want the assistant's reply to stream to the terminal as it is generated, so that I see progress instead of waiting for the whole response.

**Acceptance Criteria:**
- [ ] agent 运行期间，assistant 文本随生成增量写入 stdout（复用现有 `LoopEventStream` / 事件流）
- [ ] 工具调用与工具结果以简洁的单行文本提示（如 `→ tool: read` / `← result: ...`），不做全屏渲染
- [ ] 一轮结束后光标回到新的提示符行
- [ ] Typecheck/lint passes

### US-005: REPL 中的斜杠命令与技能
**Description:** As a user, I want to type slash commands and invoke skills in the REPL, so that `/model`, `/help`, user commands, and `/skill-name` still work.

**Acceptance Criteria:**
- [ ] 输入以 `/` 开头时，先经 `SlashRegistry.ResolveOutcome` 解析
- [ ] Action 命令（如 `/model`、`/models`、`/help`）执行后打印其返回文本，不发起 agent run
- [ ] Prompt 命令与技能（`/skill-name`）展开为 prompt 文本后，作为本轮用户输入发起 agent run
- [ ] 未知斜杠命令打印一行错误提示（如 `unknown command: /foo`），不发起 run，不崩溃
- [ ] `~/.agents/skills` 下的技能仍通过 `buildSlashRegistry` 加载并可 `/skill-name` 调用
- [ ] Typecheck/lint passes

### US-006: REPL 中的会话持久化
**Description:** As a user, I want my REPL conversation saved and resumable, so that `--resume`、`--continue`、`--list-sessions` continue to work.

**Acceptance Criteria:**
- [ ] 每轮 agent run 结束后，会话消息写入 `~/.pigo/sessions`（或 `$PIGO_HOME/sessions`）
- [ ] `--list-sessions` 列出已存会话（保持现有输出格式）
- [ ] `--resume <id>` 加载指定会话历史并在 REPL 中继续对话
- [ ] `--continue` 恢复最近一次会话
- [ ] 恢复的会话在 REPL 启动时按角色回显既有对话（user / assistant / tool），再接受新输入
- [ ] Typecheck/lint passes

### US-007: 运行中模型切换（文本命令形式）
**Description:** As a user, I want to switch models mid-session via `/model <id>`, so that I keep the ability the TUI picker provided without a full-screen picker.

**Acceptance Criteria:**
- [ ] `/model` 无参数时打印当前模型与 provider
- [ ] `/model <id>` 通过 `resolveProvider` 切换 live 配置，下一轮生效，并打印切换结果
- [ ] `/models [provider]` 打印预置模型目录（复用现有 `presetListing`）
- [ ] 切换后的模型随会话持久化（写入 SessionHeader）
- [ ] Typecheck/lint passes

### US-008: 迁移可复用的纯逻辑测试
**Description:** As a maintainer, I want the still-relevant pure-logic tests preserved, so that slash resolution and session persistence remain covered after the TUI removal.

**Acceptance Criteria:**
- [ ] 删除所有 `internal/tui/*_test.go`
- [ ] 新的行式 REPL 至少覆盖：斜杠命令分流（action 不 run / prompt 发起 run / 未知命令不 run）、EOF 与 `/exit` 退出、多轮历史累积
- [ ] 新增测试通过 `go test ./...`
- [ ] `runtime` 与 `session` 包中既有测试（slash、skills、session）保持通过

## Functional Requirements

- FR-1: 系统必须删除 `internal/tui` 目录下的全部源文件与测试文件。
- FR-2: 系统必须移除 `cmd/pigo/interactive.go` 中对 `internal/tui` 的 import 及 picker/menu 相关装配代码。
- FR-3: 系统必须在 `go mod tidy` 后从 go.mod / go.sum 移除仅被 TUI 使用的第三方依赖。
- FR-4: 无 `-p` 且 stdout 为终端时，系统必须进入行式 REPL 循环。
- FR-5: 无 `-p` 且 stdout 非终端且无 `--resume` 时，系统必须报错并以非零码退出。
- FR-6: REPL 必须在轮次间复用同一个 `AgentContext`，累积对话历史。
- FR-7: REPL 必须以流式方式打印 assistant 回复。
- FR-8: REPL 必须将 `/` 开头的输入经 `SlashRegistry.ResolveOutcome` 解析后分流为 action 或 prompt。
- FR-9: REPL 必须支持 `/skill-name` 调用 `~/.agents/skills` 加载的技能。
- FR-10: REPL 必须在每轮结束后持久化会话到 `~/.pigo/sessions`。
- FR-11: 系统必须保留 `--list-sessions`、`--resume`、`--continue` 的现有行为。
- FR-12: REPL 必须支持 `/exit`（含 `/quit`）与 EOF 干净退出。
- FR-13: REPL 必须支持 `/model`、`/models` 文本命令进行模型查看与切换。

## Non-Goals (Out of Scope)

- 不保留任何全屏 TUI 能力：弹出菜单、模型 picker、viewport 滚动、markdown 高亮渲染、两段式 Ctrl+C 交互、彩色主题、spinner 动画。
- 不保留斜杠命令的**自动补全弹出菜单**（US-022/菜单特性）——REPL 下用户手动输入完整命令名。
- 不引入替代的 TUI 框架（如 tview、tcell 直接编程）。
- 不改变 headless 模式（`-p` / `--output-format text|stream-json`）的行为与接口。
- 不改变 provider 解析、工具集、system prompt 构建逻辑。
- 不改变会话文件的磁盘格式（保持向后兼容，旧会话仍可 `--resume`）。

## Design Considerations

- 新 REPL 建议放在 `cmd/pigo/interactive.go`（原地重写）或新增 `cmd/pigo/repl.go`，不新建包，避免再引入一层抽象。
- 复用现有 `runtime.StartRun` / `LoopEventStream`，通过消费事件流实现流式打印——这是当前 TUI bridge 已用的机制，去掉渲染层即可。
- 复用 `session.Store`、`buildSlashRegistry`、`liveRunConfig`、`resolveProvider`、`presetListing` 等既有构件，删除的仅是 UI 呈现层。
- 提示符与工具提示用纯 ASCII / 简单文本，保证在任意终端与管道下都可读（中文对话内容本身照常输出）。

## Technical Considerations

- `liveRunConfig` 仍可保留用于运行中 `/model` 切换，但不再需要 bubbletea 单 goroutine 约束——REPL 是同步循环，读输入与跑 run 顺序执行。
- 流式打印需处理事件流的增量文本去重（`MessageUpdateEvent` 累积文本 vs `TurnEndEvent` 最终文本），避免重复输出——参考原 `upsertStreamingAssistant` 的语义但简化为直接写差量。
- 移除依赖后需确认 glamour/reflow/go-runewidth 是否被非 TUI 代码引用（如 headless 输出），仅删除确实无引用者。
- 中断处理：REPL 可用一个 `signal.Notify(SIGINT)` 取消当前 run 的 context，回到提示符；无需两段式逻辑。

## Success Metrics

- `internal/tui` 包完全消失，`grep -rn "internal/tui"` 无结果。
- 交互相关代码（含 REPL 与其测试）总行数相比原 tui 包（约 3000 行）下降 70% 以上。
- `go build ./... && go vet ./... && go test ./...` 全绿。
- 无 `-p` 启动仍可进行多轮中文对话、调用技能、切换模型、恢复历史会话。

## Open Questions

- REPL 是否需要在每次提示符处显示当前模型名（如 `pigo(openrouter/free)> `）？倾向于显示，便于确认 `/model` 切换生效。
- 中断（Ctrl+C）在 REPL 空闲时的行为：直接退出，还是打印一次提示再等第二次？倾向于第一次取消 run、空闲时第一次即退出（简化）。
- 工具调用/结果的单行提示格式是否需要可配置（verbose / quiet）？初版固定简洁格式即可。

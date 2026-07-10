# PRD: 拆分 internal/agent 为多个 internal 子 package

## Introduction

`internal/agent` 目前是一个单一的巨型 package——64 个 `.go` 文件、约 12000 行代码，涵盖了从核心消息类型、多厂商 LLM provider、工具实现、sandbox 安全、agent loop 运行时到 sub-agent 编排的**全部职责**。所有代码挤在一个 package 里带来几个问题：

- **认知负担**：新读者面对一个平铺的 64 文件目录，无法从目录结构看出分层。
- **编译/测试粒度粗**：改动任何一个文件都重编译整个 package，测试也无法按层隔离。
- **边界靠约定而非编译器**：例如"工具实现不应反向依赖 loop"这类分层约束目前只靠人工纪律维持，没有 package 边界强制。

本 PRD 的目标是把 `internal/agent` 按职责拆成**若干 `internal/` 下的子 package**，让分层由 Go 的 import 关系强制表达，同时保持功能与测试 100% 不变。

拆分依据一份已完成的依赖分析：当前包内已是一个**干净的有向无环图（DAG）**，不存在双向依赖，因此拆分是可行的、低风险的——关键是把边界画对，别在拆分时引入 import cycle。

**读者假设**：面向初级开发者或 AI agent，涉及的分层概念首次出现时给出解释。

**关键约束**：拆分是**纯结构重构**，不改变任何运行时行为；每个 US 完成后 `go build ./... && go vet ./... && go test ./...` 必须全绿。

## Goals

- 把 `internal/agent` 拆成 **4 个粗粒度子 package**（core / provider / tool / runtime），用 import 关系强制表达分层。
- 保持依赖图为 DAG，**零 import cycle**。
- 功能与测试语义 100% 不变：拆分前后 `go test ./...` 全绿是每个 US 的强制验收条件。
- 测试文件与 faux_provider 测试基架**随被测代码同迁**到对应子 package。
- 外部引用方（`cmd/`、`internal/tui`、`internal/session`）**允许破坏性改动**——直接改成引用新子 package 的符号，不保留 `agent` 门面转发层。

## 目标 Package 结构

依据依赖分析，当前包是如下 DAG：

```
leaves (message, content, event, event_stream, hooks)
   ↑
tool.go (AgentContext, AgentTool)
   ↑                    ↑
Providers            Tools ← Sandbox
   ↑                    ↑
   └──── Loop ──────────┘
           ↑
      Orchestration
```

拆成 4 个粗粒度 package（保持 DAG，箭头表示 import 方向）：

| 子 package | 路径 | 承载内容 | 依赖 |
|-----------|------|---------|------|
| **agentcore** | `internal/agentcore` | 叶子类型 + 基础抽象 + 跨层共享的 hook 函数类型与 helper | 无（叶子） |
| **provider** | `internal/provider` | 多厂商 LLM provider、transport、auth、thinking、model_registry | agentcore |
| **agenttool** | `internal/agenttool` | 工具实现、tool executor、registry、batch executor、sandbox | agentcore |
| **runtime** | `internal/runtime` | agent loop、prompt、config、headless、sub-agent 编排、skills、slash-commands | agentcore, provider, agenttool |

> 粗粒度选择：把原分析中的 sandbox 并入 agenttool（sandbox → tool 单向依赖，职责相关），把 orchestration 并入 runtime（orchestration → loop 单向依赖）。这样 4 个包即可覆盖全部代码且保持 DAG。

## 依赖分析中必须处理的三个陷阱

拆分不是简单按文件分组挪目录，有三处跨组耦合必须先处理，否则会引入 cycle 或编译失败：

1. **`stream_response.go` 归属错位**：它定义 `LoopConfig` 和 `streamAssistantResponse`，语义上属于 **runtime/loop**，不属于 provider。必须随 loop 进入 `runtime` 包。
2. **`emitFunc` 被 tool 层依赖**：unexported 的 `emitFunc` 定义在 `stream_response.go`，但 `tool_executor.go`/`batch_executor.go`（tool 层）依赖它。若 `stream_response.go` 进 runtime，tool 包将无法访问 → 必须把 `emitFunc` **上提到 agentcore**（或在 agentcore 定义等价类型）。
3. **共享 helper 与 hook 函数类型跨层**：
   - `contentToText`（现在 providers.go）、`lastAssistantOf`（现在 headless.go）被 loop / orchestration / provider 多处使用 → 上提到 **agentcore**。
   - hook 函数类型 `BeforeToolCallDecision`/`BeforeToolCallFunc`/`AfterToolCallFunc`/`AfterToolCallResult` 被 tool 与 sandbox 同时引用，`AfterToolCallResult` 已在叶子 hooks.go → 把这组 hook 函数类型统一放在 **agentcore**，让 agenttool 与 runtime 都 import 它。

## User Stories

> 编号规则：US-NNN。每个 US 可独立实现且尽量在一个专注会话内完成。本项目是命令行/库项目，"浏览器验证"不适用，UI 无关；每个 US 的硬性验收是 `go build ./... && go vet ./... && go test ./...` 全绿。拆分建议**自底向上**（先建叶子包，再逐层上移），这样每一步都能编译通过。

### US-001: 建立 agentcore 叶子包
**Description:** 作为开发者，我需要先把"谁都依赖、但不依赖包内其他任何东西"的叶子类型抽成独立的 `internal/agentcore` 包，作为其余所有子包的地基。

**Acceptance Criteria:**
- [ ] 新建 `internal/agentcore` 包，迁入：`message.go`、`content.go`、`event.go`、`event_stream.go`、`tool.go`、`hooks.go` 及其对应 `_test.go`（`types_test.go`、`event_stream_test.go` 等随迁）
- [ ] 把跨层共享的 helper 上提到 agentcore：`contentToText`（原 providers.go）、`lastAssistantOf`（原 headless.go）
- [ ] 把 tool 层依赖的 unexported `emitFunc` 及其相关类型上提到 agentcore（解决陷阱 2）
- [ ] 把 hook 函数类型 `BeforeToolCallDecision`/`BeforeToolCallFunc`/`AfterToolCallFunc`/`AfterToolCallResult` 统一收敛到 agentcore（解决陷阱 3）
- [ ] `internal/agentcore` **不 import** `internal/agent` 或任何其他子包（叶子包，零包内依赖）
- [ ] 此阶段 `internal/agent` 通过 import agentcore + 类型别名/直接引用继续编译通过
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿

### US-002: 抽出 provider 子包
**Description:** 作为开发者，我需要把多厂商 LLM provider 相关代码抽成 `internal/provider` 包，它只依赖 agentcore。

**Acceptance Criteria:**
- [ ] 新建 `internal/provider` 包，迁入：`provider.go`、`provider_interface.go`、`transport.go`、`anthropic.go`、`gemini.go`、`openai.go`、`providers.go`、`auth.go`、`model_registry.go`、`thinking.go` 及各自 `_test.go`
- [ ] **不迁入** `stream_response.go`（它属于 runtime/loop，见陷阱 1）
- [ ] `internal/provider` 只 import `internal/agentcore`，**不 import** tool / runtime
- [ ] `Provider`、`StreamFn`、`StreamFnFromProvider`、`Model`、`NewOpenRouterProvider`、`NewOllamaProvider`、`NewCredentialStore` 等符号从 provider 包正确导出
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿

### US-003: 抽出 agenttool 子包（含 sandbox）
**Description:** 作为开发者，我需要把工具实现、执行器与 sandbox 抽成 `internal/agenttool` 包，它只依赖 agentcore。

**Acceptance Criteria:**
- [ ] 新建 `internal/agenttool` 包，迁入：`bash_tool.go`、`edit_tool.go`、`read_tool.go`、`write_tool.go`、`search_tool.go`、`tool_executor.go`、`registry.go`、`batch_executor.go`、`sandbox.go`、`sandbox_exec.go`、`sandbox_secrets.go` 及各自 `_test.go`
- [ ] `internal/agenttool` 只 import `internal/agentcore`（sandbox → tool → core 单向），**不 import** provider / runtime
- [ ] `emitFunc` 依赖已由 US-001 上提到 agentcore，agenttool 从 agentcore 引用而非本地定义
- [ ] `BashTool`、`EditTool`、`ReadTool`、`WriteTool`、`GrepTool`、`FindTool`、`ToolRegistry`、`NewToolRegistry`、`ToolExecutorConfig`、`BatchConfig` 等符号正确导出
- [ ] 敏感值处理保持不变：sandbox_secrets 的按 key 名引用（不记录明文）行为不回归
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿

### US-004: 抽出 runtime 子包（loop + 编排）
**Description:** 作为开发者，我需要把 agent loop、prompt、config、headless 以及 sub-agent 编排抽成 `internal/runtime` 包，作为依赖 agentcore/provider/agenttool 的顶层。

**Acceptance Criteria:**
- [ ] 新建 `internal/runtime` 包，迁入：`loop.go`、`stream_response.go`、`prompt.go`、`config.go`、`headless.go`、`subagent.go`、`skills.go`、`slashcommand.go` 及各自 `_test.go`
- [ ] faux_provider 测试基架（`faux_provider_test.go`）随 loop/orchestration 测试同迁到 runtime 包（因 `loop_test.go`、`orchestration_test.go`、`stream_response_test.go` 依赖它）
- [ ] `internal/runtime` import `agentcore` + `provider` + `agenttool`，三者均为下游单向依赖，**无反向依赖**
- [ ] `StartRun`、`ContinueRun`、`RunConfig`、`LoopConfig`、`LoopEventStream`、`BuildSystemPrompt`、`PromptConfig`、`RunHeadless`、`HeadlessConfig`、`NewSlashRegistry`、`SlashRegistry`、`LoadUserCommandsDir`、`NewSubAgentTool`、`ParseSkill` 等符号正确导出
- [ ] 原 `internal/agent` 包被完全清空并删除（所有文件已迁出）
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿

### US-005: 更新外部引用方
**Description:** 作为开发者，我需要把 `cmd/`、`internal/tui`、`internal/session` 中对旧 `agent.*` 的引用改成对新子包符号的引用（破坏性改动，不留门面）。

**Acceptance Criteria:**
- [ ] `cmd/pigo/main.go`、`cmd/pigo/interactive.go`、`internal/tui/model.go`、`internal/tui/state.go`、`internal/session/session.go` 中的 `agent.XXX` 引用全部改为对应子包（`agentcore.` / `provider.` / `agenttool.` / `runtime.`）
- [ ] 约 48 个原 `agent.*` 导出符号引用点全部迁移正确，无残留对 `internal/agent` 的 import
- [ ] `internal/tui/state_test.go` 等外部测试的引用同步更新
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿

### US-006: 校验分层与无 cycle
**Description:** 作为开发者，我需要一个可复核的检查，确认拆分后的依赖图是 DAG 且分层正确。

**Acceptance Criteria:**
- [ ] `go build ./...` 成功即证明无 import cycle（Go 编译器会拒绝 cycle）——记录该结论
- [ ] 用 `go list -deps` 或 `go mod graph` 验证：`agentcore` 不依赖任何其他子包；`provider`/`agenttool` 只依赖 `agentcore`；`runtime` 依赖三者
- [ ] `go test ./... -count=1` 全绿，且与拆分前的测试用例集一一对应（无测试丢失）
- [ ] `gofmt -l` 无输出（格式干净）

## Functional Requirements

- FR-1: 系统必须把 `internal/agent` 拆分为 `internal/agentcore`、`internal/provider`、`internal/agenttool`、`internal/runtime` 四个子 package。
- FR-2: `internal/agentcore` 必须不 import 任何其他 pigo 子 package（叶子包）。
- FR-3: `internal/provider` 必须只 import `internal/agentcore`。
- FR-4: `internal/agenttool` 必须只 import `internal/agentcore`。
- FR-5: `internal/runtime` 必须可 import `agentcore`、`provider`、`agenttool`，且这三者必须不反向 import `runtime`。
- FR-6: 系统必须把 `emitFunc` 及 tool 层依赖的相关类型上提到 `agentcore`，避免 `provider → tool` 或 `tool → runtime` 的意外耦合。
- FR-7: 系统必须把 `contentToText`、`lastAssistantOf` 及 hook 函数类型收敛到 `agentcore`。
- FR-8: 系统必须把 `stream_response.go` 归入 `runtime` 而非 `provider`。
- FR-9: 每个 `_test.go` 必须随其被测代码进入对应子 package；faux_provider 测试基架必须迁入 `runtime`。
- FR-10: 系统必须删除清空后的旧 `internal/agent` 目录。
- FR-11: 系统必须更新全部外部引用方（cmd/tui/session）指向新子包，不保留 `agent` 门面转发层。
- FR-12: 拆分完成后 `go build ./... && go vet ./... && go test ./...` 必须全绿，且测试用例集与拆分前一致。

## Non-Goals（不在范围内）

- **不改变任何运行时行为**：这是纯结构重构，不修 bug、不加功能、不改 API 语义。
- **不做中/细粒度拆分**：本次不把 sandbox、orchestration 拆成独立包（合并进 agenttool/runtime）。
- **不保留向后兼容的 `agent` 门面包**：外部引用直接改动。
- **不重命名导出符号本身**：只改 package 归属，`StartRun` 仍叫 `StartRun`，不趁机改名（除非跨包可见性要求 export 原 unexported 符号）。
- **不引入新依赖**：不新增第三方库。
- **不调整 `internal/tui`、`internal/session` 的内部结构**：只改它们对 agent 的引用点。

## Technical Considerations

- **自底向上迁移顺序**：US-001（core）→ US-002/003（provider、tool，可并行）→ US-004（runtime）→ US-005（外部引用）。每步都保持可编译。
- **过渡期编译策略**：US-001~004 之间，旧 `internal/agent` 可临时用类型别名 `type X = agentcore.X` 保持编译，直到 US-004 清空删除。
- **unexported 符号跨包**：迁移中若某 unexported 符号被跨包引用（如 `emitFunc`），需 export 或上提到共享的 agentcore；优先上提到 agentcore 而非散落 export。
- **测试 seam 复用**：`faux_provider_test.go` 是 loop/orchestration 测试的共享 seam，必须与它们同包（runtime）；若 provider 包测试也需要它，则各自保留一份或抽公共 testdata（优先同迁到 runtime，provider 测试用自己的 mock）。
- **命名**：包名避免与标准库/常见库冲突，故用 `agentcore`/`agenttool`（而非 `core`/`tool`）；`provider`/`runtime` 保持简洁。

## Success Metrics

- `internal/agent` 单包从 64 文件降为 0（删除），代码分布到 4 个职责清晰的子包。
- `go build ./...` 通过 = 编译器证明零 import cycle。
- 拆分前后测试用例数量与通过状态一致（无回归、无丢失）。
- 任一子包可独立 `go test ./internal/<pkg>/` 运行。

## Open Questions

- 包命名 `agentcore`/`agenttool`/`provider`/`runtime` 是否符合团队偏好？是否倾向更短的 `core`/`tools`/`llm`/`loop`？
- `faux_provider_test.go` 若 provider 包与 runtime 包都需要，是否值得抽一个 `internal/agenttest` 测试辅助包，还是各自维护 mock？
- 是否需要在拆分后补一个 `internal/agentcore/doc.go` 之类的包级文档，说明分层约束（谁能 import 谁）？
- config.go（`ResolveConfig`/`ConfigLayer`）是否更适合独立成 `internal/config` 而非塞进 runtime？当前归 runtime 是粗粒度选择的结果。

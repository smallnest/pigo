# PRD: Harness Engineering — 审视与补全 pigo 的 Agent 脚手架层

## Introduction

一个 Agent 的质量不只取决于底层大模型，还取决于包裹在模型外面的那层"脚手架"（harness）：上下文如何组织、工具结果如何裁剪、失败如何重试、任务如何被引导（steering）、长会话如何压缩、危险操作如何设护栏。这层脚手架就是 **harness engineering** 关注的对象。

本 PRD 以 harness engineering 的视角审视 `pigo` 当前实现，盘点已具备的能力与缺口，并给出一份**对标 pi / Claude Code**、分阶段落地的路线图。本文档定位为**审计 + 路线图（audit + roadmap）**：它梳理现状、定义目标脚手架层、拆出可实施的 User Story，但不在本轮直接实现——实现由后续 `/to-issues` → 实现流程承接。

术语：本文中 "harness 层" 指 `internal/runtime`（loop、prompt、compaction）+ `internal/agenttool`（tool executor、各工具）+ `internal/provider/transport` 中所有"非模型推理本身"的编排逻辑。

## Goals

- 用 harness engineering 的六个维度，系统盘点 pigo 现有能力与缺口，形成一张可追踪的"能力矩阵"。
- 明确每个缺口的**对标基准**（pi / Claude Code 的对应行为），避免凭空设计。
- 定义一个内聚的"harness 层"目标形态：动态上下文注入、工具结果裁剪预算、韧性重试、steering、可观测性、安全护栏协同工作。
- 把路线图拆成小而独立、可单会话实现的 User Story，每个都带可验证的验收标准与对标出处。
- 为每个机制定义双重成功判据：**行为正确性**（单测证明机制在正确条件下触发）+ **鲁棒性指标**（长会话 / 大输出场景下的溢出与失败率下降）。

## 现状盘点（Harness Capability Matrix）

盘点基于对 `internal/runtime/loop.go`、`prompt.go`、`compact.go`、`internal/agenttool/*`、`internal/provider/transport.go`、`internal/runtime/subagent.go` 的代码审阅。

| 维度 | 现状 | 已有实现 | 缺口 |
|------|------|----------|------|
| **上下文管理** | 部分 | 系统提示分层（base + 环境块 + AGENTS.md 链，prompt.go）；`GetSteeringMessages`/`PrepareNextTurn`/`TurnUpdate` 钩子；自动压缩（compact.go，按 ContextWindow−ReserveTokens 阈值触发） | **无通用 `system-reminder` 动态注入机制**——无法按轮次注入待办、文件状态变化、预算告警等易失上下文；AGENTS.md 是静态一次性注入 |
| **工具结果裁剪** | 部分 | `read` 有行数上限截断；`search` 有 1000 条上限；`webfetch` 有字节 + Markdown 长度上限 | **`bash` 只有超时上限（2min/10min），无输出字节上限**；executor 层无统一工具结果预算/裁剪策略，单个工具巨量输出可直接撑爆上下文 |
| **韧性与重试** | 部分 | 传输层连接期重试（仅 429/503/529，尊重 Retry-After，绝不重放已消费的流） | 无流中断后的恢复；无工具执行失败的分类重试策略；length-stop 有处理但无更广的错误分类 |
| **Steering & 中断** | 部分 | `GetSteeringMessages` 在每轮工具执行后注入 | steering 的触发/来源较薄；无标准化的运行时中断—引导 UX |
| **子 Agent 编排** | 较完整 | `SubAgentTool`、goroutine/process 两种隔离、`RunSubAgentOnce` | 基本满足，本轮不作为重点 |
| **可观测性** | 薄弱 | 事件流（AgentEvent / CompactionEvent）；plugin OnEvent 钩子 | 无结构化 harness 遥测：轮次数、工具耗时、截断次数、压缩次数、上下文占用率等无统一采集 |
| **安全护栏** | 部分 | trust 机制（目录信任 + 副作用工具确认）；`--approve` | 与 harness 其他机制未打通；无按工具/按操作粒度的统一策略入口 |

**核心结论（缺口优先级）：**
1. 通用动态上下文注入（system-reminder）——影响面最大，是 Claude Code harness 的标志性能力。
2. 工具结果裁剪预算（尤其 bash 输出无字节上限）——长任务上下文溢出的直接诱因。
3. harness 可观测性——没有度量就无法验证 4C 的鲁棒性指标。
4. 韧性重试增强、steering 强化、安全护栏协同——次优先级。

## User Stories

> 说明：US 按缺口优先级编号。每个 US 独立可实现，验收标准可观察/可测/可验证，并标注对标出处。UI 相关项为 CLI 行为，验证方式为命令行观测而非浏览器。

### US-001: 定义 harness 能力矩阵与对标基线文档
**Description:** As a maintainer, I want a living capability matrix mapping each harness dimension to pigo's current state and the pi/Claude Code baseline, so that后续每个 US 都有明确的对标依据。

**Acceptance Criteria:**
- [ ] 在 `docs/` 产出 harness 能力矩阵文档，覆盖本 PRD 六个维度
- [ ] 每个缺口条目标注对标出处（pi 的对应文件/行为，或 Claude Code 的公开行为）
- [ ] 每个缺口标注优先级（P0/P1/P2）与对应 US 编号
- [ ] 文档通过 review，无未解释的"待定"条目

### US-002: 引入通用 system-reminder 动态上下文注入机制
**Description:** As the agent runtime, I want a general mechanism to inject ephemeral `<system-reminder>` context per turn, so that待办、文件状态、预算告警等易失信息能在正确的轮次进入上下文，对标 Claude Code 的 system-reminder。

**Acceptance Criteria:**
- [ ] 新增 harness 钩子（复用或扩展 `GetSteeringMessages`/`PrepareNextTurn`），支持注册"reminder 提供者"，按轮次产出 `<system-reminder>` 包裹的消息
- [ ] reminder 内容标注为背景上下文而非用户指令（对标 pi/CC 的语义约定）
- [ ] 至少内置一个 reminder provider（如 todo 列表状态或 CWD 文件变化）作为参考实现
- [ ] reminder 为易失注入：不污染持久化会话历史（或明确标注可被压缩丢弃）
- [ ] 单测证明 reminder 在满足条件时注入、不满足时不注入
- [ ] Typecheck/lint（`go build ./... && go vet ./...`）通过

### US-003: 为 bash 工具增加输出字节上限与 head/tail 预览截断
**Description:** As the agent runtime, I want bash tool output to be capped by byte size with a head/tail preview on overflow, so that单条命令的海量输出不会撑爆上下文，对标 pi 的工具输出裁剪。

**Acceptance Criteria:**
- [ ] `bash` 工具输出超过阈值时截断，保留 head + tail 预览，中间以明确的 `[truncated N bytes]` 标注
- [ ] 阈值有常量定义（如 `bashMaxOutputBytes`），可通过配置层调整
- [ ] 截断发生在结果进入上下文之前
- [ ] 单测覆盖：小输出不截断、大输出截断且保留首尾、标注字节数正确
- [ ] Typecheck/lint 通过

### US-004: 在 tool executor 层引入统一工具结果裁剪预算
**Description:** As the agent runtime, I want a unified tool-result truncation budget at the executor layer, so that所有工具（不止 read/bash）都遵循一致的输出上限策略，对标 pi 的统一结果整形。

**Acceptance Criteria:**
- [ ] executor 层新增可配置的单条工具结果字节预算
- [ ] 超预算时按统一策略截断并标注，各工具自身的特化上限（read 行数、search 条数）作为更严格的内层约束仍生效
- [ ] 裁剪策略有单一实现点，避免逻辑散落到各工具
- [ ] 单测证明预算对任意工具生效
- [ ] Typecheck/lint 通过

### US-005: harness 可观测性——结构化遥测采集
**Description:** As a maintainer, I want structured harness telemetry (turn count, tool durations, truncation/compaction counts, context-utilization ratio), so that能量化验证鲁棒性改进。

**Acceptance Criteria:**
- [ ] 通过现有事件流/新增事件采集：每轮工具耗时、截断发生次数、压缩发生次数、上下文 token 占用率
- [ ] 指标可通过一种途径导出（stream-json 事件字段，或运行结束时的 summary）
- [ ] 至少一项指标在 headless 模式下可被脚本读取
- [ ] 单测证明指标在对应事件发生时被记录
- [ ] Typecheck/lint 通过

### US-006: 韧性增强——工具执行失败的分类与有限重试
**Description:** As the agent runtime, I want tool-execution failures classified and retried under bounded policy, so that瞬态失败（如临时 IO/网络）不会立即终止任务，对标 pi 的错误恢复。

**Acceptance Criteria:**
- [ ] 工具执行错误按类别区分（瞬态可重试 vs 终态不可重试）
- [ ] 可重试类别按有限次数重试，绝不无限循环
- [ ] 传输层现有连接期重试语义保持不变（仍不重放已消费的流）
- [ ] 单测覆盖：瞬态错误触发重试、终态错误不重试、重试次数封顶
- [ ] Typecheck/lint 通过

### US-007: 端到端鲁棒性验证场景
**Description:** As a maintainer, I want end-to-end scenarios exercising long sessions and large tool outputs, so that harness 改进的鲁棒性收益可被复现验证（对应 4C 的鲁棒性指标）。

**Acceptance Criteria:**
- [ ] 新增 e2e/集成测试：长会话触发压缩、大输出触发截断，两类场景各至少一条
- [ ] 场景断言 harness 机制正确触发（压缩事件、截断标注、上下文未溢出）
- [ ] 场景可在 `go test` 中运行，不依赖真实外部大模型（用桩/录制）
- [ ] Typecheck/lint 通过

## Functional Requirements

- FR-1: 系统必须提供一个通用的按轮次动态上下文注入机制，产出 `<system-reminder>` 语义的易失消息。
- FR-2: system-reminder 注入的内容必须被标注为背景上下文，且不得污染持久化会话历史。
- FR-3: 系统必须为 bash 工具输出设定字节上限，超限时以 head/tail 预览 + 截断标注呈现。
- FR-4: 系统必须在 tool executor 层提供统一的单条工具结果字节预算，对所有工具生效。
- FR-5: 各工具自身更严格的特化上限（read 行数、search 条数、webfetch 字节）必须作为内层约束继续生效。
- FR-6: 系统必须采集结构化 harness 遥测：轮次数、工具耗时、截断次数、压缩次数、上下文占用率。
- FR-7: 遥测指标必须至少有一种在 headless 模式下可被脚本读取的导出途径。
- FR-8: 系统必须对工具执行失败区分瞬态/终态，并对瞬态失败按有限次数重试。
- FR-9: 传输层连接期重试策略必须保持不变：仅 429/503/529、尊重 Retry-After、绝不重放已消费的流。
- FR-10: 系统必须提供覆盖"长会话压缩"与"大输出截断"的端到端测试场景。
- FR-11: 能力矩阵文档必须为每个缺口标注对标出处与优先级。

## Non-Goals (Out of Scope)

- 不重写现有两层 Agent 循环（loop.go）的核心结构——只增补钩子与策略。
- 不改动子 Agent 编排（subagent.go）——现状已较完整，本轮不作为重点。
- 不引入外部遥测后端（Prometheus/OTLP 等）——遥测先落在事件流/summary，导出后端后续再议。
- 不改动 trust/安全护栏的既有交互模型——安全护栏与 harness 协同留作 P2，本 PRD 仅记录，不展开设计。
- 不在本轮实现任何代码——本 PRD 是审计 + 路线图，实现由后续流程承接。
- 不改变 `--thinking-level` / 分层配置的既有语义（已于 PR #246 落地）。

## Technical Considerations

- **注入点复用**：system-reminder 应优先复用 loop.go 已有的 `GetSteeringMessages` / `PrepareNextTurn` / `TurnUpdate` 钩子，避免新增并行的注入路径。
- **裁剪单点**：工具结果预算应在 `tool_executor.go` / batch executor 的结果整形阶段实现，各工具特化上限作为内层约束，防止逻辑散落。
- **易失性与压缩的关系**：reminder 注入内容需与 compaction 协同——压缩时应可安全丢弃易失 reminder，不被误纳入结构化摘要。
- **对标来源**：pi 源码（`research-pi-agent-loop-go-mapping.md` 已有映射）与 Claude Code 的公开 harness 行为作为主要对标依据。
- **遥测载体**：优先扩展现有 `AgentEvent` 事件族，headless 下经 stream-json 暴露，避免引入新依赖。
- **测试隔离**：e2e 场景用 provider 桩/录制流，禁止依赖真实外部大模型，保证 CI 可复现。

## Success Metrics

**行为正确性（4C-a）：**
- 每个 harness 机制（reminder 注入、bash 截断、executor 预算、失败重试、遥测记录、压缩触发）都有单测证明其"在正确条件下触发、在不该触发时不触发"。
- `go build ./... && go vet ./... && go test ./...` 全绿。

**鲁棒性指标（4C-b）：**
- 大输出场景：单条 bash/工具输出不再能使上下文单轮溢出（截断生效，注入前即受控）。
- 长会话场景：压缩按阈值可靠触发，会话可持续超过单一上下文窗口而不失败。
- 可量化：截断次数、压缩次数、上下文占用率可从遥测读出，用于回归对比。

## Open Questions

- system-reminder 的内置 provider 首选哪个作为参考实现——todo 列表状态，还是 CWD 文件变化？（US-002 需定）
- 工具结果字节预算的默认阈值取值，是否随模型上下文窗口动态伸缩，还是固定常量 + 配置覆盖？（US-004）
- 遥测导出：本轮是否需要 headless summary 之外的实时途径，还是事件流字段即可？（US-005）
- 安全护栏与 harness 协同（P2）是否需要单独 PRD，还是并入本路线图后期 US？

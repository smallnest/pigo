# 研究摘要：zero（Go 类 agent）的 transport / TUI / sandbox

> Wayfinder ticket #3 的产出。源码精读自本地 `/Users/chaoyuepan/ai/zero`
> （charm.land v2 版 bubbletea/lipgloss/bubbles；原始仓库 https://github.com/Gitlawb/zero）。
> 目的：为 pigo 的 provider transport（US-007~013）、TUI（US-022）、sandbox（US-026）三个决策提供 Go 实现输入。
> 解锁 wayfinder ticket #7（sandbox 强度）与 #11（TUI 选型）。

---

## A. Provider Transport 层（→ US-007~013、Provider 通用 transport 雾区）

### A.1 架构

- **统一接口**：`internal/zeroruntime/types.go:209`
  ```go
  type Provider interface {
      StreamCompletion(ctx, CompletionRequest) (<-chan StreamEvent, error)
  }
  ```
- **共享 transport 包** `internal/providers/providerio/`：一个 SSE scanner + 一个 HTTP client + 统一 retry/auth/错误分类/redaction/看门狗。各 provider 退化为**纯 schema mapper**。
- **各 provider 适配器**：anthropic / openai(chat) / openai/codex(responses) / gemini，只做各自 wire schema 的 JSON 解码，HTTP/SSE 机制全委托 providerio。
- **工厂** `factory.go:39` 按 provider kind 分派。

### A.2 关键设计（值得 pigo 借鉴）

1. **transport/schema 分层**（最干净的部分）：一个 SSE scanner（`scanSSEPayloads` providerio.go:210，基于 `bufio.Scanner`，逐行累积 `data:`、blank line flush、丢 `[DONE]`、`:` 前缀当 keep-alive）；providers 只映射 schema。**pigo 应直接采用这个分层** —— 正好填补地图上「Provider 通用 transport 抽象」雾区。
2. **错误在流内表达（dual failure model）**：pre-flight/marshal 错误走返回值 `error`（同步，channel 活动前）；**连接后的一切失败**（HTTP 非 2xx、坏 JSON、上游错误、idle/stall 超时）编码为终态 `StreamEventError` 事件再关 channel。**这正好印证 pi 的 `StreamFn` 契约**（FR-13：请求失败以流内终态 error/aborted 消息表达而非抛错）—— pi 和 zero 在这点上一致，pigo 的 `Provider` 接口可放心照此设计，且可比 pi 更明确地区分「建流前错误=返回 error」与「建流后错误=流内事件」。
3. **幂等感知的 retry**（retry.go）：只重试 429/503/529，**从不重放已发出的 POST**（completion 非幂等，避免重复计费）；backoff `attempt*400ms`，认 `Retry-After`，capped 30s。401 只刷新重试一次（auth.go）。
4. **双看门狗**（providerio.go:256）：idle watchdog（默认 5m 真静默 → `ErrStreamIdle`）+ content-stall watchdog（心跳在但无真数据 idle×1.2 → `ErrStreamStalled`，针对 gpt-5.x/ollama 心跳但不出内容的挂起）；keep-alive 注释重置计时器；`ZERO_STREAM_IDLE_TIMEOUT` 可调。
5. **agent 层 connect-only 重连**（`internal/agent/reconnect.go`）：只重试**连接**、绝不重放已消费的流（避免重复文本）；`maxStreamReconnects=2`。另有「无可见文本已提交时才整体重发」的 stall retry（loop.go:305）—— 精心 gate 以免重复正文。
6. **StreamEvent 事件模型**：flat struct + `Type` 判别，类型含 `text/reasoning/tool-call-start/delta/end/tool-call-dropped/usage/done/error`。**channel-based**（非 iterator/callback）+ 消费侧可选 callback 层（`CollectStreamWithOptions`）。thinking/reasoning 单独事件、绝不折进正文；`ReasoningBlocks`/signature 保留以便跨 provider replay。

### A.3 与 pi 的对照结论

- **pi 的 StreamFn（不返回 error，失败进流）与 zero 的 dual model 一致**。pigo `Provider` 接口建议：`StreamCompletion(ctx, req) (<-chan StreamEvent, error)`，返回的 error 仅限建流前失败；运行时失败进流内终态事件。这比 §研究1（pi 映射）§3.3 的「StreamFn 不返回 error」更精确，采纳 zero 的 dual 划分。
- **channel-based 流**与 §研究1 的 EventStream（chan）映射方向一致，互相印证。

### A.4 避免 / 重新考虑

- `DisableKeepAlives` 对整个 darwin 平台（providerio.go:171）过于粗暴（每请求全新 TCP+TLS）；pigo 可用 per-conn 健康检查或 HTTP/2 替代。
- `bufio.Scanner` 16MB 行上限（超长单 SSE 行会 `ErrTooLong` 杀流）；用 `bufio.Reader.ReadString('\n')` 避免硬上限。
- 字符串匹配错误分类（`shouldReconnect`、`upstreamFailureMarkers`）脆弱；优先 typed error（`net.Error`/`errors.Is`）。
- 四个适配器的 `stream()` 脚手架大量复制；pigo 可抽成共享 driver + per-provider decoder。
- Gemini 缺 OAuth 401 刷新路径（不一致）。

---

## B. TUI 层（→ US-022；解锁 ticket #11 TUI 选型）

### B.1 架构：单一 monolithic bubbletea Model

- **只有一个 `tea.Model`**（`internal/tui/model.go:60`），Elm 三件套（Init/Update/View）都在这一个类型上。**无嵌套子 Model** —— "子组件" 是普通 state struct 字段（composerState、planPanelState 等），由 `updateModel` 手动分派的 `(m) handleX(msg)` 方法更新。只嵌了 `textinput` 和 `spinner` 两个 bubbles 组件。**没用 `viewport`**，滚动手写。
- 用的是 **charm.land v2** 版（`bubbletea/v2 v2.0.7`、`lipgloss/v2`、`bubbles/v2`）+ `chroma/v2`（高亮）+ `go-udiff`（diff 文本，在 tools 层非 TUI）。

### B.2 事件桥（agent goroutine → bubbletea loop）—— 最值得借鉴

- agent 跑在一个 `tea.Cmd` goroutine 里（`agent.Run` 阻塞调用），**但流式事件不走 Cmd 返回值**。
- 模型注册 agent 回调（OnText/OnReasoning/OnToolCallStart/Delta/OnPermissionRequest/...），回调**同步**被 agent loop 调用，内部把 typed `tea.Msg` 推给 `runtimeMessageSink`。
- `runtimeMessageSink` 在 `Run()` 里接到 `program.Send(msg)` —— **这就是 channel 桥**：`program.Send` 是 bubbletea 线程安全的注入点，把 goroutine 的消息送回单一 Update loop。Cmd 只返回终态结果。
- **对 pigo 的直接意义**：pigo 的 loop 发的 `AgentEvent`（§研究1 §2）在 TUI 模式下，正好通过一个 `func(AgentEvent)` sink → `program.Send` 桥接进 bubbletea。这解决了「loop 的 channel 事件流如何进 TUI 事件循环」这个 US-022 核心问题。

### B.3 流式渲染要点

- **streaming text 不进 transcript rows**：`m.streamingText` 单独存，`View` 里以 "pending interim" item 每帧重渲染；**开着的 ``` 代码块缓冲到闭合 fence 才渲染**（避免逐 token 重新高亮闪烁），按 prefix SHA-256 缓存。
- **tool-call-in-progress**：增量 O(fragment) JSON decoder（`streaming_decoder.go`，避免 O(n²) 重扫）抽出文件路径+尾部内容行，渲染 "writing" 块；结果 card 落地时清掉 live 块。
- **runID 戳 + stale-run guard**：每个 msg 带 runID，`msg.runID != m.activeRunID` 直接丢弃 —— 取消旧 run、启新 run 时防迟到消息污染状态。**关键正确性模式**。
- **屏幕 diff 完全交给 bubbletea**：无手写 cursor/framebuffer diff（grep `\x1b[2J`/cursor-up 全空）。`View` 返回字符串，bubbletea renderer 做增量重绘。代码 +/- diff 是**内容**（`go-udiff` 生成文本，TUI 只做样式），非屏幕 diff。**印证 PRD「不自研差分渲染」可行**。

### B.4 对 pigo 的建议（→ #11 TUI 选型决策的输入）

**借鉴**：单 Model + 平铺 state struct（避免子 Model 转发税）；callback→`program.Send` 桥；runID stale guard；interim 流式块与 committed rows 分离 + 代码块 fence 缓冲；增量 tool-call decoder；两阶段确认（Ctrl+C/Esc）；queue-one-follow-up steering（见 B.5）。

**避免**：177KB 的 model.go god-file（保留单 Model 思路但**拆文件**）；手写滚动 viewport（优先用 stock `bubbles/viewport`，除非要 zero 的逐行选择/hover）；fade/ripple 动画机制（非必要，先上静态 spinner + elapsed clock）；**绝不**手写 unified-diff 文本（用 go-udiff）或屏幕帧 diff（返回字符串让 bubbletea diff）。

**选型倾向**：charm.land/bubbletea v2 生态可用（zero 已验证 v2 可行），但需确认 v2 稳定性/API；保守选择是 `github.com/charmbracelet/bubbletea`（v1，更稳）。这一取舍留给 #11 与你敲定。

### B.5 中断与 steering（与 pi loop 语义的映射）

- **Ctrl+C** 两阶段：非 pending 且 composer 有文本 → 清 composer；否则 cancelRun + 武装退出确认（`tea.Tick` 过期），窗口内二次 Ctrl+C 退出。
- **Esc** 两阶段取消确认。**cancelRun** 调 `context.CancelFunc`，记录部分答案 + "Run cancelled." 标记，阻塞的 permission/askUser goroutine 靠 `ctx.Done()` 解除。
- **steering = queue-next-turn，非 mid-turn 注入**：agent 运行时提交的 prompt 存进 `m.queuedMessage`（单条），当前 run 结束后作为新 run 启动。
  - **与 pi 的差异（重要）**：pi 的 `getSteeringMessages` 是**每轮工具执行后注入到下一轮之前**（agent 还在干活时插话，同一 run 内）；zero 的 queue-next-turn 是**等整个 run 结束才启新 run**。pi 的 steering 更细粒度。pigo 若要严格复刻 pi，TUI 层的 `getSteeringMessages` 应在**当前 run 内每轮后**喂入队列的消息，而非 zero 的「结束后启新 run」。**记录为 pigo 需按 pi 语义实现、不照搬 zero 的点**。

---

## C. Sandbox / 权限 / redaction 层（→ US-026；解锁 ticket #7 sandbox 强度）

### C.1 隔离强度：**两层解耦** —— 进程内策略 + 可选 OS 级隔离

zero 同时有两者，且**刻意解耦**：进程内策略门**永远**跑；OS 隔离 best-effort、可降级。

**Layer A — 进程内策略引擎（always-on）**：`internal/sandbox/engine.go:277` `Evaluate(ctx, Request) Decision`，纯 Go 的 allow/prompt/deny 评估（路径校验、网络分类、破坏性命令检查、grant 查询），任何执行前跑。**这一层就是 pigo v1 要的 `beforeToolCall` 门。**

**Layer B — OS 级隔离（per-platform，`selectPlatformBackend`）**：macOS `sandbox-exec`(Seatbelt/SBPL)；Linux bubblewrap(`bwrap`)；Windows restricted-token + WFP + ACL。Landlock 是**未接线的 stub**（`linux_helper.go:137` 返回 "not implemented"）—— Linux 唯一活的是 bubblewrap。额外 Linux seccomp BPF（AF_UNIX/网络 deny）。

**降级带底线**：OS backend 不可用时降到 `EnforcementDegraded/Disabled`，**但进程内策略门 + 逐命令审批仍生效**。—— **这正是 pigo「OS 隔离作为后续步骤」要的语义：Go 门是地板，OS 隔离是叠加。**

### C.2 策略表达与执行点

- **Policy schema**（`types.go:151`）：`Mode`(disabled/enforce)、`Network`(allow/deny)、`EnforceWorkspace`、细粒度 `AllowRead/DenyRead/AllowWrite/DenyWrite []string`（DenyWrite 最高优先；每条 home 展开+绝对化+符号链接解析）、`BlockUnixSockets`、`MonitorDenials`。默认：enforce + NetworkDeny + EnforceWorkspace。
- **read-all / write-jail 默认姿态**：全盘可读、写限于 workspace + 额外 roots；secrets 靠 `DenyRead` 隐藏；保护元数据目录 `.git/.zero/.agents`。
- **配置安全细节**：额外写 root **只认全局用户配置 + CLI，不认项目配置**（防恶意 repo 给自己放开写权限）。config→policy overlay 只开不关。
- **执行门（两处，都在执行前）**：
  - `internal/tools/registry.go:136` `RunWithOptions` 调 `Sandbox.Evaluate`，`ActionDeny` 硬返回 error result，`ActionPrompt` 未授权返回 "approval required"。**这是权威门**：每个 tool run 都过 registry。
  - `internal/agent/loop.go:860` loop 也调 `Evaluate`（preflight）驱动交互式审批 UX。
- **shell 命令分析用真 AST**（`mvdan.cc/sh/v3/syntax`，`analyzer.go`）非纯正则：走 AST 分类 interactive/destructive/network，解 wrapper 前缀（sudo/env/bash -c）并递归 `sh -c`。正则只在解析失败时兜底。

### C.3 secret redaction：正则 + 已知 key 注册表（两个互补包）

- **`internal/redaction`**（主 scrubber，结构化 + 正则）：~40 个精确敏感 key 名 + 段启发式 + 保守结构规则（避免 `max_tokens`/`public_key` 误伤）；值正则（sk-/sk-ant-api/ghp_/glpat-/AIza/xox./AKIA/JWT）；结构正则（PEM、`"key":"val"`、`KEY=val`、Authorization 头、URL query/userinfo）；`RedactValue` 反射深走 map/struct/slice 带 cycle 检测。
- **`internal/secrets/scanner.go`**（窄而高精度、零依赖，专用于 shell/tool 输出）：`\b`-anchored 正则，longest-match-first 替换为 `[REDACTED:<type>]`。
- **接线点（单一 choke point）**：`registry.go:117` 一个 `defer scrubResultSecrets(result)` 在**每条返回路径**上跑 redaction（Output/Summary/Preview/Meta）。bash 工具额外跑 `secrets.Redact`。
- `internal/securefile`（AES-256-GCM 加密**存储**，仅 credstore 用）—— 是凭据落盘加密，非输出脱敏；pigo 仅在持久化凭据时相关。

### C.4 与 tool 执行路径的集成 = pi 的 beforeToolCall 等价物

- **sandbox 门本身就是 `beforeToolCall` 等价物**（registry.go:136，tool.Run 前调 Evaluate）。
- zero **另有**一套通用 `beforeTool`/`afterTool` 外部 hook 系统（loop.go:1002/1068）—— 用户配置的外部 hook，与 sandbox 门是**两回事**。
- loop 内顺序：filters → pre-permission reject → 命令前缀 grant → **Sandbox.Evaluate preflight** → 交互审批 → **beforeTool hooks** → `registry.RunWithOptions`（再跑 Evaluate 作权威门）→ afterTool hooks → 边界 secret scrub。
- **grants**：可持久化或 session-scoped，scope 按 tool/file/dir/host（`~/.config/zero/sandbox-grants.json`，0600，**明文**）；命令前缀 grant 支持 "always allow git status"。

### C.5 对 pigo 的建议（→ #7 sandbox 强度决策的输入）

**借鉴**：
- **解耦两层架构** —— `Evaluate(ctx, Request) → Decision{Action, Risk, Reason, Block}` 纯函数，就是 pigo v1 的 `beforeToolCall` 门，独立于任何 OS backend 且可独立测试。
- **registry 单 choke-point 执行 + `defer` scrub** —— 不散在各 tool，保证无 tool 能绕过门或漏 secret。
- **tool 声明 `SideEffect` + AST shell 分析器**（`mvdan.cc/sh/v3`）替代纯正则做风险分类。
- **redaction 设计**：已知 key 注册表 + 精度优先正则 + 误伤规避 + longest-match-first；两包分工（结构化 redaction / 窄输出 secrets）。
- **read-all/write-jail 姿态 + 保护 `.git` 等 + 每路径符号链接解析**（否则 symlink 前缀绕过 deny）。
- **grant 模型**（session vs 持久、file/dir/host/tool scope + 命令前缀 grant）。
- **降级带底线**：OS 隔离不可用时保留进程内门 + 逐命令审批，绝不 fail-open。**直接匹配 pigo「OS 隔离作后续」。**
- **配置安全**：额外写 root 只认全局/CLI 不认项目配置。

**避免 / 延后**：
- **不要一上来做四个 OS backend**（Seatbelt SBPL 生成、bwrap 参数、Windows WFP+ACL+restricted-token、WSL 检测都是大维护面）—— PRD 已正确延后，v1 先上进程内门。
- **Landlock 是坑**（zero 自己都 stub 了，且表达不了 DenyRead/DenyWrite）；将来加 Linux 隔离走 bubblewrap，别从 Landlock 起。
- macOS Seatbelt 脆弱（sandbox-exec 已废弃、`cd` 误报 ENOTDIR、denial monitor 某些 OS 版本静默失效）。
- grant store 明文 JSON（grants 不是 secret 无需加密，但凭据持久化要走 securefile 模式）。
- **不要混淆两个 "beforeTool"**：pigo v1 sandbox 门单独就满足 `beforeToolCall`；通用外部 hook 系统是可分离的后续特性。

---

## D. 对下游 ticket / 雾区的影响

- **解锁 ticket #7（sandbox 强度）**：C.5 已给出明确输入。倾向结论：**v1 = 进程内 `Evaluate` 门（接 beforeToolCall）+ redaction 单 choke-point + AST shell 分析；OS 隔离（首选 bubblewrap/Seatbelt）作后续阶段，降级带底线**。待 #7 与你确认。
- **解锁 ticket #11（TUI 选型）**：B.4 已给出输入。倾向：单 Model + callback→program.Send 桥 + runID guard + stock viewport；bubbletea v1 vs charm.land v2 的稳定性取舍留给 #11。
- **graduate 雾区「Provider 通用 transport 抽象」**：A.1/A.2 已足够锐利 —— 可成 ticket（providerio 式共享层：SSE scanner + HTTP client + retry/auth/看门狗 + dual failure model）。
- **新发现的复刻差异**：zero 的 steering 是 queue-next-turn，**与 pi 的 per-turn steering 语义不同**；pigo 须按 pi 实现（B.5）。这条影响 US-022 与 loop（US-006 已复刻 pi 语义，TUI 层对接时勿照搬 zero）。

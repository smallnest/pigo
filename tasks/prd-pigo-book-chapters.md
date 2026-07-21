# PRD:《用 Go 从零构建 Pi Agent》第 2–10 章与后记

## Introduction

《用 Go 从零构建 Pi Agent》以 [pigo](https://github.com/smallnest/pigo) 源码为标本，庖丁解牛式逐层拆解一个命令行 AI Agent。引言（#202）与第 1 章（#204）已完成并入库，构建管线（pandoc → tectonic → ElegantBook）已跑通。

本 PRD 覆盖**剩余全部正文写作单元**：第 2–10 章共 9 章正文 + 后记，合计 10 个独立可交付单元。每章锚定 `outline.md` 中列出的真实源码包与关键文件，读者可对照源码逐行印证。写作规范、图表/实验盒子/交叉引用约定沿用第 1 章既定模式（见 `book/README.md`「写作约定」）。

## Goals

- 补全全书 9 章正文 + 后记，与引言、第 1 章合为完整书稿。
- 每章严格锚定 `outline.md` 指定的源码包与关键文件，杜绝架空描述。
- 每章产出的 `chapterN.md` 能被 `build_pdf.sh` 无错误纳入构建，章节/小节编号与图表编号正确。
- 全书行文风格、术语、交叉引用与第 1 章保持一致。

## User Stories

### US-001: 撰写第 2 章《agentcore 抽象》
**Description:** 作为读者，我想理解贯穿全局的核心类型契约（消息、内容、事件、工具、钩子），以便看懂后续各章如何复用这些抽象。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter2.md`，锚定 `internal/agentcore/{message,content,event,event_stream,tool,hooks,helpers}.go`
- [ ] 覆盖：Message/Content 建模与多模态表达；AgentEvent 密封接口与 10 种事件类型；Tool 抽象与 Hooks 契约
- [ ] 一级标题无手写「第 N 章」编号，小节无手写「N.N」编号（编号交由 `--number-sections`）
- [ ] 代码引用来自 pigo 真实源码，关键结构体/接口签名与仓库一致
- [ ] 加入 `book/build_pdf.sh` 的 CANDIDATES 后 `bash build_pdf.sh` 成功产出 PDF，无 LaTeX 报错

### US-002: 撰写第 3 章《两层 Agent 循环》
**Description:** 作为读者，我想理解内层「流式回复→执行工具→回填」与外层「后续消息」如何驱动一次 run。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter3.md`，锚定 `internal/runtime/{loop,stream_response,prompt,render}.go`
- [ ] 覆盖：内层循环收尾条件；外层 `getFollowUpMessages` 与 steering 续跑；六个钩子与停止原因（length/error/aborted）
- [ ] 标题无手写编号；交叉引用「第N章」使用既定 `第N章` 写法以触发 crossref 链接
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-003: 撰写第 4 章《Provider 层与传输》
**Description:** 作为读者，我想理解统一 Provider 接口下 OpenAI/Anthropic 两套协议、SSE 传输与鉴权的实现。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter4.md`，锚定 `internal/provider/{provider,provider_interface,openai,anthropic,transport,registry,presets,auth,special_auth,providers}.go`
- [ ] 覆盖：两套协议解码；共享传输驱动（HTTP+SSE 行解析+重试+双看门狗+双失败模型）；注册表/精选模型目录/特殊鉴权
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-004: 撰写第 5 章《工具系统》
**Description:** 作为读者，我想理解内置工具的注册、批量执行与各类能力实现。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter5.md`，锚定 `internal/agenttool/{registry,tool_executor,batch_executor,read_tool,write_tool,edit_tool,search_tool,bash_tool,webfetch_tool,htmlmarkdown,todo_tool}.go`
- [ ] 覆盖：工具注册表与执行器（单个 vs 批量并发）；文件读写/编辑、搜索、Bash、网络抓取（HTML→Markdown）、待办工具要点
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-005: 撰写第 6 章《上下文压缩》
**Description:** 作为读者，我想理解逼近 token 上限时如何选切点、做摘要并压缩上下文。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter6.md`，锚定 `internal/compaction/{compact,cutpoint,summary,tokens}.go` 与 `internal/runtime/compaction_test.go`
- [ ] 覆盖：token 计数与上限判定；切点选择策略与摘要生成；压缩如何嵌入循环并保住关键上下文
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-006: 撰写第 7 章《会话持久化》
**Description:** 作为读者，我想理解会话的存取、导出与 HTML 回放。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter7.md`，锚定 `internal/session/{session,export,export_html}.go`
- [ ] 覆盖：会话数据模型、保存与加载；导出能力与 HTML 回放实现
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-007: 撰写第 8 章《项目信任与安全》
**Description:** 作为读者，我想理解首次进入目录的信任决策与受信状态管理如何作为工具执行前的安全闸门。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter8.md`，锚定 `internal/trust/manager.go` 与 `cmd/pigo/trust.go`
- [ ] 覆盖：首次进入项目目录的信任决策流程；受信状态持久化与 CLI 呈现；作为 Bash/工具执行前安全闸门的作用
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-008: 撰写第 9 章《子 Agent 编排》
**Description:** 作为读者，我想理解进程隔离的子 Agent 与基于 JSON-RPC 的父子通信。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter9.md`，锚定 `internal/runtime/subagent.go`、`cmd/pigo/subagent_rpc.go`、`internal/jsonrpc/{message,transport}.go`
- [ ] 覆盖：`pigo --subagent-rpc` 子进程侧；JSON-RPC 2.0 over stdio 的 `subagent/run` 请求与结果回传；父侧 SubAgentTool 驱动与事件汇聚
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-009: 撰写第 10 章《可扩展性生态（Skills / Plugins / 包管理）》
**Description:** 作为读者，我想理解 Skills、斜杠命令、Plugins 与包管理器如何让 Agent 可插拔生长。

**Acceptance Criteria:**
- [ ] 新建 `book/chapter10.md`，锚定 `internal/runtime/{skills,slashcommand}.go`、`internal/plugin/{plugin,manager,manifest,events}.go`、`internal/pkgmgr/{install,fetch,distribute,classify,lockfile,ref}.go`、`cmd/pigo/pkgcmd.go`
- [ ] 覆盖：Skills 与斜杠命令挂载；Plugin 清单/管理器/事件系统；包管理器拉取/分类/分发/锁文件
- [ ] 标题无手写编号
- [ ] 代码引用与仓库一致
- [ ] `bash build_pdf.sh` 成功产出 PDF，无报错

### US-010: 撰写后记《解牛之后》
**Description:** 作为读者，我想在读完源码解剖后，理解 pigo 相对 pi 的设计取舍与可延伸方向。

**Acceptance Criteria:**
- [ ] 新建 `book/afterword.md`，标题以 `{.unnumbered}` 标记（后记不参与章节编号，比照引言）
- [ ] 覆盖：从阅读到重造的设计取舍与工程权衡；可延伸方向与读者练习
- [ ] `book/build_pdf.sh` 已含 `afterword.md` 候选项，`bash build_pdf.sh` 成功产出 PDF，无报错

## Functional Requirements

- FR-1: 系统必须为第 2–10 章分别新增 `book/chapterN.md`（N=2..10）。
- FR-2: 系统必须新增 `book/afterword.md` 作为后记，标题以 `{.unnumbered}` 标记。
- FR-3: 每章正文标题必须不含手写「第 N 章」/「N.N」编号，编号交由 pandoc `--number-sections` 与文档类生成。
- FR-4: 每章内容必须锚定 `outline.md` 指定的对应源码包与关键文件，代码引用与 pigo 仓库真实源码一致。
- FR-5: 图表标题若出现必须写作「图N-M」形式以触发 `crossref.lua` 打锚点；正文「图N-M」「第N章」引用沿用既定写法。
- FR-6: 每章文件加入构建后，`bash build_pdf.sh` 必须无 LaTeX 报错地产出完整 PDF。
- FR-7: 全书行文风格、术语与交叉引用必须与已完成的引言、第 1 章保持一致。

## Non-Goals

- 本轮不新增配图/SVG（图表 issue 后续单独规划）。
- 不修改构建管线、preamble、cover 等基础设施（`chapterN.md` 已在 CANDIDATES 中，无需改脚本）。
- 不重写已完成的引言与第 1 章。
- 不做英文版或其它语言翻译。

## Technical Considerations

- 各 `chapterN.md` 已在 `build_pdf.sh` 的 CANDIDATES 数组中，存在即被编译，无需改脚本。
- 写作约定见 `book/README.md`「写作约定」：章节编号自动生成、图表「图N-M」打锚点、`::: experiment` / 「实验 N-M」渲染为盒子。
- 代码高亮为 tango 风格（浅色代码框），代码块标注语言（```go / ```bash）即可着色。
- 每章建议独立成一个 agent 会话完成，规模可控。

## Success Metrics

- 10 个写作单元全部合入后，全书 PDF 含引言 + 10 章正文 + 后记，章节/小节/图表编号连续正确。
- 每章关键源码引用可在 pigo 仓库中逐一对照到真实文件与符号。
- 构建零报错。

## Experiments (可选配套)

「实验 N-M」动手环节**不强求**：由各章作者判断，若该章内容适合动手验证（如第 1 章的 实验 1-1），则酌情加入一个，以「实验 N-M」开头的标题或 `::: experiment` 围栏 Div 触发 `experiment_box.lua` 盒子渲染；不适合则省略。

## Open Questions

- 后记是否需要「延伸阅读/参考文献」小节并启用 `reference.bib`？

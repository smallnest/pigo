# 《Write Pi Agent in Go》全书大纲

本书以 [pigo](https://github.com/smallnest/pigo)（pi 命令行 AI Agent 的 Go 重实现）源码为标本，
按「从入口向内、再向外」的解剖顺序，逐层拆解一个可用于生产的命令行 AI Agent 是如何装配、运行与扩展的：
先从 **CLI 入口** 进入，深入 **核心抽象 → 两层循环 → Provider 传输 → 工具系统**，
再向内触及 **上下文压缩 → 会话持久化 → 项目信任**，最后向外延伸到 **子 Agent 编排 → 可扩展性生态**。

全书含 **引言**、一篇贯穿全书的 **主线导读**、**10 章正文** 与 **后记**。每章都锚定 pigo 仓库中真实存在的包与关键文件，读者可对照源码逐行印证。

## 内容速览

| 章号 | 主题 | 一句话核心 | 对应源码包 |
| --- | --- | --- | --- |
| 引言 | 为什么解剖 pigo | 从 pi 到 pigo：一只可拆解的命令行 Agent 标本 | — |
| 主线导读 | 一条命令的生命周期 | 追一条命令从回车到结果，看清十个包如何一站站接力（不编号，横贯全书） | 全包综述 |
| 第 1 章 | CLI 装配与运行架构 | 命令行入口如何解析参数、装配 Provider/工具并选择运行模式 | `cmd/pigo`、`internal/runtime`（config/headless） |
| 第 2 章 | agentcore 抽象 | 消息、内容、事件、工具、钩子等贯穿全局的核心类型契约 | `internal/agentcore` |
| 第 3 章 | 两层 Agent 循环 | 内层「流式回复→执行工具→回填」与外层「后续消息」如何驱动一次 run | `internal/runtime`（loop/stream_response/prompt） |
| 第 4 章 | Provider 层与传输 | 统一 Provider 接口之下的 OpenAI/Anthropic 协议、SSE 传输与鉴权 | `internal/provider` |
| 第 5 章 | 工具系统 | 内置工具的注册、批量执行与读写/搜索/网络等能力实现 | `internal/agenttool` |
| 第 6 章 | 上下文压缩 | 逼近 token 上限时如何选切点、做摘要并压缩上下文 | `internal/compaction`、`internal/runtime`（compaction） |
| 第 7 章 | 会话持久化 | 会话的存取、导出与 HTML 回放 | `internal/session` |
| 第 8 章 | 项目信任与安全 | 首次进入目录的信任决策与受信状态管理 | `internal/trust`、`cmd/pigo`（trust） |
| 第 9 章 | 子 Agent 编排 | 进程隔离的子 Agent 与基于 JSON-RPC 的父子通信 | `internal/runtime`（subagent）、`internal/jsonrpc`、`cmd/pigo`（subagent_rpc） |
| 第 10 章 | 可扩展性生态 | Skills / 斜杠命令 / Plugins 与包管理器如何让 Agent 可插拔生长 | `internal/plugin`、`internal/pkgmgr`、`internal/runtime`（skills/slashcommand）、`cmd/pigo`（pkgcmd） |
| 后记 | 解牛之后 | 从阅读源码到重造轮子：设计取舍与延伸方向 | — |

## 各章详解

### 引言：为什么解剖 pigo

- pi 与 pigo 的关系：命令行 AI Agent 的问题域与 Go 重实现的动机。
- 本书的解剖路线「从入口向内、再向外」与阅读方法（对照源码、动手实验）。
- 环境与仓库速览：`cmd/pigo`（入口）与 `internal/*`（核心）的整体地图。

### 主线导读：一条命令的生命周期（不编号）

- 定位：夹在引言与第 1 章之间的一篇导读，不参与章节编号，是一条把后面十章串起来的红线。
- 追一条真实命令 `pigo "读一下 main.go…"`，从敲下回车到打出结果，拆成 12 个交接点。
- 每个交接点标出「哪个包在动、做了什么、把接力棒交给谁」，并标注对应章号，读者随时能在全书地图上定位自己。
- 对应文件：`book/reading-guide.md`。

### 第 1 章 CLI 装配与运行架构

- 关键文件：`cmd/pigo/main.go`、`cmd/pigo/run.go`、`cmd/pigo/repl.go`、`cmd/pigo/interactive.go`、
  `cmd/pigo/baseurl.go`、`cmd/pigo/color.go`、`internal/runtime/config.go`、`internal/runtime/headless.go`。
- 参数解析（pflag）、`--model`/Provider 解析、API Key 环境变量注入。
- 三种运行模式：headless print 模式、`stream-json` 事件模式、交互式 REPL。
- Provider 与工具注册表在启动期的装配顺序。

### 第 2 章 agentcore 抽象

- 关键文件：`internal/agentcore/message.go`、`content.go`、`event.go`、`event_stream.go`、
  `tool.go`、`hooks.go`、`helpers.go`。
- Message / Content 的建模与多模态内容表达。
- AgentEvent 密封接口与 10 种事件类型（对应 pi 的事件全集）。
- Tool 抽象与 Hooks 契约：贯穿循环各阶段的扩展点。

### 第 3 章 两层 Agent 循环

- 关键文件：`internal/runtime/loop.go`、`internal/runtime/stream_response.go`、`internal/runtime/prompt.go`、
  `internal/runtime/render.go`。
- 内层循环：流式回复 → 批量执行工具调用 → 回填结果，直到一轮无工具调用而自然收尾。
- 外层循环：`getFollowUpMessages` 与 steering 消息如何续跑。
- 六个钩子（prepareNextTurn / shouldStopAfterTurn 等）与停止原因（length/error/aborted）的处理。

### 第 4 章 Provider 层与传输

- 关键文件：`internal/provider/provider.go`、`provider_interface.go`、`openai.go`、`anthropic.go`、
  `transport.go`、`registry.go`、`presets.go`、`auth.go`、`special_auth.go`、`providers.go`。
- 统一 Provider 接口下的 OpenAI-compatible 与 Anthropic-Messages 两套协议解码。
- 共享传输驱动：HTTP + SSE 行解析 + 重试 + 双看门狗 + 双失败模型（流内 StreamErrorEvent 与构建期错误）。
- Provider 注册表、精选模型目录与鉴权（含特殊鉴权 provider）。

### 第 5 章 工具系统

- 关键文件：`internal/agenttool/registry.go`、`tool_executor.go`、`batch_executor.go`、
  `read_tool.go`、`write_tool.go`、`edit_tool.go`、`search_tool.go`、`bash_tool.go`、
  `webfetch_tool.go`、`htmlmarkdown.go`、`todo_tool.go`。
- 工具注册表与执行器：单个执行与批量并发执行。
- 文件读写/编辑、搜索、Bash、网络抓取（含 HTML→Markdown）、待办工具的实现要点。

### 第 6 章 上下文压缩

- 关键文件：`internal/compaction/compact.go`、`cutpoint.go`、`summary.go`、`tokens.go`、
  `internal/runtime/compaction_test.go`。
- token 计数与上限逼近的判定。
- 切点（cutpoint）选择策略与摘要（summary）生成。
- 压缩如何嵌入循环，保住关键上下文的同时释放窗口。

### 第 7 章 会话持久化

- 关键文件：`internal/session/session.go`、`export.go`、`export_html.go`。
- 会话的数据模型、保存与加载。
- 导出能力与 HTML 回放的实现。

### 第 8 章 项目信任与安全

- 关键文件：`internal/trust/manager.go`、`cmd/pigo/trust.go`。
- 首次进入项目目录的信任决策流程。
- 受信状态的持久化与在 CLI 中的呈现，作为执行 Bash/工具前的安全闸门。

### 第 9 章 子 Agent 编排

- 关键文件：`internal/runtime/subagent.go`、`cmd/pigo/subagent_rpc.go`、
  `internal/jsonrpc/message.go`、`internal/jsonrpc/transport.go`。
- 进程隔离的子 Agent：`pigo --subagent-rpc` 子进程侧。
- 基于 JSON-RPC 2.0 over stdio 的父子通信：`subagent/run` 请求与结果回传。
- 父侧 SubAgentTool 如何驱动子进程并汇聚事件。

### 第 10 章 可扩展性生态（Skills / Plugins / 包管理）

- 关键文件：`internal/runtime/skills.go`、`internal/runtime/slashcommand.go`、
  `internal/plugin/plugin.go`、`manager.go`、`manifest.go`、`events.go`、
  `internal/pkgmgr/install.go`、`fetch.go`、`distribute.go`、`classify.go`、`lockfile.go`、`ref.go`、
  `cmd/pigo/pkgcmd.go`（Skills/包相关子命令散见于 `cmd/pigo/run.go`、`repl.go`、`interactive.go`）。
- Skills 与斜杠命令：把领域能力挂载进 Agent。
- Plugin 清单、管理器与事件系统。
- 包管理器：拉取、分类、分发与锁文件，让 Skills/Prompts/Themes 可安装可复用。

### 后记：解牛之后

- 从阅读到重造：pigo 相对 pi 的设计取舍与工程权衡。
- 可延伸的方向与练习：读者如何在此标本上继续生长自己的 Agent。

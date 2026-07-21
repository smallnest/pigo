# 《庖丁解牛 pigo：用 Go 从零构建命令行 AI Agent》

本目录存放技术书《庖丁解牛 pigo》的书稿源文件与 PDF 构建管线。
管线仿照参考书 [ai-agent-book](https://github.com/bojieli/ai-agent-book)：
Markdown 章节 → pandoc → xelatex → ElegantBook 文档类（cyan 主题）。

## 目录结构

```
book/
├── build_pdf.sh        # 一键构建脚本（pandoc + xelatex + ElegantBook）
├── preamble.tex        # ElegantBook cyan 主题定制 + 中文字体回退
├── cover.tex           # 封面（书名/副标题/作者）
├── crossref.lua        # pandoc 过滤器：图表编号与交叉引用链接
├── experiment_box.lua  # pandoc 过滤器：「实验 N-M」渲染为带框盒子
├── images/             # 图片资源目录
├── introduction.md     # 引言（占位，待 #202/#203/#204 替换）
└── chapter*.md         # 各章正文（由后续节点补全）
```

源文件进、产物出：生成的 `*.pdf` 不纳入版本管理（见根目录 `.gitignore`）。

## 依赖

| 依赖 | 说明 | 安装 |
| --- | --- | --- |
| pandoc | Markdown → LaTeX 转换 | `brew install pandoc` / `apt-get install pandoc` |
| xelatex | PDF 引擎（TeX Live 或 MiKTeX） | TeX Live，或本机 MiKTeX（`~/miktex/bin/xelatex`，脚本自动探测） |
| ElegantBook | 文档类 `elegantbook.cls` | 随 TeX 发行版安装，或 MiKTeX 按需自动拉取 |
| rsvg-convert | librsvg，嵌入 SVG 图（可选） | `brew install librsvg` / `apt-get install librsvg2-bin` |
| 中文字体 | macOS：Songti SC / Heiti SC；Linux：Noto Serif/Sans CJK SC | 系统自带或 `fonts-noto-cjk` |

`build_pdf.sh` 会在缺失 `pandoc` 或 `xelatex` 时给出明确提示并退出；缺 `rsvg-convert`
仅告警。若本机检测到 `~/miktex/bin/xelatex`，脚本会自动将其加入 `PATH`。

## 构建

```bash
bash build_pdf.sh
```

脚本会按顺序编译当前存在的 `introduction.md`、`chapter1.md` … `chapter10.md`、
`afterword.md`（不存在的文件自动跳过），输出单个 PDF：`庖丁解牛-pigo.pdf`。
因此书稿可随章节逐步补全而增量构建。

## 写作约定

- **章节编号**：由文档类与 pandoc `--number-sections` 自动生成，正文标题不手写编号。
- **图表编号**：图标题写作「图N-M」，`crossref.lua` 自动为其打锚点；正文中「图N-M」「第N章」
  会被转成可点击的内部链接。
- **实验盒子**：以「实验 N-M」开头的标题，或 `::: experiment` 围栏 Div，会被
  `experiment_box.lua` 渲染为带边框的高亮盒子。

## 全书大纲

全书含 **引言**、**10 章正文** 与 **后记**，按「从入口向内、再向外」的解剖顺序展开：
CLI → agentcore → 两层循环 → provider → 工具 → 压缩 → 会话 → 信任 → 子 Agent → 生态。
完整分章详解见 [`outline.md`](./outline.md)。

| 章号 | 主题 | 一句话核心 | 对应源码包 |
| --- | --- | --- | --- |
| 引言 | 为什么解剖 pigo | 从 pi 到 pigo：一只可拆解的命令行 Agent 标本 | — |
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

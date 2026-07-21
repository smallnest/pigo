# 《用 Go 从零构建 Pi Agent》

本目录存放技术书《用 Go 从零构建 Pi Agent》的书稿源文件与 PDF 构建管线。
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
├── svg2pdf.lua         # pandoc 过滤器：SVG 图引用改写为 rsvg 预转的 PDF
├── images/             # 图片资源目录（.svg 源；构建时生成同名 .pdf）
├── introduction.md     # 引言（占位，待 #202/#203/#204 替换）
└── chapter*.md         # 各章正文（由后续节点补全）
```

源文件进、产物出：生成的 `*.pdf` 不纳入版本管理（见根目录 `.gitignore`）。

## 依赖

| 依赖 | 说明 | 安装 |
| --- | --- | --- |
| pandoc | Markdown → LaTeX 转换（本书使用 3.10 验证） | `brew install pandoc` / `apt-get install pandoc` |
| xelatex | PDF 引擎，**需要可正常工作的 TeX Live**（推荐） | TeX Live（`brew install --cask mactex` 或 `apt-get install texlive-xetex texlive-lang-chinese`） |
| ElegantBook | 文档类 `elegantbook.cls` | 随 TeX Live 安装（`tlmgr install elegantbook`），或 MiKTeX 按需拉取 |
| rsvg-convert | librsvg，将 SVG 图预转为 PDF 后嵌入（**无需 Inkscape / shell-escape**） | `brew install librsvg` / `apt-get install librsvg2-bin` |
| 中文字体 | macOS：Songti SC / Heiti SC；Linux：Noto Serif/Sans CJK SC | 系统自带或 `fonts-noto-cjk` |

`build_pdf.sh` 会在缺失 `pandoc` 或找不到**可用**的 `xelatex` 时给出明确提示并退出；
缺 `rsvg-convert` 仅告警（SVG 图将无法嵌入）。脚本不再盲目信任 PATH 上的第一个
`xelatex`，而是**逐个 smoke-test** 候选引擎（依次为 `/Library/TeX/texbin`、
`/usr/local/texlive`、PATH、`~/miktex`），选用第一个能编译出 PDF 的引擎，从而自动跳过
损坏的引擎（见下文「已知问题 / 构建环境」）。

## 构建

```bash
bash build_pdf.sh
```

脚本流程：

1. 收集当前存在的 `introduction.md`、`chapter1.md` … `chapter10.md`、`afterword.md`
   （不存在的文件自动跳过），因此书稿可随章节逐步补全而增量构建。
2. 用 `rsvg-convert` 把 `images/*.svg` 预转为同名 `images/*.pdf`（向量，无损）。
3. `pandoc` 经 `svg2pdf.lua`、`crossref.lua`、`experiment_box.lua` 三个 Lua 过滤器
   生成 LaTeX，再用探测到的可用 `xelatex` 编译为单个 PDF：`用Go从零构建Pi Agent.pdf`。

`svg2pdf.lua` 把图片引用中的 `.svg` 改写为已生成的 `.pdf`，避免 pandoc 默认的
`\usepackage{svg}` + `\includesvg`（那条路径依赖 Inkscape 与 `-shell-escape`）。
生成的 `用Go从零构建Pi Agent.pdf` 与 `images/*.pdf` 均为构建产物，不纳入版本管理
（见根目录 `.gitignore`）。

### 验证产物

```bash
pdfinfo "用Go从零构建Pi Agent.pdf"              # 页数、页面尺寸
pdftotext "用Go从零构建Pi Agent.pdf" - | grep -n '庖丁解牛\|引言\|CLI 装配\|图1-1'
```

正常产出应包含：封面 + 目录（TOC）+ 引言 + 第 1 章（含图 1-1）。

## 已知问题 / 构建环境

本仓库开发机（macOS）自带的 **MiKTeX 21.7（`~/miktex/bin/xelatex`，MiKTeX-XeTeX 4.4）
存在引擎级故障**，任何输入都会在启动阶段直接崩溃：

```
FATAL xelatex.core - Bad parameter value.
FATAL xelatex.core - Data: parameterName="save_size"
Source: Libraries/MiKTeX/TeXAndFriends/include/miktex/TeXAndFriends/TeXMFMemoryHandlerImpl.h:107
```

该故障早于本书构建管线即已存在，且**与配置无关**：`initexmf --set-config-value
'[xetex]save_size=<N>'`（试过 5000 / 32768 / 40000 / 80000 / 200000）配合
`initexmf --update-fndb` 后仍复现——问题出在引擎的内存处理器（memory handler），而非
`save_size` 取值。本机也没有其他可用的 xelatex（无 TeX Live、无 tectonic）。

因此在本机 `bash build_pdf.sh` 会在引擎 smoke-test 后**明确报错退出**，不产出 PDF。
这是环境问题，不是管线问题。已逐段验证到最后一个可用阶段：

- ✅ pandoc → LaTeX：正确生成 `elegantbook` 文档类（`lang=cn, cyan, device=normal`）、
  注入 `preamble.tex`（中文字体回退）、`cover.tex`（封面 titlepage）、`\tableofcontents`；
- ✅ Lua 过滤器：`crossref.lua`（`\crossreflink` 交叉引用）、`experiment_box.lua`
  （`\begin{experimentbox}`、「实验 1-1」）、`svg2pdf.lua`（图片引用改写为 `.pdf`）均生效；
- ✅ SVG → PDF：`rsvg-convert -f pdf` 将 `images/fig1-1.svg` 转为有效单页向量 PDF
  （`pdfinfo` 确认 1 页），并以 `\includegraphics{images/fig1-1.pdf}` 嵌入。
- ⛔ xelatex → PDF：因上述引擎故障阻塞，无法在本机完成。

**在健康的 TeX 环境上构建**（任一即可）：

```bash
# macOS：安装 MacTeX（含可用 xelatex + tlmgr）
brew install --cask mactex
# 或 Linux：
sudo apt-get install -y texlive-xetex texlive-lang-chinese pandoc librsvg2-bin fonts-noto-cjk
tlmgr install elegantbook            # 若发行版未自带

bash book/build_pdf.sh               # 脚本会自动选用 /Library/TeX/texbin 等处的可用 xelatex
```

只要 PATH 或 `/Library/TeX/texbin`、`/usr/local/texlive` 下存在一个能编译的 xelatex，
脚本即会优先选用它，绕开损坏的 MiKTeX，正常产出 `用Go从零构建Pi Agent.pdf`。

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

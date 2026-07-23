# PRD：《Write Pi Agent in Go》技术书（全书大纲 + 样章）

## 引言 / Overview

为 pigo 项目编写一本技术书，采用"庖丁解牛"式的拆解：不讲空泛原理，而是**顺着 pigo 的真实源码**，逐个模块剖开一个命令行 AI Agent 是怎样从 `main()` 一路运转到一次完整对话的。

本书排版**完全复刻**参考书 [bojieli/ai-agent-book](https://github.com/bojieli/ai-agent-book) 的管线：Markdown 章节 → pandoc → xelatex → ElegantBook 文档类（cyan 主题），配 `preamble.tex`（Palatino 字体）、`cover.tex`、lua 过滤器与生成式配图，最终产出单一 PDF。

**本次交付范围**：搭好 `book/` 目录与构建管线，写出「引言」与「全书 10 章大纲」，并**详写第 1 章作为样章**，本地成功构建出 PDF。其余 9 章正文留待后续。

面向读者：想读懂/复刻一个生产级 Coding Agent 的中高级 Go 开发者，以及研究 Agent 工程的从业者。

## Goals

- 在项目根目录建立 `book/` 目录，复刻参考书的构建/排版管线，本地可一键构建 PDF。
- 产出中文「引言」，交代核心公式（Agent = LLM + 上下文 + 工具）与本书的拆解方法论。
- 产出覆盖 pigo 全部核心模块的 10 章大纲，每章明确对应的源码包/文件。
- 详写第 1 章《全景与骨架》作为样章，风格、结构、配图、实验框可作为后续章节模板。
- 章节层级与体例对齐参考书：H1 章 / H2 节 / H3 小节 + 「实验 N-M ★」框 + 「本章小结」+「思考题」。

## User Stories

### US-001：搭建 book/ 目录与构建管线骨架
**Description:** 作为作者，我需要一个可运行的构建管线，以便把 Markdown 章节编译成 ElegantBook 风格的 PDF。

**Acceptance Criteria:**
- [ ] 创建 `book/` 目录，含 `build_pdf.sh`、`preamble.tex`、`cover.tex`、`crossref.lua`、`experiment_box.lua`、`images/`。
- [ ] `build_pdf.sh` 使用 pandoc + `--pdf-engine=xelatex`，`-V documentclass=elegantbook`、`classoption=cyan/lang=cn/device=normal`，`--toc --toc-depth=3 --number-sections`，并 include `preamble.tex` 与 `cover.tex`。
- [ ] `preamble.tex` 设定中文字体回退（macOS Heiti SC / Linux Noto CJK）与 ElegantBook cyan 主题定制。
- [ ] `experiment_box.lua` 能把「实验 N-M」段落渲染为带边框的实验框（对齐参考书体例）。
- [ ] `book/README.md` 写明依赖（pandoc、xelatex/miktex、ElegantBook 文档类）与 `bash build_pdf.sh` 用法。
- [ ] 根 `.gitignore` 忽略 `book/*.pdf` 等构建产物（源码入库、产物不入库）。

### US-002：撰写全书 10 章大纲（映射 pigo 源码）
**Description:** 作为读者，我想先看到全书骨架，知道每章拆解 pigo 的哪个模块。

**Acceptance Criteria:**
- [ ] 在 `book/README.md`（或 `book/outline.md`）给出「内容速览」表：章号 / 主题 / 一句话核心 / 对应源码包。
- [ ] 10 章覆盖：CLI 装配与运行架构、agentcore 抽象、两层 Agent 循环、Provider 层与传输、工具系统、上下文压缩、会话持久化、项目信任与安全、子 Agent 编排、可扩展性生态（Skills/Plugins/包管理）。
- [ ] 每章列出对应的 `internal/*` 或 `cmd/pigo` 包/关键文件，可与仓库现有代码逐一对上。
- [ ] 引言与后记在大纲中列明。

### US-003：撰写引言 introduction.md
**Description:** 作为读者，我想在正文开始前理解本书的核心公式与"庖丁解牛"的读法。

**Acceptance Criteria:**
- [ ] `book/introduction.md`：H1 标题 + 若干 H2 节。
- [ ] 阐明 Agent = LLM + 上下文 + 工具，并说明 pigo 如何是 pi 的 Go 复刻。
- [ ] 说明本书的读法：对照源码与 commit，逐模块拆解。
- [ ] 给出阅读路径建议与前置知识（Go、LLM API 基础）。
- [ ] 引用（cross-ref）后续章节，语言为中文。

### US-004：详写样章——第 1 章《全景与骨架：从 main() 到一次对话》
**Description:** 作为读者，我想通过第 1 章建立 pigo 的全局心智模型，并作为后续章节的写作模板。

**Acceptance Criteria:**
- [ ] `book/chapter1.md`：H1 章标题，≥4 个 H2 节，含若干 H3 小节。
- [ ] 拆解 `cmd/pigo` 的运行装配：`setupAgentEnv`、`newRunConfig`、REPL 与 headless（`-p` / stream-json）两条驱动路径，引用真实文件与符号（形如 `cmd/pigo/run.go`）。
- [ ] 嵌入运行架构图（复用 `docs/pigo-runtime.*` 转换的 SVG，见 US-005）。
- [ ] 含至少 1 个「实验 N-M ★」框（例如：用 `-p` 跑一次无头对话并观察 stream-json 首个事件的 session_id）。
- [ ] 章末含「本章小结」与「思考题」两节，对齐参考书体例。
- [ ] 正文引用的代码路径/符号与仓库当前代码一致（抽查通过）。

### US-005：样章配图（运行架构图 SVG）
**Description:** 作为读者，我想在第 1 章看到清晰的运行架构图。

**Acceptance Criteria:**
- [ ] 在 `book/images/` 放入第 1 章配图（如 `fig1-1.svg`），内容为 pigo 高层运行架构（可基于 `docs/pigo-runtime.architecture.json` 重绘/导出为 SVG）。
- [ ] 图在 `chapter1.md` 中通过标准 Markdown 图片语法引用，并有图题（供 `crossref.lua` 编号）。
- [ ] 构建 PDF 时图片被 xelatex 正确嵌入（SVG 经 rsvg-convert 或等价途径）。

### US-006：本地构建 PDF 并验证
**Description:** 作为作者，我需要确认管线端到端可用。

**Acceptance Criteria:**
- [ ] 在 `book/` 下运行 `bash build_pdf.sh` 成功产出 PDF，退出码 0。
- [ ] 生成的 PDF 含封面、目录（TOC）、引言、第 1 章，且第 1 章配图可见。
- [ ] 构建命令与依赖记录在 `book/README.md`，本机（miktex/xelatex）复现通过。

## Functional Requirements

- FR-1：系统须在项目根提供 `book/` 目录，承载全部书籍源码与构建脚本。
- FR-2：`book/build_pdf.sh` 须以 pandoc + xelatex + ElegantBook（cyan 主题）把有序 Markdown 章节编译为单一 PDF。
- FR-3：构建须启用 `--toc --toc-depth=3 --number-sections`，章节编号由文档类产生（正文标题不手动编号）。
- FR-4：`preamble.tex` 须配置中文字体与跨平台回退，保证 macOS 与 Linux 均可构建。
- FR-5：`experiment_box.lua` 须把「实验」段落渲染为带样式的实验框。
- FR-6：`book/README.md` 须列出 10 章大纲，并把每章映射到具体的 pigo 源码包/文件。
- FR-7：须提供 `introduction.md`、`chapter1.md` 两篇中文正文，体例对齐参考书。
- FR-8：第 1 章须嵌入至少一张运行架构 SVG 配图，并可被交叉引用编号。
- FR-9：每篇正文章节须以「本章小结」与「思考题」收尾。
- FR-10：构建产物（PDF 等）须被 `.gitignore` 排除，源码入库。

## Non-Goals（Out of Scope）

- 本次**不**撰写第 2–10 章正文（仅产出其大纲）。
- **不**做多语言翻译（仅中文原版）。
- **不**内置任何外部实验/训练仓库。
- **不**配置 CI / GitHub Actions 自动构建与发布 Release。
- **不**制作 EPUB（仅 PDF）。
- **不**修改 pigo 自身的 Go 代码或功能。

## Design Considerations

- 体例对齐参考书：H1=章、H2=节、H3=小节；「实验 N-M ★/★★/★★★」框标注难度；章末「本章小结」+「思考题」。
- 配图优先复用已有的 `docs/pigo-runtime.*`（archify 生成）导出为 SVG，风格统一。
- 书名暂定《Write Pi Agent in Go》，可在评审时调整。

## Technical Considerations

- 依赖：pandoc、xelatex（本机已装 miktex：`~/miktex/bin/xelatex`）、ElegantBook 文档类、rsvg-convert（SVG→PDF 嵌入）、中文字体。
- ElegantBook 类若 miktex 未预装，需通过包管理器补装（构建脚本或 README 提示）。
- lua 过滤器（`crossref.lua`、`experiment_box.lua`）可参考参考书实现思路重写，不直接照搬其领域内容。
- 章节顺序遵循"从入口向内、再向外"的拆解动线：CLI → agentcore → loop → provider → tools → compaction → session → trust → subagent → 生态。

## Success Metrics

- `bash build_pdf.sh` 一次成功产出含封面/TOC/引言/第 1 章的 PDF。
- 第 1 章所有代码路径/符号引用与当前仓库一致（抽查 100% 命中）。
- 10 章大纲与 `internal/` 下实际包一一对应，无遗漏核心模块。

## Open Questions

- 书名是否采用暂定名，或你另有偏好？
- 样章「实验框」的实验是否要给出可复制的完整命令与预期输出（更贴近参考书的"88 个实验"风格）？
- 是否需要在 `book/` 内也放一份大纲的独立 `outline.md`，还是合并进 `README.md` 即可？

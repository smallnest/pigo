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

<!-- TODO(node #201): 在此填入全书 10 章大纲表格（章号、标题、要点、对应 pigo 源码模块）。 -->

*（占位：完整的 10 章大纲将由节点 #201 补全。）*

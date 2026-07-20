# pigo

使用 Go 复刻的 [pi](https://pi.dev) AI Agent —— 一个面向命令行的编码智能体，同时支持**无头（headless）脚本模式**与**交互式 REPL**。

pigo 可以读写文件、执行命令、检索代码、抓取网页，并借助大模型完成从"读懂需求"到"改好代码"的闭环。它兼容 OpenAI / Anthropic 等多种协议网关，支持会话续跑、项目信任、技能（Skills）、插件与包管理。

> 模块路径：`github.com/smallnest/pigo` · Go 1.27+

---

## 目录

- [特性一览](#特性一览)
- [安装与构建](#安装与构建)
- [快速开始](#快速开始)
- [命令行参数](#命令行参数)
- [模型与 Provider](#模型与-provider)
- [内置工具](#内置工具)
- [运行模式](#运行模式)
- [系统提示词组装](#系统提示词组装)
- [项目信任](#项目信任)
- [技能 Skills](#技能-skills)
- [插件](#插件)
- [包管理](#包管理)
- [目录与环境变量](#目录与环境变量)
- [安全说明](#安全说明)

---

## 特性一览

- **两种模式**：无头 `-p` 一次性执行（适合脚本 / CI），或直接进入交互式 REPL。
- **多 Provider**：OpenRouter（默认）、本地 Ollama、NVIDIA NIM、Anthropic、任意 OpenAI 兼容端点。
- **内置工具集**：`read` / `write` / `edit` / `grep` / `find` / `bash` / `todo` / `webfetch`。
- **会话续跑**：`--list-sessions` / `--resume` / `--continue`，无头与 REPL 均可续跑。
- **stream-json 输出**：逐行 JSON 事件，首个事件携带 `session_id`，便于调用方关联。
- **系统提示词分层组装**：base 指令 + 环境块 + `AGENTS.md`（general→specific）+ `--append-system-prompt`。
- **项目信任**：副作用工具（bash/write/edit）在未信任目录需确认，`--approve` 一次性授权。
- **技能与插件**：`~/.agents/skills` 下的 `/slash` 命令、`~/.pigo/plugins` 下的外部插件。
- **上下文自动压缩**：接近上下文窗口上限时自动摘要，亦可 `/compact` 手动触发。
- **包管理**：`pigo install npm:<pkg>` 安装 pi 生态的 extension / skill / prompt / theme。

---

## 安装与构建

需要 Go 1.27 或更高版本。

```bash
# 克隆仓库
git clone https://github.com/smallnest/pigo.git
cd pigo

# 构建二进制（生成 ./pigo）
go build ./cmd/pigo

# 或安装到 $GOPATH/bin
go install ./cmd/pigo

# 也可以不构建，直接运行
go run ./cmd/pigo -p "1+1=?"
```

---

## 快速开始

```bash
# 1. 配置默认 Provider（OpenRouter）的 API Key
export OPENROUTER_API_KEY=sk-or-...

# 2. 无头模式跑一个 prompt，打印最终回答
pigo -p "读取 README 并用三句话总结"

# 3. 进入交互式 REPL（不带 -p 且 stdout 是终端时自动进入）
pigo

# 4. 用本地 Ollama 模型，无需联网
pigo -m ollama/qwen2.5-coder -u http://localhost:11434/v1 -p "解释 main.go 做了什么"
```

---

## 命令行参数

| 长参数 | 短参数 | 默认值 | 说明 |
|--------|--------|--------|------|
| `--print` | `-p` | `""` | 无头打印模式的 prompt（也可用位置参数传入） |
| `--model` | `-m` | `openrouter/free` | 使用的模型 id |
| `--base-url` | `-u` | `""` | 覆盖 Provider 的 base URL（如本地 Ollama） |
| `--api-key` | `-k` | `""` | 指定 Provider 的 API Key（覆盖 env/config，否则读 `<PROVIDER>_API_KEY`） |
| `--protocol` | `-P` | `""` | 强制线路协议：`openai` \| `anthropic`（默认由 model id 推断） |
| `--output-format` | `-o` | `text` | 输出格式：`text` \| `stream-json` |
| `--no-tools` | `-n` | `false` | 禁用内置文件/shell 工具（同时跳过插件发现） |
| `--list-sessions` | `-l` | `false` | 列出已存储的会话并退出 |
| `--resume` | `-r` | `""` | 续跑指定 id 的会话 |
| `--continue` | `-c` | `false` | 续跑最近一次的会话 |
| `--approve` | `-a` | `false` | 为本次运行信任工作目录：跳过首次信任提示，副作用工具免逐次确认 |
| `--no-skills` | | `false` | 禁用技能发现（不加载 `~/.agents/skills` 为 `/skill-name` 命令） |
| `--system-prompt` | | `""` | 用自定义系统提示词替换默认的 coding-assistant 提示词 |
| `--append-system-prompt` | | `nil` | 向系统提示词末尾追加文本或文件内容；可重复 |

> `--subagent-rpc` 为内部参数（进程隔离子 Agent 的 JSON-RPC 服务端），不用于直接调用。

**使用例子：**

```bash
# 位置参数等价于 -p
pigo "把 utils.go 里的 getUserName 重命名为 getUsername"

# 指定模型
pigo -m anthropic/claude-3.5-sonnet -p "审查 foo.go 的并发安全性"

# 自定义系统提示词（替换默认）
pigo --system-prompt "你是一个只用中文回答的 Go 专家" -p "什么是 goroutine 泄漏"

# 追加系统提示词：可多次，值为文件路径则读取文件内容，否则作字面文本
pigo --append-system-prompt ./CONVENTIONS.md \
     --append-system-prompt "回答尽量简洁" \
     -p "为这个包补充单元测试"

# 一次性授权工作目录，让 bash/write/edit 免逐次确认
pigo -a -p "运行 go test ./... 并修复失败的用例"
```

---

## 模型与 Provider

模型 id 通过启发式规则映射到具体 Provider（`--protocol` 显式指定时优先级最高）：

1. **`--protocol`** 显式选择 → `openai`（需配合 `--base-url`）或 `anthropic`（默认公有 Anthropic API）。
2. **预置目录命中** → 使用预置声明的 Provider（REPL 中可用 `/models` 查看、`/model <id>` 切换）。
3. **`ollama/` 前缀** 或 base URL 含 `11434` → 本地 Ollama。
4. **`nvidia/` 前缀** → NVIDIA NIM。
5. **其余** → OpenRouter（默认）。

| Provider | 线路格式 | 默认 base URL | API Key 环境变量 |
|----------|----------|---------------|------------------|
| OpenRouter（默认） | OpenAI Chat Completions | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| Ollama（本地） | OpenAI 兼容 | `http://localhost:11434/v1` | 无需（本地） |
| NVIDIA NIM | OpenAI 兼容 | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` / `NVIDIA_NIM_API_KEY` |
| OpenAI 兼容 | OpenAI Chat Completions | 需自行提供 `--base-url` | `OPENAI_API_KEY` |
| Anthropic | Anthropic Messages | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` / `CLAUDE_API_KEY` |

Key 解析顺序：OAuth token → `--api-key` → 环境变量 → 配置文件。其他 Provider（google/deepseek/xai/groq/mistral 等）遵循 `<PROVIDER>_API_KEY` 约定。

**使用例子：**

```bash
# 默认 OpenRouter
export OPENROUTER_API_KEY=sk-or-...
pigo -p "写一个快排"

# 任意 OpenAI 兼容端点，强制 openai 协议
pigo -P openai -u https://my-gateway.example.com/v1 -m my-model -k $MY_KEY -p "..."

# 公有 Anthropic API
export ANTHROPIC_API_KEY=sk-ant-...
pigo -P anthropic -m claude-3-5-sonnet-20241022 -p "..."
```

---

## 内置工具

工具集根植于当前工作目录，`--no-tools` 可整体禁用。

| 工具 | 说明 |
|------|------|
| `read` | 按路径读取文本文件，支持行 offset/limit，输出带行号，超大文件截断 |
| `write` | 创建或覆盖文件，按需创建父目录 |
| `edit` | 精确字符串替换（`old_string` 需唯一，除非 `replace_all`），返回 diff |
| `grep` | 正则检索文件内容，支持 glob 过滤，跳过 `.gitignore` 路径 |
| `find` | 按文件名 glob 查找文件，跳过 `.gitignore` 路径 |
| `bash` | 执行 shell 命令，流式 stdout/stderr，支持超时与取消 |
| `todo` | 记录/更新结构化任务清单，每次提交整份列表（pending/in_progress/completed） |
| `webfetch` | 抓取 URL 并转为精简 Markdown 正文，HTTP 自动升级 HTTPS |

> `bash` / `write` / `edit` 属于"副作用工具"，在未信任目录下需确认（见[项目信任](#项目信任)）。

---

## 运行模式

```bash
# 无头打印模式：只输出最终回答文本
pigo -p "总结这个仓库的架构"

# stream-json：逐行 JSON 事件，首个事件带 session_id
pigo -p "列出所有 Go 文件" --output-format stream-json

# 交互式 REPL：不带 -p 且 stdout 为终端时进入
pigo

# 会话管理
pigo --list-sessions              # 列出会话
pigo --resume 20260720-1530-abcd  # 续跑指定会话（无头/REPL 均可）
pigo --continue                   # 续跑最近一次会话
```

REPL 中的内置斜杠命令包括 `/model`、`/models`、`/help`、`/compact`、`/fork`、`/clone`、`/tree`、`/export`、`/import`、`/copy`、`/session`、`/exit` 等。

---

## 系统提示词组装

系统提示词按三层顺序拼装（`internal/runtime/prompt.go`）：

1. **base 指令**：默认的 coding-assistant 提示词，可用 `--system-prompt` 整体替换。
2. **环境块**：工作目录、OS/架构、当前日期。
3. **`AGENTS.md` 注入**：从仓库根目录到当前工作目录，**由通用到具体**依次拼接——越靠近工作目录（越具体）的 `AGENTS.md` 排在越后，优先级更高。

`--append-system-prompt` 的内容追加在最后，按参数顺序排列；每个值若为存在的普通文件则读取文件内容，否则作为字面文本，空条目跳过。

---

## 项目信任

副作用工具（`bash` / `write` / `edit`）在**未信任**或**未决定**的目录下需要逐次确认。信任状态按目录三态（Trusted / Untrusted / Undecided）持久化为 JSON。

- 首次在某目录启动 REPL 时会提示是否信任。
- `--approve` / `-a` 为本次运行一次性授予会话级信任，跳过首次提示并免逐次确认。

---

## 技能 Skills

技能是带 YAML frontmatter（`name`、`description`，可选 `allowed-tools`、`model`）的 Markdown 文件，位于 `~/.agents/skills`（可用 `PIGO_SKILLS_DIR` 覆盖）：

- 支持扁平的 `*.md` 与嵌套的 `<name>/SKILL.md`。
- 每个技能在 REPL 中暴露为 `/skill-name` 斜杠命令（展开正文为 prompt，支持 `$ARGUMENTS` 替换），也可作为子 Agent 工具运行。
- `--no-skills` 禁用技能发现；格式错误的技能会被非致命地跳过。

---

## 插件

外部插件从 `$PIGO_HOME/plugins`（默认 `~/.pigo/plugins`）发现：

- 容错发现——启动失败的插件会被记录并跳过。
- 插件可提供额外工具，并订阅 Agent 生命周期事件。
- `--no-tools` 会整体跳过插件发现。

---

## 包管理

安装 pi 生态的包（extension / skill / prompt / theme）。`install` 需要 PATH 上有 `npm`。

```bash
# 安装（仅支持 npm: 源，支持 scoped 包与指定版本）
pigo install npm:pi-mcp-adapter
pigo install npm:@scope/name@1.2.3

# 列出已安装的包
pigo list

# 更新（无参数则更新全部）
pigo update
pigo update pi-mcp-adapter

# 卸载
pigo uninstall pi-mcp-adapter
```

包类型（`extension` / `skill` / `prompt` / `theme`）会分别分发到对应目录，安装记录写入 lockfile。

---

## 目录与环境变量

| 变量 / 路径 | 用途 |
|-------------|------|
| `PIGO_HOME` | 覆盖 `~/.pigo` 基础目录（影响 plugins 与 commands） |
| `PIGO_SKILLS_DIR` | 覆盖技能目录（默认 `~/.agents/skills`） |
| `~/.pigo/sessions` | 会话存储（JSONL） |
| `~/.pigo/plugins` | 外部插件 |
| `~/.pigo/commands` | 用户自定义命令模板 |
| `<PROVIDER>_API_KEY` | 各 Provider 的 API Key（见[模型与 Provider](#模型与-provider)） |

---

## 安全说明

- pigo 会向解析出的 Provider 端点发起外部网络请求。
- `bash` / `write` / `edit` 会在本地产生副作用，仅由项目信任机制把关；`--approve` 会跳过逐次确认，请在受信任的目录中使用，权衡便利与安全。
- 处理来自文件、命令输出、网页等外部来源的内容时应视为不可信数据。

---

## 许可证

参见仓库根目录的 [LICENSE](LICENSE)。


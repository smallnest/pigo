# PRD: Prompt Templates（提示词模板）

## Introduction

pigo 已经有一套"声明式斜杠命令"机制（`internal/runtime/slashcommand.go` 的 `LoadUserCommandsDir` / `ParseUserCommand`）：把 `~/.pigo/commands/*.md` 加载成 `/name` 命令，文件正文是提示词模板，展开时仅做 `$ARGUMENTS` 替换。但相比 pi 的 **prompt templates**（<https://pi.dev/docs/latest/prompt-templates>），pigo 缺口很大：

- **加载来源单一**：只从 `~/.pigo/commands` 加载；缺项目级 `.pigo/prompts`（受信任时）、config.toml 的 `prompts` 数组、`--prompt-template` CLI flag、`--no-prompt-templates` 关闭开关。
- **参数语法薄弱**：只支持 `$ARGUMENTS`；缺 pi 的 `$1`..`$N` 位置参数、`$@`、`${1:-default}` / `${@:-default}` / `${ARGUMENTS:-default}` 默认值、`${@:N}` / `${@:N:L}` 切片，以及 shell 风格的引号参数解析（`/x Button "click handler"`）。
- **缺少 `argument-hint`**：frontmatter 只有 `description` / `name`，没有 `argument-hint`，自动补全也不展示参数提示。
- **缺来源间优先级**：当前 `SlashRegistry` 只有"built-in 胜 + 用户层后写覆盖"，没有跨来源（项目/全局/包/配置/CLI）的优先级。

本 PRD 的目标是让 pigo 对齐 pi 的 prompt templates：采用 `prompts` 命名（`~/.pigo/prompts`、`.pigo/prompts`），保留对旧 `~/.pigo/commands` 的向后兼容加载；实现完整的参数展开语法与 `argument-hint`；支持全部六种发现来源与明确的跨来源优先级；并把包管理器的安装目标迁移到 `~/.pigo/prompts`、识别 pi 的 `prompts/` 目录约定。

参考文档：<https://pi.dev/docs/latest/prompt-templates>

## Goals

- 采用 `prompts` 命名（`~/.pigo/prompts`、`.pigo/prompts`），同时继续加载旧 `~/.pigo/commands/*.md`，老用户零回归。
- 实现与 pi 一致的模板参数语法：`$1`..`$N`、`$@`/`$ARGUMENTS`、`${1:-default}`、`${@:-default}`、`${ARGUMENTS:-default}`、`${@:N}`、`${@:N:L}`，以及 shell 风格引号参数解析。
- 支持全部六种发现来源：全局 `~/.pigo/prompts`、项目 `.pigo/prompts`（受信任时）、包、config.toml `prompts` 数组、`--prompt-template <path>`（可重复）、`--no-prompt-templates` 关闭开关。
- frontmatter 支持 `argument-hint`，并在 REPL Tab 补全与 `/help` 中以 `name <hint> - description` 形式展示；`description` 缺省时回退为首行非空正文。
- 跨来源同名模板按明确优先级解析：built-in > project > global > package > settings > CLI，败者丢弃并报告。
- 包管理器识别 pi 的 `prompts/` 目录约定（除现有 `commands/` 外），安装目标改为 `~/.pigo/prompts`。
- 发现均为非递归；子目录模板仅在被显式引用（settings/CLI/包清单）时加载。

## User Stories

### US-001: shell 风格参数分词器
**Description:** 作为用户，我希望 `/component Button "click handler"` 中的引号参数被正确切分，这样 `$2` 能拿到 `click handler` 整体。

**Acceptance Criteria:**
- [ ] 新增参数分词函数（如 `runtime.SplitArgs(s string) ([]string, error)`），使用成熟的 shell 引号库（如 `github.com/kballard/go-shellquote`），遵循全局"复用而非自实现"规则
- [ ] `Button "click handler"` -> `["Button", "click handler"]`
- [ ] 单引号 `'a b'` 同样作为一个参数
- [ ] 空字符串输入 -> `[]`（无 error）
- [ ] 未闭合引号 -> 返回 error
- [ ] 去除每个参数首尾的成对引号，但保留内部空格
- [ ] 新增单测覆盖：空、单参数、双引号、单引号、混合、未闭合引号、前后多余空白
- [ ] Typecheck/lint（`go build ./...` 与 `go vet ./...`）通过

### US-002: 模板参数展开引擎
**Description:** 作为模板作者，我希望模板正文支持 `$1`、`$@`、`${1:-default}`、`${@:N:L}` 等 pi 语法，按位置/默认值/切片展开。

**Acceptance Criteria:**
- [ ] 新增展开函数（如 `runtime.ExpandTemplate(template string, args []string) string`）
- [ ] `$1`..`$N` 展开为第 N 个位置参数（1-indexed）；越界 -> 空字符串
- [ ] `$@` 与 `$ARGUMENTS` 展开为全部参数以单空格连接
- [ ] `${1:-default}`：arg1 存在且非空则用 arg1，否则用 `default`；`${@:-default}`、`${ARGUMENTS:-default}` 同理
- [ ] `${@:N}` 展开为从第 N 个起的所有参数（1-indexed，空格连接）；`${@:N:L}` 为从第 N 个起的 L 个参数
- [ ] **展开顺序**：先处理 `${...}` 形式，再处理裸 `$N`/`$@`/`$ARGUMENTS`，确保 `$1` 不会误匹配 `${1:-...}` 内部、默认值字面量中的 `$` 不会被二次展开
- [ ] 模板无任何占位符时：有参数则追加参数、无参数则原样返回（保持现有 `ParseUserCommand` 行为）
- [ ] 新增单测覆盖每种形式、越界、空默认值、`${...}` 与裸 `$` 共存、默认值含 `$` 字符
- [ ] Typecheck/lint 通过

### US-003: 把新引擎接入 ParseUserCommand
**Description:** 作为维护者，我希望 `ParseUserCommand` 用新的分词+展开引擎替换现有仅 `$ARGUMENTS` 的逻辑，使所有用户模板自动获得新语法。

**Acceptance Criteria:**
- [ ] `ParseUserCommand`（`internal/runtime/slashcommand.go`）的 `Expand` 闭包改为：先 `SplitArgs(args)`，再 `ExpandTemplate(template, tokens)`
- [ ] 分词失败（如未闭合引号）时，`Expand` 回退为把原始 `args` 整体作为 `$ARGUMENTS`（不向用户抛错），保证可用性
- [ ] 现有仅含 `$ARGUMENTS` 的模板行为不变（回归测试）
- [ ] 现有无占位符模板行为不变（追加参数）
- [ ] 新增单测：`/review`（无参）、`/component Button "click handler"`（多参）、`${1:-7}` 默认值
- [ ] Typecheck/lint 通过

### US-004: frontmatter 新增 argument-hint 与 description 回退
**Description:** 作为模板作者，我希望在 frontmatter 写 `argument-hint` 并在缺 `description` 时自动用首行正文，这样补全与帮助信息更友好。

**Acceptance Criteria:**
- [ ] `ParseUserCommand` 解析 frontmatter 中的 `argument-hint`（YAML 键 `argument-hint`）
- [ ] `SlashCommand` 结构体新增 `ArgumentHint string` 字段并承载该值
- [ ] `description` 缺省时，回退为模板正文第一个非空行（去除首尾空白）
- [ ] `description` 与 `argument-hint` 均可选；两者皆无时不报错
- [ ] 现有 `name` frontmatter 覆盖文件名行为保持不变
- [ ] 新增单测：仅 description、仅 argument-hint、两者皆有、两者皆无（回退首行）
- [ ] Typecheck/lint 通过

### US-005: 全局 prompts 目录 + 旧 commands 向后兼容
**Description:** 作为用户，我希望 pigo 从 `~/.pigo/prompts` 加载模板（对齐 pi），同时我旧的 `~/.pigo/commands/*.md` 仍能用。

**Acceptance Criteria:**
- [ ] `cmd/pigo/interactive.go` 在加载用户模板时同时读取 `$PIGO_HOME/prompts`（或 `~/.pigo/prompts`）与 `$PIGO_HOME/commands`（或 `~/.pigo/commands`）两个目录
- [ ] 两个目录均非递归加载 `*.md`
- [ ] 缺失目录静默跳过（无 error），与现有 `LoadUserCommandsDir` 行为一致
- [ ] 两个目录下的模板都标记为 global 层
- [ ] 新增单测/集成测试：仅在 `~/.pigo/commands` 放模板时命令仍可用（回归）；仅在 `~/.pigo/prompts` 放模板时命令可用
- [ ] Typecheck/lint 通过

### US-006: 项目级 .pigo/prompts（受信任时加载）
**Description:** 作为项目维护者，我希望在仓库 `.pigo/prompts/` 放项目专属模板，团队在该项目里就能用 `/review` 等，且仅受信任目录加载。

**Acceptance Criteria:**
- [ ] 工作目录下的 `.pigo/prompts/*.md` 被非递归加载为项目级模板
- [ ] 仅当当前项目处于 trusted 状态时加载；untrusted/undecided 时静默跳过（无 error、不打印告警）
- [ ] 项目级模板标记为 project 层（优先级高于 global）
- [ ] 复用现有 trust manager（`internal/trust`）判定信任状态
- [ ] 新增单测：trusted 时加载、untrusted 时不加载、目录缺失时无 error
- [ ] Typecheck/lint 通过

### US-007: config.toml 的 prompts 数组
**Description:** 作为用户，我希望在 `~/.config/pigo/config.toml` 写 `prompts = ["./my-prompts", "/abs/x.md"]` 来追加模板来源（文件或目录）。

**Acceptance Criteria:**
- [ ] `fileConfig`（`cmd/pigo/config_file.go`）新增 `Prompts []string` 字段（TOML 键 `prompts`）
- [ ] 每个条目：文件 -> 加载该单个模板；目录 -> 非递归加载其下 `*.md`
- [ ] 这些模板标记为 settings 层
- [ ] 不存在的路径 -> 跳过并打印一行告警（不致命）
- [ ] `applyFileConfig` 把 `prompts` 透传到运行时（CLI flag 未覆盖时）
- [ ] 新增单测：解析 TOML、文件条目、目录条目、不存在条目跳过
- [ ] Typecheck/lint 通过

### US-008: CLI flag --prompt-template 与 --no-prompt-templates
**Description:** 作为用户/脚本作者，我希望用 `--prompt-template ./x.md` 临时加载模板、用 `--no-prompt-templates` 完全关闭模板发现。

**Acceptance Criteria:**
- [ ] `cmd/pigo/main.go` 新增 `--prompt-template <path>` flag（可重复，`StringArrayVar`）
- [ ] 每个 `--prompt-template` 路径：文件 -> 单模板；目录 -> 非递归 `*.md`；标记为 CLI 层
- [ ] 新增 `--no-prompt-templates` bool flag：置 true 时关闭全部模板发现（global/project/settings/CLI），built-in 斜杠命令不受影响
- [ ] `--no-prompt-templates` 与 `--no-skills` 互相独立
- [ ] 不存在的 `--prompt-template` 路径 -> 跳过并打印一行告警
- [ ] 新增单测：`--prompt-template` 加载文件与目录、`--no-prompt-templates` 关闭发现、两 flag 独立
- [ ] Typecheck/lint 通过

### US-009: SlashRegistry 分层优先级解析
**Description:** 作为维护者，我需要一个明确的跨来源优先级规则，使同名模板按 `built-in > project > global > package > settings > CLI` 解析，避免后写覆盖的不确定性。

**Acceptance Criteria:**
- [ ] `SlashCommand` 或注册表引入显式 tier 概念，枚举：Builtin > Project > Global > Package > Settings > CLI
- [ ] 同名模板：高 tier 胜出；低 tier 被丢弃并计入 `shadowed`（沿用现有冲突报告机制）
- [ ] built-in 始终胜出（保持现有 built-in-wins）
- [ ] skills/plugins 仍按现有来源标签展示，但其 tier 归入 Global 层（与用户全局模板同级，后写覆盖仅在同级内生效）
- [ ] `Shadowed()` 输出包含被丢弃的模板名及其来源 tier，便于诊断
- [ ] 新增单测：project 覆盖 global、global 覆盖 settings、built-in 覆盖 project、同级后写覆盖
- [ ] Typecheck/lint 通过

### US-010: REPL Tab 补全展示 argument-hint 与 description
**Description:** 作为用户，我希望按 Tab 时看到 `name <hint> - description`，一眼知道每个模板怎么用。

**Acceptance Criteria:**
- [ ] `cmd/pigo/line_editor.go` 的斜杠命令补全渲染为 `name <argument-hint> - description`（无 hint 时省略 `<...>` 段，仅 `name - description`）
- [ ] 列按现有补全列宽对齐（参考现有 `/model` 目录补全的排版）
- [ ] prompt templates 与 built-in/skill/plugin 命令在同一列表中按名字排序展示
- [ ] 新增单测：有 hint、无 hint、有 description、description 回退首行 四种渲染
- [ ] 在 REPL 中验证（启动 REPL，输入 `/` 后按 Tab，确认渲染）
- [ ] Typecheck/lint 通过

### US-011: /help 列表展示模板信息
**Description:** 作为用户，我希望 `/help`（列出可用命令）里看到每个 prompt template 的 name、argument-hint、description 与来源。

**Acceptance Criteria:**
- [ ] 现有列出斜杠命令的 built-in（`cmd/pigo/interactive.go` 中 `list available slash commands`）扩展输出 prompt templates
- [ ] 每条模板输出：`/name <argument-hint> - description (source: <tier>)`（无 hint 时省略）
- [ ] 与 built-in/skill/plugin 命令统一排序展示
- [ ] 新增单测：含 hint、无 hint、各来源 tier 标注
- [ ] 在 REPL 中验证（输入 `/help`，确认输出含模板条目与来源）
- [ ] Typecheck/lint 通过

### US-012: 包管理器识别 prompts/ 约定并安装到 ~/.pigo/prompts
**Description:** 作为包作者，我希望我的 pi 风格 prompt 包（模板放在 `prompts/` 下）能被 `pigo install` 正确安装到 `~/.pigo/prompts`。

**Acceptance Criteria:**
- [ ] `internal/pkgmgr/distribute_prompt.go` 依次查找 `prompts/`、`commands/`、包根 `*.md`（现有 `commands/` 优先级降为第二）
- [ ] 安装目标目录由 `CommandsDir()` 改为 `PromptsDir()`（`$PIGO_HOME/prompts` 或 `~/.pigo/prompts`）
- [ ] 包根 `*.md` 回退时仍跳过 `README.md`
- [ ] lockfile 记录的安装路径随之更新，`pigo uninstall` 能精确移除
- [ ] 新增/更新单测：`prompts/` 优先、回退 `commands/`、回退根 `*.md`、安装到 `~/.pigo/prompts`、卸载
- [ ] Typecheck/lint 通过

### US-013: 文档更新（README + 电子书）
**Description:** 作为用户，我希望 README 与电子书清楚记录新模板目录、flag、参数语法与 argument-hint，便于使用。

**Acceptance Criteria:**
- [ ] README "技能与插件"/"目录与环境变量"小节记录：`~/.pigo/prompts`、`.pigo/prompts`（受信任时）、`--prompt-template`、`--no-prompt-templates`、config.toml `prompts`、`argument-hint`
- [ ] README 命令行参数表新增 `--prompt-template` 与 `--no-prompt-templates` 两行
- [ ] 给出参数语法速查表（`$1`、`$@`、`${1:-default}`、`${@:N}`、`${@:N:L}`）与一个完整模板示例
- [ ] 配套电子书相关章节同步更新（若有 prompt/命令章节）
- [ ] 文档中 Markdown 链接可正常渲染（`grep` 校验无死链格式错误）
- [ ] Typecheck/lint 通过

## Functional Requirements

- FR-1: 系统必须非递归加载 `~/.pigo/prompts/*.md`（全局），并向后兼容加载 `~/.pigo/commands/*.md`。
- FR-2: 系统必须加载工作目录下 `.pigo/prompts/*.md`（项目级），且仅当项目受信任时加载；未信任时静默跳过。
- FR-3: 系统必须加载 config.toml `prompts` 数组中每个条目（文件或非递归目录）作为模板。
- FR-4: 系统必须为每个 `--prompt-template <path>` flag 加载模板（文件或非递归目录），flag 可重复。
- FR-5: 系统在 `--no-prompt-templates` 置位时必须关闭全部模板发现（全局/项目/配置/CLI）；built-in 斜杠命令不受影响。
- FR-6: 模板文件名（去掉 `.md`）必须成为命令名，可经 `/name` 调用。
- FR-7: 系统必须解析可选 YAML frontmatter 的 `description`、`name`、`argument-hint` 三个字段。
- FR-8: 当 `description` 缺省时，系统必须用模板正文第一个非空行作为描述。
- FR-9: 系统必须用 shell 风格引号分词处理调用参数（如 `Button "click handler"` -> `["Button", "click handler"]`）。
- FR-10: 系统必须把 `$1`..`$N` 展开为第 N 个位置参数（1-indexed，越界为空字符串）。
- FR-11: 系统必须把 `$@` 与 `$ARGUMENTS` 展开为全部参数以单空格连接。
- FR-12: 系统必须把 `${1:-default}` 展开为：arg1 存在且非空则 arg1，否则 `default`；`${@:-default}`、`${ARGUMENTS:-default}` 同理。
- FR-13: 系统必须把 `${@:N}` 展开为从第 N 个起的所有参数，`${@:N:L}` 为从第 N 个起的 L 个参数（1-indexed）。
- FR-14: 系统必须先展开 `${...}` 形式再展开裸 `$N`/`$@`/`$ARGUMENTS`，避免 `$1` 误匹配 `${1:-...}` 内部及默认值中的 `$` 被二次展开。
- FR-15: 同名模板跨来源冲突时，系统必须按 `built-in > project > global > package > settings > CLI` 解析；败者丢弃并计入冲突报告。
- FR-16: built-in 斜杠命令必须始终胜过同名模板。
- FR-17: REPL Tab 补全必须把 prompt templates 渲染为 `name <argument-hint> - description`（无 hint 时省略 hint 段）。
- FR-18: `/help`（列出命令）输出必须包含 prompt templates 的 name、argument-hint（若有）、description 与来源 tier。
- FR-19: 包管理器必须识别 prompt 包的 `prompts/` 子目录（pi 约定，优先于 `commands/`），并把模板安装到 `~/.pigo/prompts`。
- FR-20: 模板发现必须是非递归的；子目录模板仅在被 settings/CLI/包清单显式引用时加载。

## Non-Goals

- 不做递归目录发现。
- 不引入文档化 pi 语法之外的模板能力（无条件分支、循环、任意 shell 展开、子进程调用）。
- 不做模板版本管理或逐模板启用/禁用配置项。
- 不迁移/重命名用户现有的 `~/.pigo/commands/*.md`（原地继续加载）。
- 不改动 built-in 斜杠命令行为，也不改动 skills/plugins 加载逻辑（除 tier 归并外）。
- 不在 Tab 补全之外新增 GUI/TUI 模板选择器。
- 不在现有包管理器之外引入远程模板注册表。

## Design Considerations

- 复用现有 `splitFrontmatter`（skills 模块）与 `gopkg.in/yaml.v3` 解析 frontmatter。
- 复用 `SlashRegistry` 的 `shadowed` 冲突报告机制承载跨来源败者。
- 复用 `internal/trust` manager 判定项目级模板的信任门控。
- 复用 `cmd/pigo/line_editor.go` 现有斜杠命令补全框架，仅扩展渲染格式与列对齐。
- `argument-hint` 用 `<angle>` 表示必选参数、`[square]` 表示可选（仅展示约定，不做语义校验）。
- 命令行参数表风格与现有 `--no-skills` 等保持一致。

## Technical Considerations

- **分词库复用**：shell 引号分词必须用成熟开源库（如 `github.com/kballard/go-shellquote`），遵循全局"复用而非自实现"规则，不手写 tokenizer。
- **展开引擎自实现**：`${1:-default}` / `${@:N:L}` 等是 pi 专属语法，无通用库，自实现合适；核心难点是 `${...}` 与裸 `$` 的展开顺序（FR-14）。
- **优先级与现有注册表的张力**：`SlashRegistry` 现为"built-in 胜 + 用户层后写覆盖"，需引入显式 tier 字段；skills/plugins 归入 Global 层，仅同级内后写覆盖。
- **包安装与 package 层**：pigo 包管理器目前把模板**复制**进全局目录。按 FR-15 的 `global > package`，复制进 `~/.pigo/prompts` 的包模板实际与全局同层。若需真正独立的 package 层，需改安装到独立目录（如 `~/.pigo/packages/prompts/`）并按更低优先级发现——见 Open Questions，本期暂以"安装即并入 global"实现。
- **配置**：`fileConfig` 增 `Prompts []string`（TOML `prompts`）；`--prompt-template` 用 `StringArrayVar` 可重复；`--no-prompt-templates` 与 `--no-skills` 独立。
- **加载顺序**：各来源加载后按 tier 注册进同一 `SlashRegistry`，由 tier 解析冲突；加载本身顺序无关。

## Success Metrics

- pi.dev 文档列出的全部参数语法形式按文档展开（单测覆盖）。
- 现有 `~/.pigo/commands/*.md` 用户无回归（回归测试通过）。
- Tab 补全与 `/help` 正确展示 `argument-hint` + `description` + 来源。
- 在受信任项目放置 `.pigo/prompts/review.md` 后，立即可 `/review` 调用。
- 一个 pi 风格 prompt 包（`prompts/` 目录）经 `pigo install` 后模板出现在 `~/.pigo/prompts` 且可调用。

## Open Questions

- 包安装的 package 层：本期"安装即并入 global"是否可接受？还是需要独立安装目录（`~/.pigo/packages/prompts/`）以真正实现 `global > package`？（FR-15 列出 package 层，但复制式安装使其与 global 同层。）
- `--prompt-template` 是否需要支持 glob 模式？文档为 `<path>`，本期按"文件或目录"实现，glob 暂不支持。
- 未信任项目的 `.pigo/prompts` 是静默跳过还是打印一行提示？本期按"静默跳过"实现，与其它信任门控一致。
- skills/plugins 归入 Global 层后，是否需要在 `/help` 中保留独立来源标签（skill/plugin）以便区分？本期保留标签用于展示，tier 仍为 Global。

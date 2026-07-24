# PRD: 内置 Skills 首次运行自动安装

## Introduction

pigo 目前的 skills 全部依赖用户手动安装（`pigo install npm:...` 或把目录放进 `~/.agents/skills`）。新用户开箱后没有任何可用的工作流技能，需要自己去 goal.rpcx.io 逐条安装，门槛高。

本功能在 pigo **首次运行**时自动把一批"内置 skills"落地到用户的 skills 目录（`~/.agents/skills`，可被 `PIGO_SKILLS_DIR` 覆盖），使 `/prd`、`/to-issues`、`/refactor` 等命令开箱即用。

技术路线（已确认）：
- **1A** — skills 内容通过 Go `embed` 打进 pigo 二进制，首次运行时复制到磁盘。完全离线、无外部依赖（不依赖 `npx`/`gh`/网络）。
- **2A（去掉 humanize-it）** — 内置 goal-workflow 全套技能（**排除 `humanize-it`**），另加 `architecture-diagram`、`weather` 两个独立技能。
- **3C** — 首次运行**静默安装、无提示、无输出**。
- **4A** — 失败时优雅降级：记录一条 warning（或静默跳过），pigo 正常启动，下次运行再重试。
- **5A** — 设计一个通用的"内置 skills 清单（manifest）"机制，本期用 goal-workflow 填充，后续可扩展。

内置技能来源：
- `smallnest/goal-workflow`（https://goal.rpcx.io/index_cn.html#install）
- 另外两个独立技能：`architecture-diagram`、`weather`

内置技能清单（16 个）：
- goal-workflow 核心：`prd`、`prd-to-spec`、`to-issues`、`review-it`、`ship-it`
- goal-workflow 附加（排除 `humanize-it`）：`insight-diagram`、`refactor`、`modern-go`、`note-it`、`code-to-spec`、`smell`、`loop-it`、`to-design`、`graph`
- 独立技能：`architecture-diagram`、`weather`

> 注：`/goal` 是 Claude Code / pigo 内置能力，不属于本清单。`weather` 运行时依赖 `curl`；`architecture-diagram` 生成 HTML+SVG，无外部依赖。

## Goals

- 新用户首次运行 pigo 后，goal-workflow 内置技能立即以 `/skill-name` 形式可用，无需任何手动步骤。
- 安装完全离线，不依赖 `npx`、`gh` 或网络。
- 首次运行安装过程静默（无提示、无输出），不打断用户。
- 安装失败不阻塞 pigo 启动；下次运行自动重试。
- 内置技能清单可扩展，为将来加入更多技能集预留通用机制。
- 不覆盖用户已存在的同名技能（用户的修改优先）。

## User Stories

### US-001: 嵌入内置 skills 到二进制
**Description:** As a pigo maintainer, I want the goal-workflow skills embedded into the binary so they ship with pigo and install offline.

**Acceptance Criteria:**
- [ ] 在 `internal/builtinskills`（新包）下用 `//go:embed` 嵌入 16 个技能目录（每个含 `SKILL.md` 及其支撑文件）：goal-workflow 13 个（排除 `humanize-it`）+ `architecture-diagram` + `weather`
- [ ] 提供导出的 `embed.FS`（或等价访问器），可枚举每个技能的名字与文件树
- [ ] 提供 `Manifest()`（或等价）返回内置技能名字列表，供 5A 的通用机制消费
- [ ] `go build ./...` 通过，嵌入的文件出现在编译产物中（通过单测用 embed.FS 读取校验）
- [ ] `go vet ./...` / lint 通过

### US-002: 首次运行判定与状态标记
**Description:** As a developer, I need pigo to detect "first run" reliably so bootstrap happens exactly once and can retry after failure.

**Acceptance Criteria:**
- [ ] 在 `$PIGO_HOME`（默认 `~/.pigo`）下用一个 sentinel 文件（如 `builtin-skills.state`，记录已安装的技能集版本/清单）判定是否已 bootstrap
- [ ] sentinel 不存在或记录的清单版本落后于当前二进制 → 触发 bootstrap
- [ ] bootstrap **成功后**才写入/更新 sentinel；失败则不写，保证 4A 的"下次重试"
- [ ] `$PIGO_HOME` 不可解析时（Home() 返回 ""）跳过 bootstrap 且不报错
- [ ] 单测覆盖：首次（无 sentinel）触发、已安装（sentinel 存在且版本匹配）跳过、版本落后触发重装

### US-003: 内置 skills 落地到 skills 目录
**Description:** As a user, I want the embedded skills copied into my skills directory on first run so they load as slash commands.

**Acceptance Criteria:**
- [ ] 把每个内置技能复制到 `SkillsDir()/<name>/`（`PIGO_SKILLS_DIR` 优先，否则 `~/.agents/skills`），保持 `SKILL.md` 及支撑文件的目录结构
- [ ] 目标 `SkillsDir()/<name>/` **已存在则跳过该技能**（不覆盖用户的修改），并视为该技能安装成功
- [ ] 复制过程中单个技能失败不中断其余技能（继续安装剩余的），整体记录哪些成功
- [ ] 安装后，`loadSkillCommands()` 能发现这些技能并暴露为 `/prd`、`/refactor` 等命令
- [ ] 单测：向临时 `PIGO_SKILLS_DIR` 安装后，目标目录出现 16 个技能子目录，且每个含 `SKILL.md`
- [ ] 单测：预置一个同名技能目录，bootstrap 后其内容不被覆盖

### US-004: 通用内置 skills manifest 机制
**Description:** As a maintainer, I want a general "built-in skill sets" manifest so future skill collections can be added without rewriting the bootstrap flow.

**Acceptance Criteria:**
- [ ] bootstrap 逻辑基于一个清单结构（如 `[]BuiltinSkillSet{ Name, Version, Skills []string, FS embed.FS }`），而非硬编码具体技能名
- [ ] 新增一个技能集只需向清单追加一项 + 嵌入其文件，无需改动安装/首次运行判定代码
- [ ] goal-workflow（13 个）与独立技能（`architecture-diagram`、`weather`）作为清单的初始条目
- [ ] 单测：向清单注入一个测试用技能集，bootstrap 能安装它（证明机制通用）

### US-005: 在启动流程静默触发 bootstrap（3C + 4A）
**Description:** As a user, I want built-in skills installed silently on first launch without prompts or output, and without blocking startup if it fails.

**Acceptance Criteria:**
- [ ] 在 REPL 启动路径（`dispatch`/`runInteractive`，`cmd/pigo/run.go`）中，加载 skill 命令**之前**调用 bootstrap
- [ ] 正常路径：无任何 stdout 提示、无交互（3C）
- [ ] 失败路径（如 skills 目录不可写）：pigo 继续正常启动进入 REPL；错误仅在 `PIGO_DEBUG`/verbose 下打印，正常模式静默（4A）
- [ ] bootstrap 只在交互式 REPL 首次运行触发；headless（`-p`）模式行为按 Non-Goals 处理
- [ ] 单测：模拟首次运行后 REPL 的 slash registry 中包含内置技能命令
- [ ] 单测：模拟 bootstrap 失败（skills 目录设为不可写/非法路径）时 `dispatch` 不返回错误、不 panic

## Functional Requirements

- FR-1: 系统必须在 `internal/builtinskills` 包中用 `//go:embed` 嵌入 16 个技能：goal-workflow 13 个（排除 `humanize-it`）+ `architecture-diagram` + `weather`。
- FR-2: 系统必须提供内置技能集清单（manifest），描述每个技能集的名字、版本与技能列表。
- FR-3: 系统必须在 `$PIGO_HOME` 下维护一个 sentinel 状态文件，用于判定是否已完成 bootstrap 及所安装的清单版本。
- FR-4: 系统必须在 sentinel 缺失或版本落后时触发 bootstrap，并仅在成功后更新 sentinel。
- FR-5: 系统必须把每个内置技能复制到 `SkillsDir()/<name>/`，保持目录结构。
- FR-6: 当 `SkillsDir()/<name>/` 已存在时，系统必须跳过该技能而不覆盖。
- FR-7: 单个技能复制失败时，系统必须继续安装其余技能。
- FR-8: 系统必须在 REPL 启动、加载 skill 命令之前静默触发 bootstrap，无提示、无输出。
- FR-9: bootstrap 失败时，系统必须让 pigo 正常启动，仅在 debug/verbose 下输出错误。
- FR-10: 当 `Home()` 或 `SkillsDir()` 不可解析时，系统必须跳过 bootstrap 且不报错。
- FR-11: 新增技能集必须只需追加 manifest 条目 + 嵌入文件，无需改动首次运行判定或安装核心逻辑。

## Non-Goals (Out of Scope)

- 不通过 `npx skills add` / `gh` / 网络安装（本期纯离线 embed）。
- 不在 headless（`-p`）模式下弹出任何交互；如需在 headless 首次运行也 bootstrap，可静默执行但不打印，本期默认仅在交互式 REPL 触发。
- 不接管或改动现有 `pigo install/uninstall/update` 的 npm 安装路径。
- 不提供内置技能的自动升级/覆盖用户已修改技能（只在版本落后且用户未修改时补装缺失项，覆盖策略见 FR-6：已存在即跳过）。
- 不内置 `humanize-it`（用户明确排除）。
- 不提供 UI/配置项让用户勾选安装哪些内置技能（5A 只要求机制可扩展，不含用户可配置界面）。

## Technical Considerations

- 复用已有基础设施：
  - `pkgmgr.SkillsDir()`（`internal/pkgmgr/layout.go`）解析 skills 目录，含 `PIGO_SKILLS_DIR` 覆盖。
  - `pkgmgr.Home()` 解析 `$PIGO_HOME`（默认 `~/.pigo`）作为 sentinel 存放位置。
  - `DistributeSkill` 的复制模式（`copyTree`）可参考；但内置安装是"已存在则跳过"，与 `DistributeSkill` 的"先 RemoveAll 再覆盖"语义不同，需独立实现或加参数区分。
  - `loadSkillCommands()` / `buildSlashRegistry`（`cmd/pigo/run.go`、`skills_test.go`）负责把 skills 目录暴露为 `/skill-name`，bootstrap 必须在其之前执行。
- embed 参考现有做法：`internal/pihost/embed.go` 的 `//go:embed pihost.mjs`。技能是目录树，需 `//go:embed all:skills/*` 之类的模式。
- 技能文件从 `smallnest/goal-workflow` 获取并放入 `internal/builtinskills/skills/<name>/`，随源码提交。
- sentinel 建议记录清单版本（如 `goal-workflow@<hash或日期>`），便于将来内置技能更新时触发补装。
- 注意 `--no-skills` 语义：若用户禁用 skill 发现，bootstrap 是否仍落地文件需在实现时明确（建议：`--no-skills` 时跳过 bootstrap，保持"不加载即不安装"的一致预期）。

## Success Metrics

- 全新环境（空 `~/.pigo` 与空 `~/.agents/skills`）首次运行 pigo 后，`/prd`、`/refactor`、`/architecture-diagram`、`/weather` 等 16 个命令可直接调用。
- 首次运行到 REPL 就绪无额外可见输出、无交互阻塞。
- 断网环境下首次运行安装成功率 100%（纯离线）。
- 二次运行不重复安装（sentinel 生效），启动无额外耗时。

## Open Questions

- sentinel 版本策略：用固定版本字符串，还是对嵌入内容做 hash？（影响将来"内置技能更新后是否补装"）
- `--no-skills` 下是否仍落地文件？（建议跳过，待确认）
- headless 首次运行是否也 bootstrap（静默）？本期默认否，是否需要开关？
- 内置技能后续更新时，对用户已修改的同名技能如何处理？（本期一律跳过，长期是否需要"检测未修改则更新"）

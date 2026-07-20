# PRD: 安装 pi Agent 包（pigo install）

## Introduction

pi 生态在 npm 上发布了大量 agent 扩展包（catalog: https://pi.dev/packages），涵盖 **extension（可执行扩展/插件）、skill（技能）、prompt（提示词/命令模板）、theme（主题）** 四类，其中不少扩展本质是 MCP adapter（如 `pi-mcp-adapter`）。pi 用户通过 `pi install npm:<package>` 一行命令即可安装。

pigo 是 pi agent 的 Go 复刻版，已具备自己的 **plugin 系统**（`$PIGO_HOME/plugins` 下的可执行文件，走 JSON-RPC）、**命令系统**（`$PIGO_HOME/commands/*.md`）和 **skill 系统**（`~/.agents/skills`）。目前用户无法直接复用 pi 生态里的现成包。

本功能给 pigo 增加一个 `pigo install` 子命令，从 npm 拉取 pi 包，识别其类型，并把内容分发到 pigo 对应的目录，使其被现有的发现机制自动加载。同时提供 `list` 与 `uninstall` 管理已安装的包。

**读者假设**：面向初级开发者或 AI agent，涉及的概念（npm 包结构、pi 包类型、pigo 目录约定）首次出现时给出解释。

**关键决策（来自澄清问答）**：
- **获取方式（1B）**：调用本机已安装的 `npm`（`npm pack` / `npm install`）拉取包，不自己实现 npm registry 客户端。未检测到 npm 时给出清晰报错与安装指引。
- **包类型（2A）**：尽力映射全部 pi 包类型到 pigo 对应机制。
- **命令范围（3C）**：`install` + `list` + `uninstall` + `update`。
- **来源前缀（4A）**：仅 `npm:<name>`，含 scoped 形式 `npm:@scope/name`。
- **安装位置（5B）**：按类型分发到各自专用目录（`plugins/`、`commands/`、skills 目录），而非集中目录。

## Goals

- 用户可通过 `pigo install npm:<package>` 一行命令安装 pi 生态的 npm 包。
- 安装时自动识别包类型并分发到 pigo 对应目录，安装后无需额外配置即可被发现机制加载。
- 支持 `list` 查看已安装包、`uninstall` 卸载、`update` 更新到最新版本。
- 维护一份已安装包的清单（lockfile），记录包名、版本、类型、安装的文件，使 `list`/`uninstall`/`update` 可靠工作。
- 依赖本机 `npm`；缺失时报错清晰，不静默失败。
- 全流程 `go build ./... && go vet ./... && go test ./...` 全绿。

## pi 包类型 → pigo 目标目录映射

| pi 包类型 | 识别依据（package.json 字段/约定） | pigo 目标目录 | 说明 |
|-----------|-----------------------------------|--------------|------|
| extension（含 MCP adapter） | 声明可执行入口（`bin`/`pi.extension`） | `$PIGO_HOME/plugins/` | 落地为可执行文件，走现有 JSON-RPC 插件协议 |
| skill | 含 `SKILL.md`/skill 元数据（`pi.skill`） | skills 目录（`~/.agents/skills` 或 `PIGO_SKILLS_DIR`） | 复用现有 skill 加载 |
| prompt / command | 含命令模板（`pi.prompt` / `commands/*.md`） | `$PIGO_HOME/commands/` | 复用现有命令加载 |
| theme | `pi.theme` | `$PIGO_HOME/themes/`（暂存） | [Assumption] pigo 尚无主题系统，先落盘保存，运行时消费留待后续（见 Open Questions） |

> 类型识别以包 `package.json` 中的 pi 元数据字段为准；一个包可同时声明多种类型（catalog 中存在 `extensionskill` 组合），按声明的每种类型分别分发。识别不出任何已知类型时，安装失败并提示。

## User Stories

### US-001: 定义包清单（lockfile）与安装目录约定
**Description:** 作为开发者，我需要一份记录已安装包的清单文件，以便 `list`/`uninstall`/`update` 能准确知道装了什么、装了哪些文件。

**Acceptance Criteria:**
- [ ] 在 `$PIGO_HOME/packages.json`（或等价 lockfile）中记录每个已安装包：`name`、`source`（如 `npm:pi-mcp-adapter`）、`version`、`type[]`、安装落地的文件路径列表
- [ ] 定义并实现读写该 lockfile 的函数（不存在时视为空清单，不报错）
- [ ] lockfile 为格式化 JSON，可人工阅读
- [ ] 该模块有单元测试覆盖读、写、空文件、损坏文件容错
- [ ] Typecheck/lint passes（`go build ./... && go vet ./...`）

### US-002: 解析并校验 `npm:<name>` 包引用
**Description:** 作为用户，我输入 `npm:pi-mcp-adapter` 或 `npm:@scope/name`，系统需正确解析出 registry 与包名。

**Acceptance Criteria:**
- [ ] 输入 `npm:pi-mcp-adapter` 解析为 `{registry: npm, name: pi-mcp-adapter}`
- [ ] 输入 `npm:@scope/name` 正确解析 scoped 包名
- [ ] 缺少 `npm:` 前缀或前缀不受支持时，返回明确错误：`unsupported package source, expected npm:<name>`
- [ ] 非法 npm 包名（含空格/非法字符）被拒绝并报错
- [ ] 解析逻辑有单元测试覆盖上述各分支
- [ ] Typecheck/lint passes

### US-003: 检测本机 npm 并从 npm 拉取包内容
**Description:** 作为用户，我希望 `pigo install` 用本机 npm 把包下载并解包到临时目录，供后续分发。

**Acceptance Criteria:**
- [ ] 安装前检测 `npm` 是否在 PATH；缺失时报错 `npm not found; install Node.js/npm to use pigo install` 并退出非零
- [ ] 通过 npm（如 `npm pack <name>` 后解包，或 `npm install` 到临时目录）获取包内容到一个临时目录
- [ ] 支持指定版本（`npm:name@1.2.3`）；未指定时取最新版
- [ ] npm 命令失败（包不存在、网络错误）时，透传 npm 的错误信息并退出非零，不留下半安装状态
- [ ] 拉取结束后清理临时目录
- [ ] Typecheck/lint passes

### US-004: 识别 pi 包类型
**Description:** 作为系统，我需要读取包内 `package.json` 的 pi 元数据，判断这是 extension/skill/prompt/theme 中的哪一种（或多种）。

**Acceptance Criteria:**
- [ ] 读取包 `package.json`，根据映射表列出的字段/约定判定类型，返回类型集合
- [ ] 一个包声明多种类型时，返回全部类型
- [ ] 无法识别任何已知 pi 类型时，返回错误 `unrecognized pi package: no known pi metadata`
- [ ] 识别逻辑有单元测试，覆盖单类型、多类型（如 extension+skill）、未知类型三种输入
- [ ] Typecheck/lint passes

### US-005: 分发 extension/MCP-adapter 类型到 plugins 目录
**Description:** 作为用户，安装一个 extension（如 MCP adapter）后，它应作为 pigo 插件被自动发现。

**Acceptance Criteria:**
- [ ] extension 包的可执行入口被落地到 `$PIGO_HOME/plugins/`，且带可执行权限（chmod +x）
- [ ] 落地的文件路径写入 US-001 的 lockfile
- [ ] 安装后启动 pigo，该插件出现在已加载插件中（现有 `plugin.Discover` 能发现它）
- [ ] 若包声明的入口需运行时（如 node 脚本），安装时验证运行时存在或在 lockfile 记录其运行方式（见 Open Questions）
- [ ] 有测试验证：给定一个 mock extension 包，分发后文件出现在 plugins 目录且可执行位已设置
- [ ] Typecheck/lint passes

### US-006: 分发 skill 类型到 skills 目录
**Description:** 作为用户，安装一个 skill 包后，它应被 pigo 的 skill 加载机制发现。

**Acceptance Criteria:**
- [ ] skill 包内容被落地到 skills 目录（`~/.agents/skills/<name>/`，遵循 `PIGO_SKILLS_DIR` 覆盖）
- [ ] 落地文件路径写入 lockfile
- [ ] 安装后 skill 以 `/skill-name` 形式出现在可用命令中（现有 skill 加载能发现它）
- [ ] 有测试验证 mock skill 包分发后出现在 skills 目录
- [ ] Typecheck/lint passes

### US-007: 分发 prompt/command 类型到 commands 目录
**Description:** 作为用户，安装一个 prompt/command 包后，它应作为斜杠命令可用。

**Acceptance Criteria:**
- [ ] command 模板被落地到 `$PIGO_HOME/commands/`（遵循现有 `commands/*.md` 约定）
- [ ] 落地文件路径写入 lockfile
- [ ] 与内置命令重名时，沿用现有「用户命令被内置命令 shadow」的行为并在安装时警告
- [ ] 有测试验证 mock prompt 包分发后出现在 commands 目录
- [ ] Typecheck/lint passes

### US-008: 分发 theme 类型（暂存落盘）
**Description:** 作为用户，安装 theme 包不应报错；即便 pigo 暂无主题系统，也先把主题文件保存下来。

**Acceptance Criteria:**
- [ ] theme 包内容落地到 `$PIGO_HOME/themes/<name>/`
- [ ] 落地文件路径写入 lockfile
- [ ] 安装成功输出提示：`theme installed but not yet consumed by pigo runtime`（`[Assumption]`，见 Open Questions）
- [ ] 有测试验证 mock theme 包分发后出现在 themes 目录
- [ ] Typecheck/lint passes

### US-009: `pigo install` 端到端命令
**Description:** 作为用户，我运行 `pigo install npm:pi-mcp-adapter` 即可完成解析→拉取→识别→分发→写清单的全流程。

**Acceptance Criteria:**
- [ ] `pigo install npm:<name>` 串联 US-002~US-008，成功后打印安装摘要（包名、版本、类型、落地目录）
- [ ] 任一步骤失败时报错清晰、退出非零，且不写入部分 lockfile 记录（要么全成功要么不留记录）
- [ ] 重复安装同一包时，先卸载旧版本文件再装新的（幂等，不残留旧文件）
- [ ] 有集成测试用 mock npm（或桩）验证一个 extension 包端到端安装
- [ ] Typecheck/lint passes

### US-010: `pigo list` 列出已安装包
**Description:** 作为用户，我想查看已安装了哪些 pi 包及其版本和类型。

**Acceptance Criteria:**
- [ ] `pigo list` 读取 lockfile，逐行打印 `name  version  type(s)  source`
- [ ] 无已安装包时打印 `no packages installed`
- [ ] 有单元测试覆盖有包/无包两种输出
- [ ] Typecheck/lint passes

### US-011: `pigo uninstall` 卸载包
**Description:** 作为用户，我想卸载一个已安装的包并清理其落地文件。

**Acceptance Criteria:**
- [ ] `pigo uninstall <name>` 删除该包在 lockfile 中记录的全部落地文件，并从 lockfile 移除该条目
- [ ] 卸载未安装的包时报错 `package not installed: <name>` 并退出非零
- [ ] 删除文件时若某文件已不存在，跳过并继续（不因残缺而中断），最终 lockfile 条目被移除
- [ ] 有测试验证安装后 uninstall 能移除文件与 lockfile 条目
- [ ] Typecheck/lint passes

### US-012: `pigo update` 更新包到最新版本
**Description:** 作为用户，我想把已安装的包更新到 npm 上的最新版本。

**Acceptance Criteria:**
- [ ] `pigo update <name>` 对指定包执行「卸载旧文件 → 按记录的 source 重新拉取最新版 → 重新分发 → 更新 lockfile 版本」
- [ ] `pigo update`（不带包名）遍历更新 lockfile 中全部包
- [ ] 已是最新版时打印 `<name> is up to date`，不做无谓改动
- [ ] 更新失败时保留原安装（不留下损坏状态）
- [ ] 有测试验证 update 后 lockfile 版本被更新
- [ ] Typecheck/lint passes

## Functional Requirements

- FR-1: 系统必须提供 `pigo install <source>` 子命令。
- FR-2: 系统必须仅接受 `npm:<name>` 形式的 source（含 `npm:@scope/name` 与 `npm:name@version`），其它前缀必须报错。
- FR-3: 系统在安装前必须检测本机 `npm` 是否可用，不可用时必须报错并退出非零。
- FR-4: 系统必须通过本机 npm 将包内容获取到临时目录，完成后必须清理临时目录。
- FR-5: 系统必须读取包 `package.json` 的 pi 元数据以判定包类型（可为多类型）。
- FR-6: 系统必须把 extension（含 MCP adapter）类型落地到 `$PIGO_HOME/plugins/` 并设置可执行权限。
- FR-7: 系统必须把 skill 类型落地到 skills 目录（遵循 `PIGO_SKILLS_DIR` 覆盖）。
- FR-8: 系统必须把 prompt/command 类型落地到 `$PIGO_HOME/commands/`。
- FR-9: 系统必须把 theme 类型落地到 `$PIGO_HOME/themes/` 并提示暂未被运行时消费。
- FR-10: 系统必须在 `$PIGO_HOME/packages.json` 中记录每个已安装包的 name、source、version、type[]、落地文件列表。
- FR-11: 系统必须提供 `pigo list` 列出 lockfile 中的已安装包。
- FR-12: 系统必须提供 `pigo uninstall <name>` 删除包的落地文件并移除 lockfile 条目。
- FR-13: 系统必须提供 `pigo update [name]` 将指定包（或全部包）更新到最新版本。
- FR-14: 系统在安装/更新任一步骤失败时必须不留下部分 lockfile 记录（原子性）。
- FR-15: 重复安装同名包时，系统必须先清理旧落地文件再安装新版本（幂等）。

## Non-Goals (Out of Scope)

- 不实现自研 npm registry 客户端；完全依赖本机 npm。
- 不支持 `github:`、`file:` 等 npm 以外的来源前缀（本期仅 `npm:`）。
- 不实现 pigo 的主题运行时系统；theme 包仅落盘暂存。
- 不实现 MCP 协议客户端本身；MCP adapter 作为普通 extension 走现有插件 JSON-RPC 协议（adapter 自身负责桥接 MCP）。
- 不做包的依赖解析/传递依赖管理（交由 npm 处理）。
- 不做包签名校验、安全沙箱审计（见 Open Questions 中的安全考量）。
- 不提供 GUI/交互式包浏览；仅命令行。

## Technical Considerations

- 复用现有发现机制：`plugin.Discover($PIGO_HOME/plugins)`、skills 加载（`~/.agents/skills` / `PIGO_SKILLS_DIR`）、命令加载（`$PIGO_HOME/commands/*.md`）。分发只需把文件放对目录，无需改动加载器。
- `$PIGO_HOME` 解析沿用现有约定（`PIGO_HOME` 环境变量，默认 `~/.pigo`）。
- 调用 npm 时用非交互方式（避免交互式提示阻塞），透传 stderr 以便诊断。
- lockfile 与安装模块建议置于新 package（如 `internal/pkgmgr`），CLI 子命令置于 `cmd/pigo`。
- 安全提示：安装第三方可执行 extension 会在本机运行外部代码，属于中高风险动作；安装 extension 时应向用户提示来源与风险（可结合已有的 Project Trust 机制，见近期提交 US-018）。

## Success Metrics

- 用户可用单条 `pigo install npm:pi-mcp-adapter` 完成安装，安装后重启 pigo 即可发现该插件（0 额外手动步骤）。
- `list`/`uninstall`/`update` 对 lockfile 的操作 100% 一致（uninstall 后 list 不再显示该包，磁盘无残留记录文件）。
- 安装失败场景（无 npm、包不存在、类型无法识别）均给出可操作的明确报错，无半安装残留。

## Open Questions

- extension 若是 node 脚本入口（非原生可执行），pigo 插件协议要求可执行文件——需确定是落地一个调用 `node xxx.js` 的 wrapper 脚本，还是要求包提供原生可执行？（影响 US-005）
- theme 包落盘后，是否在本期就规划一个最小主题消费点，还是完全留待后续独立 PRD？
- 是否需要把安装动作纳入 Project Trust 信任确认流程（安装即运行第三方代码的风险）？
- pi 包元数据的确切字段（`pi.extension` / `pi.skill` / `pi.prompt` / `pi.theme`）需以 pi 官方包文档为准核对——catalog 页指向 GitHub 上的 package docs，实现前需确认真实字段名与结构。
- 版本升级时若包类型发生变化（旧版是 skill，新版新增 extension），分发与清理如何处理？

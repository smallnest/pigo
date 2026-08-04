# PRD: 工具白名单 / 黑名单 CLI 参数（--allowed-tools / --disallowed-tools）

## 1. Introduction/Overview

pigo 目前对"模型能用哪些工具"只有一个**全有或全无**的开关：`--no-tools`（`cmd/pigo/main.go:166`）。要么全部内置工具可用，要么一个都不给。中间态完全缺失——用户没法说"这次只准读文件，别碰 bash"，也没法说"随便用，就是别写文件"。

对标 Claude Code，它提供 `--allowedTools` / `--disallowedTools` 做工具级准入控制。pigo 侧其实已经有现成的地基：

- `internal/runtime/skills.go:37` 的 `AllowedTools stringList`（技能 frontmatter 的 `allowed-tools`）
- `internal/runtime/skills.go:357` 的 `filterToolsByName(tools, allow)` 过滤器
- `internal/runtime/skills.go:96` 的 `stringList.UnmarshalYAML`——已能同时吃 YAML 列表和 `A, B` 标量

但这套机制**只作用于技能派生出的子 Agent**（`skills.go:255`），管不到主会话。本特性把同等能力提到 CLI 层，让主会话也能被约束。

本 PRD 还要解决一个当前存在的**假安全感**风险：`--approve`（`main.go:170`）会免掉副作用工具的逐次确认。如果白名单只是"软提示"、被 `-a` 一把绕过，那用户以为限住了、其实没限住，比不做更危险。所以本特性明确把白名单定义为**硬边界**。

读者假设为初级开发者或 AI agent，术语尽量展开。

## 2. Goals

- 提供工具级准入控制：`--allowed-tools`（白名单）与 `--disallowed-tools`（黑名单），补齐 `--no-tools` 之外的中间态。
- 黑名单优先于白名单：同一工具同时出现在两侧时，**拒绝**胜出（fail-closed）。
- 工具名匹配**大小写不敏感**，让 Claude Code 习惯的 `Read` / `Bash` 写法能直接命中 pigo 的 `read` / `bash`。
- 白名单是**硬边界**：`--approve` 只能免掉确认弹窗，不能让边界外的工具运行。
- 拼错工具名**立即报错退出**，绝不静默忽略——静默忽略会制造"我以为限制住了"的错觉。
- 复用现有 `filterToolsByName`，不新造平行机制。
- 支持 `config.toml` 声明默认边界，遵循既有 CLI > 文件 > 默认 的精度覆盖。

## 3. User Stories

### US-001: 注册两个 CLI 参数并解析多种输入形式
**Description:** As a pigo 用户, I want 用 `--allowed-tools` / `--disallowed-tools` 在命令行声明工具边界 so that 我能在不改配置文件的前提下按次收紧本次运行的权限。

**Acceptance Criteria:**
- [ ] `cliOptions` 新增 `allowedTools []string` 与 `disallowedTools []string` 两个字段，带说明其语义与优先级的注释
- [ ] 用 `flag.StringArrayVar` 注册 `--allowed-tools` 与 `--disallowed-tools`（无短选项，避免与现有 `-a`/`-n` 冲突）
- [ ] 参数**可重复**：`--allowed-tools read --allowed-tools grep` 等价于两项
- [ ] 单个参数值支持**逗号分隔**：`--allowed-tools "read,grep"` 解析为两项
- [ ] 逗号切分后逐项 `TrimSpace`，空串项被丢弃：`--allowed-tools "read, ,grep"` 得到 `[read grep]`
- [ ] `pigo --help` 中两个参数各有一行说明，写明"黑名单优先"
- [ ] 单元测试覆盖：单值、重复传参、逗号形式、混合形式、含空白与空项
- [ ] `go build ./...` 与 `go test ./cmd/...` 通过

### US-002: 工具名规范化与未知名校验
**Description:** As a pigo 用户, I want 写 `Read` 也能命中内置的 `read`、写错名字时立刻收到报错 so that 我不会因为大小写或拼写问题得到一个"看起来限制住了、其实没限住"的会话。

**Acceptance Criteria:**
- [ ] 新增纯函数完成规范化：对输入工具名做 `TrimSpace` + `strings.ToLower`
- [ ] 校验发生在**工具集组装完成之后**（此时才知道全量可用工具名，含 MCP / 子 Agent / 插件工具）
- [ ] 任一 `--allowed-tools` / `--disallowed-tools` 项不在可用工具名集合中时，向 stderr 打印 `pigo: --allowed-tools: unknown tool "xxx" (available: ...)` 并 `os.Exit(2)`
- [ ] 报错信息列出全部可用工具名，便于用户自我纠正
- [ ] 多个未知名时一次性全部列出，而非报第一个就退出
- [ ] 单元测试覆盖：大小写变体命中、未知名报错、多个未知名合并报错、空列表不校验
- [ ] Typecheck/lint 通过

### US-003: 工具集过滤（黑名单优先）
**Description:** As a pigo 用户, I want 声明的边界真正作用到交给模型的工具清单上 so that 模型连边界外工具的存在都感知不到。

**Acceptance Criteria:**
- [ ] 新增纯函数 `ApplyToolPolicy(tools []agentcore.AgentTool, allow, deny []string) []agentcore.AgentTool`，位于工具组装所在包
- [ ] `allow` 非空时只保留名字（规范化后）在 `allow` 中的工具；`allow` 为空表示"不限制"
- [ ] `deny` 非空时移除名字在 `deny` 中的工具，**且在 allow 之后执行**——同名同时出现在两侧时工具被移除
- [ ] 两者都为空时返回原切片，行为与当前完全一致（零回归）
- [ ] 与 `--no-tools` 组合：`--no-tools` 仍是全局优先，工具集为空时本过滤是 no-op
- [ ] 过滤后工具集为空时向 stderr 打印一行警告（不退出），提示模型将无工具可用
- [ ] 单元测试覆盖：纯白名单、纯黑名单、两者交叠、两者都空、过滤到空集
- [ ] Typecheck/lint 通过

### US-004: 接入主会话的三条运行路径
**Description:** As a pigo 用户, I want 无论用 TUI、REPL 还是无头 `-p` 模式，边界都同样生效 so that 我不会因为换了个前端就意外获得更大权限。

**Acceptance Criteria:**
- [ ] `allowedTools` / `disallowedTools` 从 `cliOptions` 透传进 `dispatch`，再进入三条路径共用的工具组装点
- [ ] TUI 路径生效：会话构建时的工具注册表已被过滤
- [ ] REPL 路径（`--no-tui` 或非 TTY）生效
- [ ] 无头 `-p` 路径生效
- [ ] 三条路径共用同一个过滤调用点（不复制粘贴三份逻辑）
- [ ] 集成测试或表驱动测试断言：三条路径在相同参数下得到相同的工具名集合
- [ ] Typecheck/lint 通过

### US-005: `read` 被屏蔽时不注入 `<available_skills>`
**Description:** As a pigo 用户, I want 屏蔽 `read` 后系统提示里不再宣传技能 so that 模型不会去调用它根本加载不了的技能。

**Acceptance Criteria:**
- [ ] 系统提示构建时的 `ReadToolAvailable` 判定（`internal/runtime/prompt.go:61`）基于**过滤后**的工具集，而非过滤前
- [ ] `--disallowed-tools read` 时 `<available_skills>` 块不出现在系统提示中
- [ ] `--allowed-tools bash`（不含 read）时同样不出现
- [ ] 现有测试 `TestBuildSystemPromptInjectsSkills`（`internal/runtime/prompt_test.go:230`）仍通过
- [ ] 新增测试：过滤掉 read 后断言提示中无 `available_skills`
- [ ] Typecheck/lint 通过

### US-006: 白名单是硬边界，`--approve` 不得绕过
**Description:** As a pigo 用户, I want `--approve` 只免掉确认弹窗、不能放行边界外的工具 so that 我的白名单是真实的安全边界而不是心理安慰。

**Acceptance Criteria:**
- [ ] `--allowed-tools read --approve` 时 `bash` / `write` / `edit` 均不在工具集中，模型无法调用
- [ ] `--disallowed-tools bash --approve` 时 `bash` 不在工具集中
- [ ] 边界内的副作用工具（如 `--allowed-tools write --approve`）仍按 `--approve` 语义免逐次确认——两个机制正交，互不削弱
- [ ] 因为过滤发生在工具注册层、早于 `BeforeToolCall` 确认门（`internal/agenttool/tool_executor.go:109`），所以"绕过"在架构上不可能，而非靠运行时检查兜住
- [ ] 测试断言上述三种组合的最终工具名集合
- [ ] README 明确写出这条优先级关系
- [ ] Typecheck/lint 通过

### US-007: config.toml 支持默认边界
**Description:** As a pigo 用户, I want 在 `config.toml` 里声明常用边界 so that 我不必每次敲一长串命令行参数。

**Acceptance Criteria:**
- [ ] `FileConfig`（`internal/cli/config/config.go:30`）新增 `AllowedTools []string \`toml:"allowed_tools"\`` 与 `DisallowedTools []string \`toml:"disallowed_tools"\``
- [ ] `applyFileConfig`（`cmd/pigo/main.go` 内）遵循既有精度覆盖：仅当对应 flag 未被显式传入（`changed("allowed-tools")` 为 false）时才用文件值
- [ ] CLI 传入时**整体替换**文件值，而非与文件值合并——合并语义会让"我想放宽"变成"放不宽"
- [ ] 配置缺失（零值）时行为与当前完全一致
- [ ] `config.toml.example` 补上两个键的注释示例，说明黑名单优先
- [ ] 单元测试参照 `TestApplyFileConfig_FillsUnsetFlags` / `TestApplyFileConfig_CLIWins`（`cmd/pigo/main_test.go:179,225`）的模式，覆盖填充与 CLI 优先两种情形
- [ ] Typecheck/lint 通过

### US-008: 文档更新
**Description:** As a pigo 用户, I want 在 README 和文档站查到这两个参数的准确语义 so that 我不必读源码去猜黑白名单谁优先。

**Acceptance Criteria:**
- [ ] README CLI 参数表（`README.md:155-171` 区域）新增两行
- [ ] README「内置工具」章节（`README.md:260`）说明可用这两个参数做工具级准入，并列出全部合法工具名
- [ ] README「项目信任」章节补一句：白名单是硬边界，`--approve` 只免确认、不放行边界外工具
- [ ] `docs/web/features.html` 的参数说明同步新增两项
- [ ] `CHANGELOG.md` 的 `## [Unreleased]` → `### Added` 补一条
- [ ] 文档中明确声明**本期不支持** `Bash(git log:*)` 这类参数级匹配，写成该形式会被当作未知工具名报错

## 4. Functional Requirements

- FR-1: 系统必须提供 `--allowed-tools` 参数，接受工具名列表，声明模型可用工具的白名单。
- FR-2: 系统必须提供 `--disallowed-tools` 参数，接受工具名列表，声明模型禁用工具的黑名单。
- FR-3: 两个参数必须支持重复传入，多次传入的值累加。
- FR-4: 两个参数的单个值必须支持逗号分隔，切分后逐项去除首尾空白，丢弃空项。
- FR-5: 系统必须在匹配工具名时忽略大小写（输入与工具 `Name()` 均转小写后比较）。
- FR-6: 当白名单非空时，系统必须只把名字命中白名单的工具交给模型。
- FR-7: 当黑名单非空时，系统必须从工具集中移除名字命中黑名单的工具。
- FR-8: 黑名单必须在白名单之后生效，使同名同时出现在两侧时该工具被移除。
- FR-9: 当任一参数包含不存在的工具名时，系统必须向 stderr 报错并以退出码 2 终止。
- FR-10: 报错信息必须一次性列出全部未知名，并附上当前全部可用工具名。
- FR-11: 当两个参数均为空时，系统行为必须与本特性引入前完全一致。
- FR-12: 过滤后工具集为空时，系统必须向 stderr 打印警告，但不终止运行。
- FR-13: 过滤必须发生在工具注册层，早于 `BeforeToolCall` 确认门，使 `--approve` 在架构上无法放行边界外的工具。
- FR-14: 过滤必须同等作用于 TUI、REPL、无头 `-p` 三条运行路径，且三者共用同一调用点。
- FR-15: 系统提示的 `ReadToolAvailable` 判定必须基于过滤后的工具集，使 `read` 被屏蔽时不注入 `<available_skills>`。
- FR-16: `FileConfig` 必须新增 `allowed_tools` 与 `disallowed_tools` 两个 TOML 键。
- FR-17: 当对应命令行参数未被显式传入时，系统必须采用配置文件中的值；显式传入时命令行值整体替换文件值。
- FR-18: 校验必须在工具集组装完成后执行，使 MCP / 子 Agent / 插件工具的名字也被纳入合法名集合。

## 5. Non-Goals (Out of Scope)

- **不支持参数级细粒度匹配**：`Bash(git log:*)`、`Read(src/**)` 这类 Claude Code 语法本期不做。它需要一套独立的参数匹配器，并且必须在 `BeforeToolCall` 层拦截调用参数而非在注册层过滤工具，是另一个量级的工程。本期写成该形式一律按未知工具名报错。
- **不引入 `--permission-mode`**：`default` / `acceptEdits` / `bypassPermissions` / `plan` 这套模式概念不做，`--approve` 保持现状。
- **不做环境变量入口**：不引入 `PIGO_ALLOWED_TOOLS`。
- **不做 TUI 运行时切换**：不新增 `/tools` 斜杠命令，边界在进程启动时确定、整个会话不变。
- **不改动技能层的 `allowed-tools`**：`internal/runtime/skills.go` 的技能 frontmatter 过滤保持原样，两者独立。本期不定义"CLI 边界与技能子 Agent 边界如何叠加"，因为子 Agent 从父工具集派生，天然继承 CLI 边界即可。
- **不改动 hooks 机制**：`PreToolUse` hook 的拦截能力与本特性正交，不做整合。
- **不改动 `SideEffectTools` 分类**：副作用工具的判定表保持原样。
- **不做持久化的项目级边界**：不引入 `.pigo/` 项目层的工具边界配置，只做全局 `config.toml`。

## 6. Design Considerations

无 UI 变更，纯 CLI 与配置层。

复用点：
- `internal/runtime/skills.go:357` 的 `filterToolsByName` 已实现"空列表即不限制"的白名单语义，`ApplyToolPolicy` 的 allow 分支可直接复用其逻辑或以其为原型（注意它当前是大小写敏感的，本期需要不敏感版本）。
- `internal/runtime/skills.go:96` 的 `stringList.UnmarshalYAML` 已实现"标量按逗号切分"的容错，FR-4 的 CLI 侧切分逻辑与它形状相同，可考虑提取共用的 `splitAndTrim` 辅助函数避免两处实现漂移。
- 错误退出遵循 `--cwd` 的既有模式（`cmd/pigo/main.go:216-220`）：向 stderr 打印 `pigo: <flag>: <reason>` 后 `os.Exit(2)`。

## 7. Technical Considerations

**校验时序是关键约束。** 工具名合法性校验必须在工具集组装完成之后，因为合法名集合包含运行时才确定的工具（MCP 工具、子 Agent 工具、插件工具）。若在 `flag.Parse()` 之后立刻校验，会把合法的插件工具名误报为未知。这意味着 `os.Exit(2)` 的位置不在 `main`，而在三条路径共用的组装点之后——需要一个能把校验错误上抛到 `dispatch` 返回码的路径。

**当前全部内置工具名**（供 FR-10 报错文案与文档使用）：

`read`、`write`、`edit`、`grep`、`find`、`ls`、`bash`、`bash_output`、`kill_bash`、`todo`、`webfetch`、`websearch`、`memory_search`、`goal_complete`、`goal_blocked`

注意 README 现有的工具表（`README.md:264-274`）只列了 9 个，缺 `ls`、`bash_output`、`kill_bash`、`memory_search`，以及仅在 goal 模式注册的 `goal_complete` / `goal_blocked`。US-008 补文档时需要处理这个既存缺口，并明确 goal 类工具是否属于可被边界约束的范围。

**`--no-tools` 的交互。** `--no-tools` 使内置工具集为空（`internal/cli/run/run.go:67,77`），此时过滤是 no-op。但插件工具在 `--no-tools` 下也被跳过，所以两者组合的最终结果是空集，FR-12 的警告会触发——这是符合预期的，不应该额外特判。

**已有的 `BuiltinToolsExcept`**（`internal/cli/run/run.go`，紧跟 `BuiltinTools`）是一个编译期调用方使用的排除辅助函数。它与本特性目标接近但入口不同（非用户可配）。实现时应评估是否能让 `ApplyToolPolicy` 与它归一，避免仓库里存在两套语义相近的过滤器。

## 8. Success Metrics

- `pigo --allowed-tools read -p "..."` 后模型的可用工具清单恰好为 `[read]`，可通过 `--output-format stream-json` 的事件流或调试输出核验。
- `pigo --allowed-tools read,bash --disallowed-tools bash -p "..."` 后 `bash` 不可用（黑名单优先生效）。
- `pigo --allowed-tools raed -p "..."`（拼错）退出码为 2，stderr 含 `unknown tool "raed"` 与可用名列表。
- 不传任何新参数时，`go test ./...` 全绿，无行为回归。
- `--allowed-tools read --approve` 下 `bash` / `write` / `edit` 均无法被调用。

## 9. Open Questions

以下三条在实现中已解决，保留结论备查：

- **`goal_complete` / `goal_blocked` 是否可被屏蔽？** 已解决：**不可**，且无需特判。这两个工具由 `goalToolRegistry`（`internal/cli/goal/goal.go:274`）在 goal 运行时叠加到会话注册表之上，从不进入 `Env.Tools`。校验只认 `Env.Tools` 里的名字，所以 `--disallowed-tools goal_complete` 会得到"unknown tool"报错——恰好就是期望行为，goal 模式的收敛路径无法被用户误伤。
- **子 Agent 是否继承边界？** 已解决：**继承**。`ChildToolSet`（`internal/cli/run/toolpolicy.go`）把策略应用到子工具集上，堵住了"让子 Agent 去跑 bash"这条逃逸路径。`TestChildToolSetInheritsPolicy` 锁死该行为。
- **过滤后为空是否按 `--no-tools` 处理？** 已解决：**不特判**，只打警告。`hasReadTool` 自然返回 false，`<available_skills>` 自然不注入，无需额外分支。

仍开放：

- 是否需要 `pigo --list-tools` 之类的只读查询，让用户在不发起对话的前提下确认当前生效的工具集？本期未做。当前的验证方式是拼错一个名字，从报错信息里读出全部可用工具名——可用但迂回。
- 进程隔离子 Agent（`--subagent-rpc`）当前从 RPC 的 `tools` 名单重建工具集，且 `run.BuiltinTools(cwd, false)` 不读 `--no-tools`。该模式在生产中未被任何代码路径启用（`SubAgentIsolationProcess` 无非测试引用），故本期未接入策略；若将来启用，需要把策略一并透传过 RPC 边界。




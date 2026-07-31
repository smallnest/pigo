# PRD: pigo 自更新（pigo update 无参数）

## Introduction

为 pigo CLI 增加自更新能力，让用户无需重新跑安装脚本即可升级到最新版本。

复用现有 `update` 子命令：`pigo update`（**无位置参数**）表示"更新 pigo 自身"，而 `pigo update <包名>` 保持现有的包管理更新语义不变。自更新采用**内置自替换**机制——从 GitHub Release 下载当前平台归档、校验 checksum、原子替换正在运行的二进制，无外部依赖、跨平台一致。

此外，pigo 交互式启动时会在欢迎横幅（banner）中展示当前版本号，并检查是否有新版本；若存在更新，则**高亮显示新版本号**并提示用户运行 `pigo update` 升级。

面向读者可能是初级开发者或 AI agent，因此下文避免省略关键约束。

## Goals

- `pigo update`（无参数）能把本地 pigo 升级到 GitHub 最新 release
- 不破坏现有 `pigo update <包名>` 的包更新语义
- 下载后校验 checksum 再替换，保证完整性与可回滚
- 原子替换当前运行的二进制，替换失败不留下损坏文件
- 启动横幅展示当前版本号，并在有更新时高亮新版本、提示升级，且不阻塞启动
- 开发版（`version == "dev"`）与非 release 构建下给出明确、无害的行为

## User Stories

### US-001: 版本比较与 release 查询模块
**Description:** As a developer, I need a module that queries the latest GitHub release and compares it against the current build version, so that both `pigo update` and the startup banner can reuse it.

**Acceptance Criteria:**
- [ ] 提供函数查询 `smallnest/pigo` 最新 release 的 tag（复用 install.sh 使用的 GitHub `releases/latest` API）
- [ ] 提供语义化版本比较：能判断 latest 是否新于当前 `main.version`
- [ ] 当前版本为 `dev`/`unknown` 时，比较函数返回"无法判断/不提示升级"，不误报
- [ ] 存在 `GITHUB_TOKEN` 环境变量时带上以提高 API 速率限制
- [ ] 单元测试覆盖：更新可用、已是最新、dev 版本、tag 解析失败
- [ ] Typecheck/lint passes

### US-002: 内置自替换执行 `pigo update`
**Description:** As a user, I want to run `pigo update` and have it download and replace my pigo binary with the latest release, so that I stay up to date without re-running the install script.

**Acceptance Criteria:**
- [ ] `pigo update`（无位置参数）触发自更新流程
- [ ] 依据当前 GOOS/GOARCH 拼出归档名（对齐 .goreleaser.yaml：`pigo_{ver}_{Darwin|Linux|Windows}_{x86_64|arm64|i386}.tar.gz`，Windows 为 `.zip`）
- [ ] 下载归档后，下载 `checksums.txt` 并校验归档 SHA256，校验失败即中止且不替换
- [ ] 解压出 pigo 二进制，原子替换当前可执行文件（先写临时文件再 rename 到 `os.Executable()` 路径）
- [ ] 目标路径不可写时，给出清晰错误提示（提示用户用 sudo 或指向可写目录），不静默失败
- [ ] 已是最新版本时打印"已是最新版本 vX.Y.Z"并以退出码 0 结束，不做替换
- [ ] 更新成功后打印新版本号
- [ ] Typecheck/lint passes

### US-003: 保持 `pigo update <包名>` 包更新语义
**Description:** As an existing user, I want `pigo update <package>` to keep updating packages as before, so that adding self-update doesn't break my workflow.

**Acceptance Criteria:**
- [ ] `pigo update <包名>`（带位置参数）仍走现有 `pkgcmd` 包更新路径
- [ ] 仅当 `update` 后无位置参数时才进入自更新
- [ ] `pigo update --check` 等仅带标志、无包名的情况归类为自更新路径（见 Open Questions）
- [ ] 现有 pkgcmd update 相关测试全部通过
- [ ] Typecheck/lint passes

### US-004: 启动横幅展示版本并高亮提示新版本
**Description:** As a user, I want the pigo startup banner to show my current version and highlight when a newer one is available, so that I know at a glance whether to update.

**Acceptance Criteria:**
- [ ] 交互式启动横幅（internal/cli/tui/banner.go 的信息面板）新增一行展示当前版本号（如 `Version  v0.3.1`）
- [ ] 启动时检查最新 release；检查异步进行，不阻塞横幅渲染与首屏输入
- [ ] 检查结果缓存到本地，默认每 24 小时最多联网检查一次（避免每次启动都请求）
- [ ] 发现新版本时，在版本行高亮显示新版本号（如 `Version  v0.3.1 → v0.4.0`，新版本号用醒目颜色）并提示 `运行 pigo update 升级`
- [ ] 已是最新时仅正常显示当前版本，无额外提示
- [ ] 无网络或 API 失败时静默跳过，仅显示当前版本，不打印错误、不影响启动
- [ ] `dev`/无法解析版本时只显示 `dev`，不提示升级
- [ ] Typecheck/lint passes
- [ ] Verify in a browser 无关；在终端交互式启动下人工核验横幅显示（当前版本、有/无更新两种状态）

## Functional Requirements

- FR-1: 系统必须在 `pigo update` 无位置参数时执行自更新，有位置参数时委派给包管理更新。
- FR-2: 系统必须查询 `smallnest/pigo` 的 GitHub 最新 release 以确定目标版本。
- FR-3: 系统必须根据运行平台的 GOOS/GOARCH 选择对应的 release 归档。
- FR-4: 系统必须在替换前用 `checksums.txt` 校验下载归档的 SHA256。
- FR-5: 系统必须以原子方式（临时文件 + rename）替换当前正在运行的二进制。
- FR-6: 当本地已是最新版本时，系统必须跳过替换并告知用户。
- FR-7: 当目标路径不可写或替换失败时，系统必须给出可操作的错误信息且不留下损坏的二进制。
- FR-8: 系统在 `version == "dev"` 或无法解析版本时，不得误判为有可用更新。
- FR-9: 系统必须在交互式启动横幅中展示当前版本号。
- FR-10: 系统必须在启动时异步检查新版本并缓存结果，默认每 24 小时最多联网一次，检查不得阻塞启动。
- FR-11: 当存在新版本时，系统必须在横幅中高亮显示新版本号并提示运行 `pigo update`。
- FR-12: 当无网络、API 失败或已是最新时，系统必须正常显示当前版本且不打印错误。

## Non-Goals (Out of Scope)

- 不提供降级 / 指定任意版本安装（本期只升级到 latest；`--version` 指定安装列为 Open Question）
- 不在启动时**自动**下载或安装更新（启动仅展示/提示，实际更新须用户显式运行 `pigo update`）
- 不实现自动静默更新（必须由用户显式运行 `pigo update`；启动仅提示不自动装）
- 不改动 install.sh / goreleaser 的发布流程
- 不做增量/差分更新（整包替换）
- 不管理多副本 / 系统包管理器（brew、apt）安装的 pigo 的更新

## Technical Considerations

- 版本、commit、date 已由 goreleaser 通过 `-ldflags -X main.version=...` 注入（见 cmd/pigo/main.go:42、.goreleaser.yaml），自更新与提示逻辑读取这些变量。
- 归档命名规则须与 .goreleaser.yaml 的 `name_template` 严格一致，建议抽成常量或与 install.sh 保持单一事实来源。
- 自替换需处理"正在运行的二进制被替换"：Unix 上对已打开的可执行文件 rename 覆盖是安全的；实现应先写同目录临时文件再 `os.Rename` 以保证原子性和同一文件系统。
- 分发建议放在新包（如 `internal/selfupdate`），供 `pkgcmd`/main 的 update 分支与启动横幅检查复用。
- 启动横幅信息面板在 internal/cli/tui/banner.go，新增版本行与高亮沿用现有 lipgloss 样式约定。
- 启动检查须异步执行、结果落地缓存（如 `~/.pigo/update-check.json`，记录检查时间与最新版本），命中缓存则不发网络请求。
- 网络与校验逻辑可参考 install.sh 已验证的 URL/命名约定。

## Success Metrics

- 用户在联网环境执行 `pigo update` 后，`pigo --version` 显示为最新 release 版本号
- checksum 不匹配时 100% 中止且旧二进制保持可用
- 启动横幅在缓存命中时零网络请求、对启动耗时无可感知影响

## Open Questions

1. `pigo update --check`（只检查不下载）是否需要？用户本期选择"仅自动更新"，是否保留一个 `--check` 只读标志作为便利项？
2. 是否需要 `pigo update --version vX.Y.Z` 指定版本（含降级）？
3. 是否需要环境变量/配置项关闭启动横幅的新版本检查（如离线或 CI 环境）？
4. 缓存文件位置 `~/.pigo/` 是否与现有配置/缓存目录约定一致？需确认。
5. Windows 下自替换（无法覆盖正在运行的 .exe）是否需要"改名旧文件 + 写新文件"的特殊路径？

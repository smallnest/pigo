# PRD: TUI 增强与中文支持（参考 zero）

## Introduction

pigo 当前的交互式终端界面（`internal/tui`）基于 `charm.land/bubbletea/v2`，采用「单体 `Model` + 纯 `uiState` + `View()` 返回整串」的极简结构。它能跑通基本对话，但存在两个硬缺陷和一批体验短板：

1. **中文无法输入**：`handleKey` 的 `default` 分支只在 `len(s) == 1` 时把按键追加到输入行，多字节的 UTF-8（中文/CJK/emoji）会被静默丢弃——用户根本打不出中文。
2. **无宽度感知渲染**：`View()` 把 transcript 和输入行拼成纯字符串，不区分中文双宽字符，换行、截断、光标对齐都会错位。

参考项目 zero（Gitlawb/zero，同样基于 bubbletea v2）已经把这些做成熟：它用 bubbles 的 `textinput`/`spinner`、lipgloss 主题、`lipgloss.Width`（底层 go-runewidth）做宽度感知渲染，并有视口滚动、主题配色、流式动画、Markdown/代码块渲染等能力。

本 PRD 的目标是「适度重构」pigo 的 TUI：引入 bubbles 组件与 lipgloss 主题层，彻底修复中文输入与双宽渲染，并对标 zero 补齐视口滚动、主题配色、流式状态指示、Markdown 渲染等体验，让 pigo 的交互界面在功能与观感上都接近 zero。

## Goals

- 用户可以在输入行正常键入中文/CJK/emoji，并按 rune 移动光标、退格删除整个字符。
- 中文双宽字符在换行、截断、光标定位、对齐时不再错位。
- 长对话可滚动浏览历史（视口滚动），而非一次性堆叠输出。
- transcript 中 user/assistant/tool/system 有清晰的颜色与样式区分，支持 dark/light 主题。
- 运行中有 spinner 动画与状态提示，工具调用以卡片形式呈现。
- assistant 输出以 Markdown（含代码块高亮）渲染，而非纯文本。
- 观感对标 zero：主题、动画、卡片等尽量还原，同时保持 pigo 现有的 runID 陈旧守卫、steering 队列、两段式 Ctrl+C 等状态语义不变。

## User Stories

### US-001: 引入 lipgloss 主题层
**Description:** As a pigo 用户, I want TUI 有一套集中的主题/配色定义, so that 各类消息有一致且可切换的视觉风格。

**Acceptance Criteria:**
- [ ] 新增 `internal/tui/theme.go`，定义 `tuiTheme` 结构，持有 user/assistant/tool-call/tool-result/system/accent/error 等元素的 `lipgloss.Style`。
- [ ] 提供 `buildTheme(mode)` 将调色板解析为样式；支持 `dark` 与 `light` 两套调色板。
- [ ] 遵守 `NO_COLOR` 环境变量：设置时输出不含 ANSI 颜色码（含义不依赖颜色，仍有角色前缀/glyph 区分）。
- [ ] `go build ./...`、`go vet ./...`、`go test ./...`、`gofmt -l` 全绿。

### US-002: 用 bubbles textinput 修复中文输入与 rune 级编辑
**Description:** As a 中文用户, I want 在输入行正常键入中文并按字符编辑, so that 我能用母语与 agent 对话。

**Acceptance Criteria:**
- [ ] 用 `charm.land/bubbles/v2/textinput` 替换 `uiState.input` 的手写字符串拼接与 `handleKey` 的 `default`/`backspace` 分支。
- [ ] 输入多字节中文（如「你好世界」）后，输入行完整显示该文本，不丢字（修复原 `len(s)==1` 缺陷）。
- [ ] 光标可在中文文本中按 rune 左右移动；退格删除整个中文字符（一次一个 rune，而非一个字节）。
- [ ] `submit()` 提交的 prompt 文本与用户键入的中文逐字一致（UTF-8 正确）。
- [ ] 保留原有语义：提交空/纯空白输入被忽略；运行中提交进入 steering 队列；任意键入 disarm 两段式 Ctrl+C。
- [ ] 相关状态转换有单元测试（含中文输入用例），`go test ./internal/tui/...` 全绿。

### US-003: 宽度感知的渲染与对齐
**Description:** As a 用户, I want 中文/emoji 等双宽字符在界面里正确对齐, so that 换行、截断、光标定位不错位。

**Acceptance Criteria:**
- [ ] 渲染层用 `lipgloss.Width`（或 go-runewidth）计算显示宽度，替换所有基于 `len()`/字节数的宽度假设。
- [ ] 按显示宽度在 rune 边界换行/截断：含中文的长行按终端宽度折行，不会把一个中文字切成两半。
- [ ] `firstLine`/tool-result 摘要等截断逻辑改为按显示宽度截断，中文行截断后不错位。
- [ ] 提供针对宽字符换行/截断的单元测试（如混合「abc中文def」按宽度折行），`go test ./internal/tui/...` 全绿。

### US-004: 视口滚动浏览长对话
**Description:** As a 用户, I want 长对话可以上下滚动查看历史, so that 超出屏幕的内容不会丢失或无法回看。

**Acceptance Criteria:**
- [ ] 引入 `charm.land/bubbles/v2/viewport`（或等价的 `chatScrollOffset` 滚动状态）承载 transcript 渲染。
- [ ] transcript 高度超过可视区域时，可用键盘（如 PgUp/PgDn 或方向键）上下滚动。
- [ ] 有新消息/流式更新时，若用户处于底部则自动跟随到底部；若用户已上滚查看历史则不强制打断。
- [ ] 窗口 resize（`WindowSizeMsg`）时视口尺寸随之更新，渲染不溢出。
- [ ] `go build ./...` 与 `go test ./internal/tui/...` 全绿。

### US-005: 流式状态指示与工具调用卡片
**Description:** As a 用户, I want 运行时看到清晰的进度与工具调用, so that 我知道 agent 正在做什么。

**Acceptance Criteria:**
- [ ] 引入 `charm.land/bubbles/v2/spinner`，运行中（`running == true`）显示动画 spinner + 运行提示。
- [ ] 工具调用（`entryToolCall`）与结果（`entryToolResult`）以带样式的「卡片」呈现，视觉上区别于普通文本。
- [ ] 保留原有的「运行中可键入 steering、Ctrl+C 中断」提示语义。
- [ ] spinner 通过 bubbletea 的 tick 命令驱动，不阻塞 Update；run 结束后停止动画。
- [ ] `go build ./...` 与 `go test ./internal/tui/...` 全绿。

### US-006: assistant 输出 Markdown 渲染
**Description:** As a 用户, I want assistant 的回复按 Markdown 渲染, so that 标题、列表、代码块可读性更好。

**Acceptance Criteria:**
- [ ] assistant 文本经 Markdown 渲染器（如 glamour，或与主题配套的渲染）输出，代码块带语法高亮/边框。
- [ ] 保留原 `fenceBuffer` 语义：流式过程中未闭合的 ``` 代码围栏能优雅呈现，不破坏布局。
- [ ] Markdown 渲染宽度与当前视口宽度一致，中文在渲染结果中不错位（与 US-003 一致）。
- [ ] `NO_COLOR` 下降级为无颜色的纯文本/朴素渲染，仍可读。
- [ ] `go build ./...` 与 `go test ./internal/tui/...` 全绿。

### US-007: 角色样式化的 transcript 渲染
**Description:** As a 用户, I want user/assistant/tool/system 消息有颜色与前缀区分, so that 我能一眼分辨对话来源。

**Acceptance Criteria:**
- [ ] `renderEntry` 使用 US-001 的主题样式为各 `entryKind` 上色（user/assistant/toolCall/toolResult/system）。
- [ ] 角色前缀（`you>`、`⚙`、`↳`、`·` 等）保留或对标 zero 优化，且在 `NO_COLOR` 下仍通过前缀/glyph 区分含义。
- [ ] 与 US-003 一致：带前缀的行按显示宽度对齐，中文不错位。
- [ ] `go build ./...`、`go vet ./...`、`gofmt -l`、`go test ./internal/tui/...` 全绿。

## Functional Requirements

- FR-1: 系统必须使用 `charm.land/bubbles/v2/textinput` 处理输入行，支持多字节 UTF-8（中文/CJK/emoji）键入。
- FR-2: 系统必须支持在输入行按 rune 移动光标与按 rune 删除（退格删除整个中文字符）。
- FR-3: 系统必须在所有渲染路径用显示宽度（`lipgloss.Width`/go-runewidth）而非字节长度计算换行、截断与对齐。
- FR-4: 系统必须按显示宽度在 rune 边界折行，不得将单个宽字符切分。
- FR-5: 系统必须提供集中的 lipgloss 主题层，支持 dark/light 两套调色板。
- FR-6: 系统必须遵守 `NO_COLOR`，禁用颜色时仍通过前缀/glyph 传达消息角色与状态。
- FR-7: 系统必须提供 transcript 视口滚动，超屏内容可用键盘上下浏览。
- FR-8: 系统必须在有新内容且用户处于底部时自动跟随，用户上滚时不强制打断。
- FR-9: 系统必须在窗口 resize 时更新视口尺寸并保持渲染不溢出。
- FR-10: 系统必须在运行中显示 spinner 动画与运行状态提示，run 结束后停止动画。
- FR-11: 系统必须将工具调用与结果以带样式的卡片呈现。
- FR-12: 系统必须将 assistant 文本以 Markdown（含代码块高亮）渲染，并保留未闭合代码围栏的优雅降级。
- FR-13: 系统必须保留现有交互语义：runID 陈旧事件守卫、steering 队列、两段式 Ctrl+C（中断/退出）、resume 会话回放。
- FR-14: 系统必须保持 `uiState` 纯状态机层可被单元测试直接驱动（不依赖真实终端）。

## Non-Goals (Out of Scope)

- 不实现鼠标选择/拖拽自动滚动（zero 有，本次不做）。
- 不实现命令中心、model/provider picker、主题 picker 等交互面板（超出本次范围）。
- 不实现图片输入、会话 fork/rewind、sidebar 等 zero 高级功能。
- 不改动 agent loop、provider、tool、session 等非 TUI 层的行为契约。
- 不新增输入法/IME 组合态（composition）的高级处理，仅保证终端已提交的 UTF-8 文本被正确接收。
- 不做多语言 i18n 文案框架（提示语中英文即可，不引入翻译系统）。

## Design Considerations

- 复用并扩展现有的两层结构：`state.go` 纯状态机 + `model.go` bubbletea 桥接。渲染/主题细节可拆到新文件（`theme.go`，必要时 `rendering.go`），但保持 `uiState` 无 bubbletea 依赖、可单测。
- 输入迁移到 `textinput.Model` 后，`uiState.input string` 的读写点（`submit`、`handleKey`、`View`）需相应改造；提交/steering/Ctrl+C-disarm 的语义保持不变。
- 主题与 zero 对齐：优先复用 zero 的角色配色思路与卡片样式，dark 为默认，`themeAuto` 可作为后续增强（本次至少提供显式 dark/light）。
- Markdown 渲染器若引入 glamour，需与视口宽度联动，避免与 US-003 的宽度计算冲突。

## Technical Considerations

- 依赖：`charm.land/bubbles/v2`（textinput/viewport/spinner）、`charm.land/lipgloss/v2`、可选 glamour；`github.com/mattn/go-runewidth` 已是间接依赖，可直接用于宽度计算。新增直接依赖后需 `go mod tidy` 将其提升为 direct。
- 兼容 bubbletea v2 的 `KeyPressMsg`/`WindowSizeMsg`/`tea.View` API（项目已在用 v2.0.8）。
- spinner/流式 fade 通过 tick 命令驱动，注意与现有 drain goroutine + `Program.Send` 的并发模型协作，所有状态变更仍只在 Update goroutine 内发生。
- 保持 `go 1.27rc1` 构建环境；所有变更需通过 `go build/vet/test` 与 `gofmt`。

## Success Metrics

- 中文可正常输入并提交：键入「你好，帮我读 README」后输入行与提交文本逐字一致，agent 收到完整 UTF-8。
- 双宽对齐无错位：混合中英文/emoji 的长行折行、截断、光标定位在标准终端宽度下目视与测试均正确。
- 长对话可滚动：超屏 transcript 可用键盘回看历史，新消息自动跟随。
- 观感对标 zero：dark 主题下 user/assistant/tool/system 配色区分清晰，运行有 spinner，工具调用为卡片，assistant 输出为 Markdown——整体接近 zero 的呈现。
- 质量门槛：`go build ./...`、`go vet ./...`、`gofmt -l`（无输出）、`go test ./...` 全绿，且 `uiState` 单测覆盖中文输入与宽度渲染用例。

## Open Questions

- Markdown 渲染是否引入 glamour（体积/依赖 vs. 渲染质量）？还是先用 lipgloss 手写轻量渲染？[Assumption] 倾向 glamour，若依赖过重可降级为轻量方案。
- 主题是否需要 `themeAuto`（探测终端背景色）？本次先提供显式 dark/light，auto 列为后续增强。
- 中文范围：用户仅显式勾选了「按 rune 编辑（C）」，但「支持中文」的前提是 CJK 输入（A）与双宽对齐（B）。本 PRD 已将三者作为整体纳入（US-002/US-003）；如只需 C 请告知以缩减范围。
- 键位约定：滚动用 PgUp/PgDn 还是方向键？是否对齐 zero 的键位（Enter 发送、Ctrl+C 取消/退出等已一致）？

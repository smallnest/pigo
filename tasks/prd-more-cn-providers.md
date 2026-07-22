# PRD: 新增国内大模型 Provider（千帆 / 火山方舟 / 百炼 / 混元）

## Introduction

pigo 通过 `internal/provider/registry.go` 的 provider 注册表管理所有内置 Provider 的元数据（名称、API Key 环境变量、默认 base URL、线路协议、鉴权方式）。目前注册表覆盖了 OpenRouter、Anthropic、DeepSeek、MiniMax、月之暗面等 30+ 个 Provider，但缺少国内几家主流云厂商的大模型平台。

本需求为 pigo 新增四个国内 Provider：**百度智能云千帆（Qianfan）**、**字节火山引擎方舟（Volcengine Ark）**、**阿里云百炼（DashScope）**、**腾讯混元（Hunyuan）**。四者均提供 OpenAI 兼容端点、走 Bearer 鉴权，因此可完全复用现有的 `NewOpenRouterProvider` / OpenAI-compatible 驱动路径，改动是**纯增量**的——向注册表追加条目、向 presets 追加预置模型、更新文档，不改动任何现有 Provider 的行为。

## Goals

- 用户可通过 `--provider qianfan|volcengine|dashscope|hunyuan` 直接选中对应平台，使用其默认 base URL、协议与 API Key 环境变量
- 每个新 Provider 至少提供一个可用的预置模型（供 REPL `/models` 展示、`/model` 切换，以及 model id → provider 推断）
- 沿用现有 base_url 覆盖优先级（`--base-url` > 专有 `*_BASE_URL` > 泛化 `<PROVIDER>_BASE_URL` > 注册表默认值）与 `<PROVIDER>_API_KEY` Key 回退约定
- README「内置 Provider 一览」表与相关文档同步更新
- 不回归：现有 Provider 的解析、鉴权、base_url 覆盖行为完全不变

## User Stories

### US-001: 向注册表新增四个国内 Provider 条目
**Description:** As a pigo 用户, I want 注册表内置千帆/火山方舟/百炼/混元 four Provider, so that 我能用 `--provider <name>` 直接选中它们而无需手动指定 base URL 和协议。

**Acceptance Criteria:**
- [ ] `providerRegistry` 新增 4 条 `ProviderSpec`：
  - `qianfan`：EnvVars `["QIANFAN_API_KEY"]`，DefaultBaseURL `https://qianfan.baidubce.com/v2`，Protocol `openai`，AuthScheme `bearer`
  - `volcengine`：EnvVars `["ARK_API_KEY", "VOLCENGINE_API_KEY"]`，DefaultBaseURL `https://ark.cn-beijing.volces.com/api/v3`，Protocol `openai`，AuthScheme `bearer`
  - `dashscope`：EnvVars `["DASHSCOPE_API_KEY"]`，DefaultBaseURL `https://dashscope.aliyuncs.com/compatible-mode/v1`，Protocol `openai`，AuthScheme `bearer`
  - `hunyuan`：EnvVars `["HUNYUAN_API_KEY"]`，DefaultBaseURL `https://api.hunyuan.cloud.tencent.com/v1`，Protocol `openai`，AuthScheme `bearer`
- [ ] `LookupProviderSpec("qianfan")` 等四个名称均返回 `ok == true`
- [ ] `ProviderNames()` 包含四个新名称，且注册表原有条目顺序不变
- [ ] `go build ./...` 通过

### US-002: 为四个 Provider 补充预置模型
**Description:** As a pigo 用户, I want 每个国内平台有代表性预置模型, so that 我能在 REPL 用 `/models` 看到它们并 `/model` 切换，或直接用 model id 让 pigo 推断出正确的 provider。

**Acceptance Criteria:**
- [ ] `presets.go` 为每个 Provider 至少新增一个 `ModelPreset`，包含 `Provider`、`ID`、`DisplayName`（示例，最终 id 以各平台文档为准）：
  - qianfan：如 `ernie-4.5-turbo-32k`（DisplayName「ERNIE 4.5 Turbo (百度千帆)」）
  - volcengine：如 `doubao-seed-1-6`（DisplayName「Doubao Seed 1.6 (火山方舟)」）
  - dashscope：如 `qwen-max`（DisplayName「Qwen Max (阿里百炼)」）
  - hunyuan：如 `hunyuan-turbos-latest`（DisplayName「Hunyuan TurboS (腾讯混元)」）
- [ ] `LookupPreset(<id>)` 对每个新预置 id 返回对应 `Provider`
- [ ] 预置条目的 `Provider` 字段与 US-001 注册表名称一致
- [ ] `go build ./...` 通过

### US-003: model id → provider 推断命中新预置
**Description:** As a pigo 用户, I want 传入新预置模型 id 时 pigo 自动路由到对应国内平台, so that 我无需每次都加 `--provider`。

**Acceptance Criteria:**
- [ ] `resolveProvider(<新预置 id>, "", "", "")` 经预置目录命中分支（`main.go` 第 1 步 `LookupPreset`）走 OpenAI-compatible 驱动，返回的 provider-name 为对应平台名
- [ ] 对四个平台的预置 id 分别验证：解析出的 provider 使用注册表中该 provider 的 base URL 与 API Key 环境变量
- [ ] 未命中预置的未知 id 仍回退到 OpenRouter（现有默认行为不变）

### US-004: 补充注册表与解析的单元测试
**Description:** As a 维护者, I want 新 Provider 有测试覆盖, so that 后续改动不会破坏它们的注册与解析。

**Acceptance Criteria:**
- [ ] 注册表测试断言四个新 Provider 的 Name / EnvVars / DefaultBaseURL / Protocol / AuthScheme 与 US-001 一致
- [ ] 解析测试断言 `--provider <name>` 与预置 id 两条路径都路由到正确 provider 且 base_url 覆盖优先级正确
- [ ] `go test ./internal/provider/... ./cmd/pigo/...` 通过

### US-005: 更新 README 文档
**Description:** As a 新用户, I want README 列出新增的国内 Provider, so that 我知道如何配置它们的 Key 与 base URL。

**Acceptance Criteria:**
- [ ] README「内置 Provider 一览（`--provider`）」表格新增四行，列出 provider 名、环境变量、默认 base_url、协议
- [ ] 与 `internal/provider/registry.go` 保持一致（表格声明的既有约束）
- [ ] `pigo --help` 输出的 provider 清单包含四个新名称（由注册表自动驱动，无需单独改 help 文本）

## Functional Requirements

- FR-1: 系统必须在 `providerRegistry` 中注册 `qianfan` Provider，默认 base URL 为 `https://qianfan.baidubce.com/v2`，OpenAI 协议，Bearer 鉴权，Key 环境变量 `QIANFAN_API_KEY`
- FR-2: 系统必须在 `providerRegistry` 中注册 `volcengine` Provider，默认 base URL 为 `https://ark.cn-beijing.volces.com/api/v3`，OpenAI 协议，Bearer 鉴权，Key 环境变量按优先级 `ARK_API_KEY` → `VOLCENGINE_API_KEY`
- FR-3: 系统必须在 `providerRegistry` 中注册 `dashscope` Provider，默认 base URL 为 `https://dashscope.aliyuncs.com/compatible-mode/v1`，OpenAI 协议，Bearer 鉴权，Key 环境变量 `DASHSCOPE_API_KEY`
- FR-4: 系统必须在 `providerRegistry` 中注册 `hunyuan` Provider，默认 base URL 为 `https://api.hunyuan.cloud.tencent.com/v1`，OpenAI 协议，Bearer 鉴权，Key 环境变量 `HUNYUAN_API_KEY`
- FR-5: 系统必须为每个新 Provider 在 presets 目录中提供至少一个预置模型，其 `Provider` 字段与注册表名称一致
- FR-6: 当用户以 `--provider <新名称>` 运行时，系统必须使用注册表声明的 base URL、协议与 Key 环境变量构建驱动
- FR-7: 系统必须保持 base_url 覆盖优先级：`--base-url` > 专有 `*_BASE_URL` env > 泛化 `<PROVIDER>_BASE_URL` env > 注册表默认值
- FR-8: 系统必须保证新增条目不改变任何现有 Provider 的注册顺序、解析结果与鉴权行为

## Non-Goals (Out of Scope)

- 不实现各平台的原生（非 OpenAI 兼容）SDK 协议——仅接入其 OpenAI 兼容端点
- 不接入需要 SK/AK 签名（如百度 IAM V3 签名、火山 AK/SK 签名）的旧版鉴权路径；仅支持各平台的 Bearer API Key 模式
- 不为每个平台穷举全部模型；只提供少量代表性预置，其余模型用户可用 `-m <id>` 自行指定
- 不实现多区域/多 endpoint 自动选择（如火山不同 region）——用户可用 `--base-url` 覆盖
- 不改动 provider 解析的启发式优先级结构，仅新增数据

## Technical Considerations

- **纯增量**：四者均为 OpenAI 兼容 + Bearer，无需新增鉴权 scheme 或专用驱动，直接复用 `ProtocolOpenAI` / `AuthBearer` 与现有 OpenAI-compatible 驱动
- **改动文件**：`internal/provider/registry.go`（注册表条目）、`internal/provider/presets.go`（预置模型）、对应 `_test.go`、`README.md`
- **base URL 待核对**：各平台 OpenAI 兼容端点以官方最新文档为准，若与本 PRD 列出的默认值不同，以实现时核对结果为准并在注册表注释标注来源
- **Key 环境变量命名**：遵循 `<PROVIDER>_API_KEY` 泛化约定；火山方舟官方惯用 `ARK_API_KEY`，故设为首选、`VOLCENGINE_API_KEY` 为回退
- **参考**：现有 `deepseek` / `zai-coding-cn` 等国内 Provider 条目是最贴近的实现模板

## Success Metrics

- 四个 `--provider <name>` 均可在配置对应 Key 后成功发起一次补全请求
- `pigo --help` 与 README 表格中出现四个新 Provider
- `go test ./...` 全绿，无既有用例回归

## Open Questions

- 各平台 OpenAI 兼容端点的确切 base URL 与推荐默认模型 id 需在实现时对照官方文档最终确认（本 PRD 已给出当前公开值作为默认，可能随平台更新调整）
- 是否需要为火山方舟的「推理接入点 ID（endpoint id）」模式做特殊说明（部分模型需用 endpoint id 而非模型名）——建议在 README 加一句提示，暂不做代码特判
- provider 名称最终定名：`volcengine` vs `ark`、`dashscope` vs `bailian`、`qianfan` vs `baidu`——本 PRD 采用平台 API 品牌名（qianfan/volcengine/dashscope/hunyuan），实现前可再确认

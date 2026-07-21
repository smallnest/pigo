# PRD: Provider 环境变量与默认端点对齐 pi

## Introduction

pigo 是 pi 的 Go 复刻。当前 pigo 只内置了 5 个可用 provider（OpenRouter 默认、Ollama、NVIDIA、Anthropic、Bedrock 构造器但未接线），API key 解析也只覆盖 9 个环境变量。而 pi 支持约 30 个 provider，每个都有约定的环境变量名、默认 `base_url` 与所属协议（OpenAI 兼容 / Anthropic Messages）。

本特性让 pigo 全量对齐 pi 的 provider 生态：用户设置对应的环境变量（如 `DEEPSEEK_API_KEY`），用 `--provider deepseek` 选中该 provider，pigo 就能用内置的默认 `base_url`、协议、精选模型目录直接发起对话，无需手动指定 `--base-url`。同时提供通用的 `<PROVIDER>_BASE_URL` 环境变量约定用于覆盖默认端点。

**读者假设：** 实现者应先阅读 `internal/provider/providers.go`（provider 构造器与两种 wire driver）、`internal/provider/auth.go`（`providerEnvVars` 与 key 解析）、`internal/provider/presets.go`（`PresetProviders` / `PresetCatalog`）、`cmd/pigo/main.go`（`resolveProvider` 与 CLI flag）。

## Goals

- 内置对齐 pi 的全部 provider（含 Azure OpenAI、Amazon Bedrock、Google Vertex、Cloudflare 及 -cn/-ams/-sgp 区域变体），每个 provider 具备：环境变量名、默认 `base_url`、协议、精选模型目录。
- 新增 `--provider` 显式标志，将 provider 名映射到内置默认端点 + env key + 协议。
- 支持通用 `<PROVIDER>_BASE_URL` 环境变量约定覆盖默认 `base_url`，并保留 pi 的专有别名（如 `AZURE_OPENAI_BASE_URL`）。
- 为每个新 provider 内置精选模型目录，供 `/models` 列出与 preset 选择。
- 现有行为（模型前缀推断、默认 OpenRouter、`--base-url`、`--protocol`、`--api-key`）保持向后兼容。

## User Stories

### US-001: 建立 provider 注册表（元数据单一来源）
**Description:** 作为开发者，我需要一份集中的 provider 注册表，描述每个 provider 的名称、环境变量、默认 base_url、协议与鉴权方式，作为后续所有逻辑的单一数据来源。

**Acceptance Criteria:**
- [ ] 新增结构体（如 `ProviderSpec`），字段含：`Name`、`EnvVars []string`、`DefaultBaseURL`、`Protocol`（openai|anthropic）、`AuthScheme`（bearer|x-api-key|aws|azure|special）、`ExtraHeaders`、`BaseURLEnvVars []string`。
- [ ] 注册表覆盖下列 provider 及默认 base_url（见 Technical Considerations 表）。
- [ ] 提供 `LookupProviderSpec(name string) (ProviderSpec, bool)`。
- [ ] Typecheck/`go vet` 通过，单测覆盖查表命中/未命中。

### US-002: 统一 API key 解析到注册表
**Description:** 作为用户，我希望任意内置 provider 都能从其约定环境变量解析出 API key，包括多别名与 `<PROVIDER>_API_KEY` 泛化回退。

**Acceptance Criteria:**
- [ ] `internal/provider/auth.go` 的 `providerEnvVars` 改为从 US-001 注册表派生，覆盖全部 provider。
- [ ] Anthropic 保持 `ANTHROPIC_OAUTH_TOKEN` 优先于 `ANTHROPIC_API_KEY` 的顺序。
- [ ] 保留 `<PROVIDER>_API_KEY` 泛化回退。
- [ ] 单测：为代表性 provider（deepseek、groq、zai、moonshotai-cn、xiaomi-token-plan-ams）验证 key 解析。
- [ ] Typecheck/`go vet` 通过。

### US-003: `--provider` 显式标志
**Description:** 作为用户，我想用 `--provider <name>` 直接选中某个内置 provider，让 pigo 使用其默认 base_url、协议与 env key。

**Acceptance Criteria:**
- [ ] `cmd/pigo/main.go` 新增 `--provider`（简写待定）flag。
- [ ] `resolveProvider` 支持：当 `--provider` 指定时，按注册表构造对应 driver（openai 或 anthropic 协议），provider 名用于 key 解析。
- [ ] `--provider` 与 `--protocol` 同时给出且冲突时报明确错误。
- [ ] 未指定 `--provider` 时保持现有推断逻辑（模型前缀 / 11434 / 默认 OpenRouter）不变。
- [ ] 未知 provider 名报错并提示可用 provider 列表。
- [ ] 单测覆盖 `--provider` 命中、冲突、未知名三种路径。
- [ ] Typecheck/`go vet` 通过。

### US-004: 通用 `<PROVIDER>_BASE_URL` 覆盖约定
**Description:** 作为用户，我想用 `<PROVIDER>_BASE_URL` 环境变量覆盖某 provider 的默认端点（如自建代理），无需每次传 `--base-url`。

**Acceptance Criteria:**
- [ ] base_url 解析优先级：`--base-url` flag > provider 专有 base_url env（如 `AZURE_OPENAI_BASE_URL`）> 泛化 `<PROVIDER>_BASE_URL`（provider 名大写、`-` 转 `_`）> 注册表默认值。
- [ ] provider 名含 `-` 的（如 `zai-coding-cn`）正确转换为 `ZAI_CODING_CN_BASE_URL`。
- [ ] 单测验证四级优先级与命名转换。
- [ ] Typecheck/`go vet` 通过。

### US-005: OpenAI 兼容 provider 批量接线
**Description:** 作为用户，我想通过 `--provider` 使用所有纯 Bearer + OpenAI 协议的 provider（deepseek、groq、xai、cerebras、mistral、moonshotai、moonshotai-cn、fireworks、together、openrouter、nvidia、zai、zai-coding-cn、kimi-coding、opencode、opencode-go、huggingface、ant-ling、vercel-ai-gateway、xiaomi 及其区域变体）。

**Acceptance Criteria:**
- [ ] 上述每个 provider 用 `newOpenAICompat` 接入，带正确默认 base_url 与 env key。
- [ ] `HF_TOKEN`（huggingface）等非 `_API_KEY` 命名的 env 正确解析。
- [ ] 每个 provider 至少一条 provider 构造 + key 解析单测。
- [ ] Typecheck/`go vet` 通过。

### US-006: Anthropic Messages 协议 provider 接线
**Description:** 作为用户，我想使用走 Anthropic Messages 协议的 provider（anthropic、minimax、minimax-cn、cloudflare-ai-gateway 的 anthropic 端点）。

**Acceptance Criteria:**
- [ ] 这些 provider 用 `newAnthropicCompat` 接入，`base_url` 与鉴权头正确（minimax 默认 `https://api.minimax.io/anthropic`）。
- [ ] minimax-cn 默认 `https://api.minimaxi.com/anthropic`。
- [ ] 单测验证 base_url 与协议选择。
- [ ] Typecheck/`go vet` 通过。

### US-007: 特殊鉴权 provider（Azure / Bedrock / Vertex / Cloudflare）
**Description:** 作为用户，我想使用需要多参数或非标准鉴权的 provider。

**Acceptance Criteria:**
- [ ] Azure OpenAI：支持 `AZURE_OPENAI_API_KEY`、`AZURE_OPENAI_BASE_URL`（或 `AZURE_OPENAI_RESOURCE_NAME` 拼接）、`AZURE_OPENAI_API_VERSION`（默认 v1）、`AZURE_OPENAI_DEPLOYMENT_NAME_MAP`；端点按 Azure 约定构造。
- [ ] Amazon Bedrock：支持 `AWS_BEARER_TOKEN_BEDROCK`（Bearer）与 `AWS_PROFILE` / `AWS_ACCESS_KEY_ID`+`AWS_SECRET_ACCESS_KEY` 的可用性判定；`AWS_REGION` 决定 base_url（默认 us-east-1）。SigV4 签名若超出范围需在 Non-Goals 标注。
- [ ] Google Vertex：`GOOGLE_CLOUD_API_KEY` 或 ADC（`GOOGLE_APPLICATION_CREDENTIALS` / 默认 ADC 路径）+ `GOOGLE_CLOUD_PROJECT` + `GOOGLE_CLOUD_LOCATION`；base_url 按 location 构造 `https://{location}-aiplatform.googleapis.com`。
- [ ] Cloudflare：`CLOUDFLARE_API_KEY` + `CLOUDFLARE_ACCOUNT_ID`（Workers AI）/ 额外 `CLOUDFLARE_GATEWAY_ID`（AI Gateway）；base_url 按模板拼接账号/网关 id。
- [ ] 缺少必需参数时报清晰错误（指出缺哪个 env）。
- [ ] 单测覆盖各 provider 的参数校验与 base_url 拼接（对缺参/齐参分别断言），不发起真实网络请求。
- [ ] Typecheck/`go vet` 通过。

### US-008: 精选模型目录扩展
**Description:** 作为用户，我想用 `/models` 看到新 provider 下的可选模型，并能按 preset 选择。

**Acceptance Criteria:**
- [ ] `PresetProviders` 扩展为包含新 provider（名 + env key）。
- [ ] `PresetCatalog` 为每个新 provider 增加至少 2-3 个精选模型条目（id 参考 pi 各 provider 的 `.models.ts`）。
- [ ] `PresetsByProvider` / `LookupPreset` 对新条目工作正常。
- [ ] 单测验证目录条目数与查表。
- [ ] Typecheck/`go vet` 通过。

### US-009: `--help` 与 README 环境变量文档
**Description:** 作为用户，我想在 `--help` 与 README 中看到支持的 provider、环境变量与默认端点清单。

**Acceptance Criteria:**
- [ ] `--help` 输出列出 `--provider` 及支持的 provider 名。
- [ ] README「目录与环境变量」章节新增一张表：provider 名 → 环境变量 → 默认 base_url → 协议。
- [ ] 文档与注册表内容一致（可由测试或人工核对）。
- [ ] Typecheck/`go vet` 通过。

## Functional Requirements

- FR-1: 系统必须提供集中的 provider 注册表，含 name、env vars、默认 base_url、协议、鉴权方式、额外 header、base_url 覆盖 env。
- FR-2: 系统必须从注册表派生 API key 解析，覆盖全部内置 provider。
- FR-3: 系统必须保持 `ANTHROPIC_OAUTH_TOKEN` 优先于 `ANTHROPIC_API_KEY`。
- FR-4: 系统必须保留 `<PROVIDER>_API_KEY` 泛化回退。
- FR-5: 系统必须新增 `--provider` 标志，按注册表选中 provider。
- FR-6: 当 `--provider` 与 `--protocol` 冲突时，系统必须报明确错误。
- FR-7: 未指定 `--provider` 时，系统必须保持现有推断与默认 OpenRouter 行为。
- FR-8: 系统必须按「`--base-url` > 专有 base_url env > `<PROVIDER>_BASE_URL` > 注册表默认」的优先级解析 base_url。
- FR-9: 系统必须将 provider 名中的 `-` 转为 `_` 并大写以生成 `<PROVIDER>_BASE_URL` 变量名。
- FR-10: 系统必须为每个 OpenAI 协议 provider 用 OpenAI 兼容 driver 接线。
- FR-11: 系统必须为每个 Anthropic 协议 provider 用 Anthropic 兼容 driver 接线。
- FR-12: 系统必须为 Azure/Bedrock/Vertex/Cloudflare 实现各自的参数校验与端点拼接。
- FR-13: 当特殊 provider 缺少必需参数时，系统必须报出缺失的具体环境变量。
- FR-14: 系统必须扩展 `PresetProviders` 与 `PresetCatalog` 以包含新 provider 的精选模型。
- FR-15: 系统必须在 `--help` 与 README 中列出 provider、环境变量与默认端点。
- FR-16: 系统对未知 `--provider` 值必须报错并提示可用 provider。

## Non-Goals

- 不实现 AWS SigV4 请求签名；Bedrock 仅支持 `AWS_BEARER_TOKEN_BEDROCK` bearer 路径，其他凭证源仅做可用性识别（若无法完成请求则报错说明）。
- 不实现 OpenAI Responses API、Codex、GitHub Copilot 等 pi 中的非标准 wire 变体（若确需，另开 PRD）。
- 不做各 provider 模型能力（vision/tool/thinking）的逐项精确建模，`SupportsImages` 沿用现有默认。
- 不实现凭证的持久化存储或交互式登录流程。
- 不做模型价格/额度查询。

## Design Considerations

- 复用现有两种 driver：`openAICompatDriver` 与 `anthropicCompatDriver`；新 provider 尽量落在这两类，特殊 provider 通过额外 header/端点拼接适配。
- `--provider` 简写字母需避免与现有 flag 冲突（`-u` 已被 `--base-url` 占用）。
- 保持「secret 值永不入日志」的既有约定。

## Technical Considerations

**Provider → env key → 默认 base_url → 协议（数据来源：pi `env-api-keys.ts` 与各 `*.models.ts`）：**

| provider | env key | 默认 base_url | 协议 |
|---|---|---|---|
| anthropic | ANTHROPIC_OAUTH_TOKEN, ANTHROPIC_API_KEY | https://api.anthropic.com/v1 | anthropic |
| openai | OPENAI_API_KEY | https://api.openai.com/v1 | openai |
| ant-ling | ANT_LING_API_KEY | https://api.ant-ling.com/v1 | openai |
| deepseek | DEEPSEEK_API_KEY | https://api.deepseek.com | openai |
| nvidia | NVIDIA_API_KEY | https://integrate.api.nvidia.com/v1 | openai |
| google | GEMINI_API_KEY | https://generativelanguage.googleapis.com/v1beta | openai |
| groq | GROQ_API_KEY | https://api.groq.com/openai/v1 | openai |
| cerebras | CEREBRAS_API_KEY | https://api.cerebras.ai/v1 | openai |
| xai | XAI_API_KEY | https://api.x.ai/v1 | openai |
| openrouter | OPENROUTER_API_KEY | https://openrouter.ai/api/v1 | openai |
| vercel-ai-gateway | AI_GATEWAY_API_KEY | https://ai-gateway.vercel.sh | openai |
| zai | ZAI_API_KEY | https://api.z.ai/api/coding/paas/v4 | openai |
| zai-coding-cn | ZAI_CODING_CN_API_KEY | https://open.bigmodel.cn/api/coding/paas/v4 | openai |
| mistral | MISTRAL_API_KEY | https://api.mistral.ai | openai |
| minimax | MINIMAX_API_KEY | https://api.minimax.io/anthropic | anthropic |
| minimax-cn | MINIMAX_CN_API_KEY | https://api.minimaxi.com/anthropic | anthropic |
| moonshotai | MOONSHOT_API_KEY | https://api.moonshot.ai/v1 | openai |
| moonshotai-cn | MOONSHOT_API_KEY | https://api.moonshot.cn/v1 | openai |
| huggingface | HF_TOKEN | https://router.huggingface.co/v1 | openai |
| fireworks | FIREWORKS_API_KEY | https://api.fireworks.ai/inference | openai |
| together | TOGETHER_API_KEY | https://api.together.ai/v1 | openai |
| opencode | OPENCODE_API_KEY | https://opencode.ai/zen | openai |
| opencode-go | OPENCODE_API_KEY | https://opencode.ai/zen/go | openai |
| kimi-coding | KIMI_API_KEY | https://api.kimi.com/coding | openai |
| xiaomi | XIAOMI_API_KEY | https://api.xiaomimimo.com/v1 | openai |
| xiaomi-token-plan-cn | XIAOMI_TOKEN_PLAN_CN_API_KEY | https://token-plan-cn.xiaomimimo.com/v1 | openai |
| xiaomi-token-plan-ams | XIAOMI_TOKEN_PLAN_AMS_API_KEY | https://token-plan-ams.xiaomimimo.com/v1 | openai |
| xiaomi-token-plan-sgp | XIAOMI_TOKEN_PLAN_SGP_API_KEY | https://token-plan-sgp.xiaomimimo.com/v1 | openai |
| azure-openai-responses | AZURE_OPENAI_API_KEY | 由 AZURE_OPENAI_BASE_URL / RESOURCE_NAME 构造 | openai(azure) |
| amazon-bedrock | AWS_BEARER_TOKEN_BEDROCK / AWS_PROFILE / AWS keys | https://bedrock-runtime.{AWS_REGION}.amazonaws.com | anthropic |
| google-vertex | GOOGLE_CLOUD_API_KEY / ADC | https://{location}-aiplatform.googleapis.com | openai/anthropic |
| cloudflare-workers-ai | CLOUDFLARE_API_KEY (+ACCOUNT_ID) | https://api.cloudflare.com/client/v4/accounts/{id}/ai/v1 | openai |
| cloudflare-ai-gateway | CLOUDFLARE_API_KEY (+ACCOUNT_ID+GATEWAY_ID) | https://gateway.ai.cloudflare.com/v1/{acct}/{gw}/anthropic | anthropic |

- 集成点：`internal/provider/{providers,auth,presets}.go` 与 `cmd/pigo/main.go` 的 `resolveProvider`。
- 现有 `NewBedrockProvider` 构造器已存在但未在 `resolveProvider` 接线，本特性需接线。
- 所有单测不得发起真实网络请求；沿用现有 `faux_provider` / transport 测试风格。

## Success Metrics

- 用户设置任一支持 provider 的 env key + `--provider <name>` 即可发起对话，无需 `--base-url`。
- `--help` 与 README 列出的 provider/env/端点与注册表完全一致。
- 全部新增单测通过，`go vet` 无告警，现有测试无回归。

## Open Questions

- `--provider` 的简写字母选哪个（`-P`？）以避免与现有 flag 冲突？
- google（Gemini generativelanguage）走 OpenAI 兼容端点还是需要专门适配？pi 中为独立 wire，pigo 是否本期以 OpenAI 兼容近似，还是标为 Non-Goal？
- google-vertex 协议随模型而异（Claude on Vertex 走 anthropic，Gemini 走自有），本期是否只支持其中一种？
- opencode / kimi-coding / zai 等端点的路径后缀是否需要在 driver 内补 `/chat/completions` 或 `/messages`，需在实现时按各家文档确认。

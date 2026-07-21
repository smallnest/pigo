# 统一契约与双协议：Provider 层与传输

第 3 章拆完了两层循环，那里留下一个悬而未决的接口：循环需要"把消息、系统提示、工具喂给模型，拿回一段流式回复"，但它从不关心对面是 OpenAI、Anthropic 还是某个本地网关。循环调用的是一个 `StreamFn`，一个签名极简的函数值——真正把这个函数值兑现成"一次 HTTP 请求 + 一段 SSE 流 + 一份解码后的助手消息"的，是 `internal/provider` 这一层。

这一层要同时解决两个层次的差异。往上，它要给循环一个统一的、协议无关的接口：不管背后说的是哪套线上协议，循环拿到的都是同一套 `AssistantMessageEvent` 增量流。往下，它要吃掉真实世界里五花八门的差异——OpenAI 风格的 Chat Completions 与 Anthropic 的 Messages 是两套完全不同的 SSE 事件序列；三十多个内置 Provider 各有各的默认端点、鉴权头与环境变量；Azure、Bedrock、Vertex、Cloudflare 这几个还得从多个环境变量拼出端点来。

本章沿着"从契约到线缆"的顺序解剖这一层：先看统一的 `Provider` 接口与它那套**双失败模型**（决定了错误到底是"返回"还是"随流而下"）；再看两套协议各自的**有状态解码器**如何把线上字节流累积成一条助手消息；接着钻进**共享传输驱动**，看它如何用一套 HTTP + SSE + 重试 + 双看门狗的机制服务所有 Provider；最后回到装配层，看**注册表、精选目录与鉴权**如何把"用户想用哪个模型"翻译成"哪个驱动、说哪套协议、拿哪个密钥"。第 1 章里一笔带过的 `resolveProvider`，到这里会补齐它下游的全部细节。

## 统一契约：StreamFn 与双失败模型

整层 Provider 的顶点是一个函数类型。`internal/provider/provider_interface.go` 把它定义为 `StreamFn`：

```go
// StreamFn produces a provider stream for a model + shaped context. Per the
// contract it returns an error only for early "cannot build the stream"
// failures; all runtime failures ride the returned stream as error events.
type StreamFn func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error)
```

它吃三样东西：模型 id、一个"塑形好"的请求上下文 `LlmContext`，以及一次请求的配置 `StreamConfig`；吐出一条 `*AssistantMessageEventStream`（增量事件流）和一个 `error`。`LlmContext` 就是循环替它准备好的原料——系统提示、已过滤掉纯 UI 消息的消息列表、工具定义：

```go
type LlmContext struct {
	SystemPrompt string
	Messages     agentcore.MessageList
	Tools        []agentcore.AgentTool
}

type StreamConfig struct {
	APIKey        string
	ThinkingLevel agentcore.ThinkingLevel
	// Extra holds provider-specific options; opaque to the loop.
	Extra map[string]any
}
```

这个签名里最容易被忽略的设计，是它对 `error` 的用法。注释把契约（FR-13）写得很死：

> a StreamFn never expresses a request failure by returning an error.

也就是说，**返回的 `error` 不是用来报告请求失败的**。它只保留给一种情况：连流都建不起来（"could not even build the stream"）——比如缺少必需参数、请求对象根本没法构造。一旦流已经开始，任何运行期失败（网络断了、上游 500、SSE 解不动、看门狗超时）都不走 `error` 这条路，而是作为流里的一个**终止事件**顺着流本身送下来。

这就是贯穿全章的**双失败模型**。为什么要把失败拆成两类？因为流式回复天然是"边收边处理"的：模型可能已经吐了半段文本、发起了一个工具调用，然后才断线。如果把断线表达成一个 Go error 从 `StreamFn` 返回，调用方就丢掉了那半段已经到手的内容；把它表达成流里的终止事件，调用方就能拿到"到目前为止累积的部分消息 + 一个 error 标记"，语义完整得多。第 3 章的循环正是依赖这一点：它消费流直到终止事件，从终止事件里读出 `StopReason` 来决定这一轮怎么收尾。

### 增量事件：一个密封接口

流里流动的是什么？是 `AssistantMessageEvent`，一个密封接口（sealed interface，第 2 章讲过这个手法）：

```go
type AssistantMessageEvent interface {
	isAssistantMessageEvent()
	// EventKind returns the delta discriminant.
	EventKind() string
}

// AssistantMessageEvent kinds.
const (
	StreamEventStart    = "start"
	StreamEventText     = "text"
	StreamEventThinking = "thinking"
	StreamEventToolCall = "toolcall"
	StreamEventDone     = "done"
	StreamEventError    = "error"
)
```

未导出的 `isAssistantMessageEvent()` 方法把实现者锁死在本包内，外部无法伪造新的事件类型——循环 `switch` 事件时可以确信自己面对的是一个封闭集合。六种事件各是一个小结构体，其中前四种（start/text/thinking/toolcall）都只携带一个字段 `Partial`：**到当前增量为止累积出来的整条助手消息**。

```go
// StreamTextEvent carries the partial message after a text delta.
type StreamTextEvent struct{ Partial agentcore.AssistantMessage }
```

这个选择值得停下来体会：每个增量事件带的不是"这次新增的那几个字符"，而是"截至此刻的完整消息快照"。UI 拿到任何一个事件都能直接整体重绘，不必自己把碎片拼起来，拼接的活儿全被解码器包办了。代价是每个事件都复制一份消息，但对话消息通常不大，这点开销换来的是消费端的简单。

真正携带最终结果的是两个终止事件：

```go
// StreamDoneEvent is the terminal success event; Message is the final response.
type StreamDoneEvent struct{ Message agentcore.AssistantMessage }

// StreamErrorEvent is the terminal failure event; Message carries the terminal
// assistant message (stopReason=error/aborted + errorMessage).
type StreamErrorEvent struct {
	Message agentcore.AssistantMessage
	Err     error
}
```

`StreamDoneEvent` 是成功收尾，`StreamErrorEvent` 是失败收尾。注意后者也带一条 `Message`——它的 `StopReason` 被置成 `error` 或 `aborted`，`ErrorMessage` 里写着人类可读的原因。这正是双失败模型的"流内"那一半：错误被包装成一条合法的助手消息塞进流里，而不是抛出来。

### 事件流与完成判定

这条流本身不是一个新类型，而是第 2 章那个通用 `EventStream` 的一次具化（specialization）：

```go
// AssistantMessageEventStream is the provider-level stream: deltas of type
// AssistantMessageEvent with a final AssistantMessage result. isComplete fires
// on done/error; extractResult takes the terminal event's message.
type AssistantMessageEventStream = agentcore.EventStream[AssistantMessageEvent, agentcore.AssistantMessage]
```

它是"事件类型为 `AssistantMessageEvent`、最终结果类型为 `AssistantMessage`"的 `EventStream`。`EventStream` 早在第 2 章就设计了两个可选回调 `IsComplete` 与 `ExtractResult`：前者判断某个事件是不是终止事件，后者从终止事件里提取最终结果。`NewAssistantMessageEventStream` 把这两个回调按 Provider 层的语义接好：

```go
func NewAssistantMessageEventStream(buffer int) *AssistantMessageEventStream {
	s := agentcore.NewEventStream[AssistantMessageEvent, agentcore.AssistantMessage](buffer)
	s.IsComplete = func(e AssistantMessageEvent) bool {
		k := e.EventKind()
		return k == StreamEventDone || k == StreamEventError
	}
	s.ExtractResult = func(e AssistantMessageEvent) agentcore.AssistantMessage {
		switch ev := e.(type) {
		case StreamDoneEvent:
			return ev.Message
		case StreamErrorEvent:
			return ev.Message
		default:
			return agentcore.AssistantMessage{}
		}
	}
	return s
}
```

"done 或 error 即完成"，"从 done/error 事件里取那条 `Message`"——两句话就把成功与失败统一进了同一个终止语义。消费方调用 `stream.Result(ctx)` 拿到的，永远是那条终止消息，无论它是自然收尾还是错误收尾。这也是为什么错误必须做成 `StreamErrorEvent` 而不是 Go error：只有这样它才能走通 `IsComplete` → `ExtractResult` → `Result` 这条既有链路，被当作"一个有结果的完成"来对待。

### Provider 接口与它的两副面孔

`StreamFn` 是给循环用的函数式契约；而 `internal/provider/provider_interface.go` 里的 `Provider` 是给实现者用的对象式契约。两者描述的是同一件事的两个视角：

```go
type Provider interface {
	// Name returns the provider's identifier (matches Model.Provider).
	Name() string
	// Models lists the models this provider can serve.
	Models() []Model
	// StreamCompletion streams a completion for req. Per the dual failure model
	// it returns an error only for the earliest "cannot build the stream" case;
	// all runtime failures ride the returned stream as a terminal error event.
	StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error)
}
```

`StreamCompletion` 的失败模型和 `StreamFn` 一字不差——这不是巧合，因为二者之间有一座直连桥：

```go
// StreamFnFromProvider adapts a Provider to the loop's StreamFn contract so a
// Provider can drive streamAssistantResponse directly.
func StreamFnFromProvider(p Provider) StreamFn {
	return func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
		return p.StreamCompletion(ctx, CompletionRequest{Model: model, Context: llm, Config: cfg})
	}
}
```

第 1 章 `newRunConfig` 里那句 `Stream: provider.StreamFnFromProvider(prov)` 到这里终于闭环：装配期把一个具体 `Provider` 适配成循环要的 `StreamFn`，因为两者失败模型完全一致，适配就是一次朴素的转发，把三个参数塞进一个 `CompletionRequest` 结构体而已。

接口里还藏着一个协议无关的元数据类型 `Model`，它让循环与 UI 能在"不知道背后是哪个 Provider"的前提下推理一个模型的能力：

```go
type Model struct {
	Provider         string `json:"provider"`
	ID               string `json:"id"`
	DisplayName      string `json:"displayName,omitempty"`
	ContextWindow    int    `json:"contextWindow,omitempty"`
	MaxOutputTokens  int    `json:"maxOutputTokens,omitempty"`
	SupportsThinking bool   `json:"supportsThinking,omitempty"`
	SupportsTools    bool   `json:"supportsTools,omitempty"`
	SupportsImages   bool   `json:"supportsImages,omitempty"`
	ThinkingLevels   agentcore.ThinkingLevelMap `json:"-"`
}
```

`ContextWindow` 会喂给第 6 章的压缩逻辑判断何时该压缩。`SupportsImages` 值得单说：注释明确写着，当它为 `false` 而请求里带了图片时，Provider 会把这报成一个硬错误，而不是悄悄丢掉——宁可让用户知道"这个模型看不见图"，也不假装成功。这个细节稍后在 `checkImageSupport` 里会再遇到。

## 两套协议：有状态解码器

统一接口之下，真正吃掉协议差异的是**解码器**。传输层（下一节详述）把每个 Provider 都退化成一个满足 `Decoder` 接口的有状态对象：

```go
type Decoder interface {
	// Decode turns one SSE data payload into zero or more StreamEvents.
	Decode(payload []byte) ([]StreamEvent, error)
	// Finish flushes any trailing state, returning a final batch of events.
	Finish() ([]StreamEvent, error)
}
```

`StreamEvent` 只是 `AssistantMessageEvent` 的传输层别名（`type StreamEvent = AssistantMessageEvent`），刻意复用同一套事件类型，不另立门户。传输层每收到一个完整的 SSE data 载荷就调一次 `Decode`，流结束时调一次 `Finish` 让解码器冲刷缓冲。pigo 内置两个解码器，对应两套主流协议。

### OpenAI 兼容：累积式 chunk 解码

`internal/provider/openai.go` 里的 `OpenAIDecoder` 解 OpenAI Chat Completions 的 SSE 流——这也是绝大多数第三方网关（OpenRouter、Groq、Together、本地 server……）说的方言。OpenAI 把回复拆成一串 `chat.completion.chunk`：

```
{"choices":[{"delta":{"role":"assistant"}}]}                 → 首块
{"choices":[{"delta":{"content":"Hel"}}]}                    → 文本增量
{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1",
    "function":{"name":"f","arguments":"{\"a\":"}}}]}]}       → 工具调用增量
{"choices":[{"finish_reason":"tool_calls"}]}                 → 停止原因
{"usage":{"prompt_tokens":10,"completion_tokens":5}}         → 最终用量
[DONE]                                                        → 传输层终止符
```

难点在工具调用：一次工具调用的 `id`、`name`、`arguments` 分散在多个 chunk 里，`arguments` 更是一串 JSON 片段需要拼接。解码器为此维护一份按增量 index 索引的累积状态：

```go
type openaiToolCall struct {
	id   string
	name string
	args strings.Builder
}

type OpenAIDecoder struct {
	text      strings.Builder
	toolCalls map[int]*openaiToolCall
	toolOrder []int // tool-call indices in first-seen order

	responseID    string
	responseModel string
	inputTokens   int
	outputTokens  int
	stopReason    string
	done          bool
}
```

`Decode` 把一个 chunk 拆开，文本增量追加进 `text`、工具增量并进对应的 `openaiToolCall`，每处理一个增量就产出一个携带最新快照的事件：

```go
for _, choice := range chunk.Choices {
	if choice.Delta.Content != "" {
		d.text.WriteString(choice.Delta.Content)
		events = append(events, StreamTextEvent{Partial: d.partial()})
	}
	for _, tc := range choice.Delta.ToolCalls {
		d.applyToolDelta(tc)
		events = append(events, StreamToolCallEvent{Partial: d.partial()})
	}
	if choice.FinishReason != "" {
		d.stopReason = mapOpenAIFinishReason(choice.FinishReason)
	}
}
```

`applyToolDelta` 是"按 index 归并片段"的核心：首次见到某个 index 就建一个累积器并记进 `toolOrder`（保持首次出现的顺序），之后每来一个片段就补 `id`/`name`、追加 `arguments`：

```go
func (d *OpenAIDecoder) applyToolDelta(tc openaiToolDelta) {
	call := d.toolCalls[tc.Index]
	if call == nil {
		call = &openaiToolCall{}
		d.toolCalls[tc.Index] = call
		d.toolOrder = append(d.toolOrder, tc.Index)
	}
	if tc.ID != "" {
		call.id = tc.ID
	}
	if tc.Function.Name != "" {
		call.name = tc.Function.Name
	}
	call.args.WriteString(tc.Function.Arguments)
}
```

无论何时，`partial()` 都能把当前累积状态物化成一条完整的 `AssistantMessage`：文本块在前，工具调用块按 index 升序在后。这里有个细节——空的 `arguments` 会被补成 `{}`，因为下游解析工具参数时需要一个合法的 JSON 对象：

```go
args := json.RawMessage(strings.TrimSpace(call.args.String()))
if len(args) == 0 {
	args = json.RawMessage("{}")
}
msg.Content = append(msg.Content, agentcore.NewToolCallContent(call.id, call.name, args))
```

停止原因也要翻译。OpenAI 的 `finish_reason` 与 pigo 内部的 `StopReason` 不是一套词汇，`mapOpenAIFinishReason` 负责映射，且对未知值一律回落到 `end_turn`（一个自然的、非错误的收尾）：

```go
func mapOpenAIFinishReason(reason string) string {
	switch reason {
	case "length":
		return agentcore.StopReasonLength
	case "tool_calls", "function_call":
		return agentcore.StopReasonToolUse
	case "stop":
		return agentcore.StopReasonEndTurn
	default:
		return agentcore.StopReasonEndTurn
	}
}
```

`Finish` 处理一种边界：如果流干净结束了却没显式给出终止事件（有些网关如此），就冲刷出一个 `StreamDoneEvent`，避免那半段回复白白丢掉。`chunk.Error != nil` 时——有些网关把错误对象直接塞进流里——`Decode` 返回一个 error，由传输层转成 `StreamErrorEvent`。整个解码器"永不 panic"：畸形载荷、内联错误都走返回 error 这条受控路径。

### Anthropic Messages：事件序列解码

`internal/provider/anthropic.go` 的 `AnthropicDecoder` 面对的是另一套完全不同的 SSE 协议。Anthropic 不是发一串同构的 chunk，而是发一个**有类型的事件序列**：

```
message_start        → 播下 id/model 与初始用量（输入 token）
content_block_start  → 在某个 index 上开启一个 text / thinking / tool_use 块
content_block_delta  → text_delta / thinking_delta / signature_delta /
                       input_json_delta 追加到打开的块上
content_block_stop   → 关闭块（tool_use 的 JSON 在此解析）
message_delta        → 携带最终 stop_reason 与输出 token 用量
message_stop         → 终止；把累积的消息作为 done 发出
error                → 运行期错误载荷 → 终止 error 事件
```

模型可以在一条回复里交错产出思考块、文本块、工具调用块，每个块占一个 index。解码器按 index 维护每个块的累积状态：

```go
type anthropicBlock struct {
	kind        string // "text" | "thinking" | "tool_use" | "redacted_thinking"
	text        strings.Builder
	thinking    strings.Builder
	thinkingSig string
	textSig     string
	toolID      string
	toolName    string
	toolJSON    strings.Builder
	redacted    bool
}
```

`Decode` 是一个按事件 `type` 分派的 `switch`，把每种事件路由到对应处理函数：

```go
switch ev.Type {
case "message_start":
	return d.onMessageStart(ev), nil
case "content_block_start":
	return d.onBlockStart(ev), nil
case "content_block_delta":
	return d.onBlockDelta(ev), nil
case "content_block_stop":
	// Nothing to emit on stop; the block is already reflected in the partial.
	return nil, nil
case "message_delta":
	return d.onMessageDelta(ev), nil
case "message_stop":
	return d.finishDone(), nil
case "ping":
	return nil, nil
case "error":
	// ... 组装错误消息并返回 error ...
default:
	// Unknown event types are ignored (forward-compatible).
	return nil, nil
}
```

值得注意的几处协议特性：`content_block_delta` 里的 `signature_delta` 携带思考块的签名（Anthropic 的扩展思考需要签名回传），它被并进它所依附的思考块的 `thinkingSig`；`input_json_delta` 是工具参数的 JSON 片段，追加进 `toolJSON`；`onBlockDelta` 遇到一个未曾见过的 index 时不丢数据，而是补开一个裸块。`ping` 和未知事件都安全忽略——后者带来了**向前兼容**：Anthropic 将来新增事件类型不会让解码器崩溃。

`partial()` 与 OpenAI 版同构，只是按 Anthropic 的块语义物化：`thinking`/`redacted_thinking` 变思考内容（带签名与 redacted 标记），`tool_use` 变工具调用内容，其余按文本处理。停止原因映射 `mapAnthropicStopReason` 把 `max_tokens` 映射成 `StopReasonLength`、`tool_use` 映射成 `StopReasonToolUse`，同样对未知值回落 `end_turn`。

两个解码器摆在一起看，就能体会统一接口的价值：它们输入的协议截然不同（一个是同构 chunk 流，一个是有类型事件序列），累积状态的组织方式也不同，但输出的都是同一套 `AssistantMessageEvent`，都遵守"永不 panic、错误走返回值、`Finish` 冲刷尾巴"的同一份 `Decoder` 契约。循环消费流时根本分不出对面是谁，这正是第 3 章那份"协议无关"承诺的兑现处。

## 共享传输驱动：一套机制服务所有 Provider

解码器只管"把一个 SSE 载荷翻译成事件"，剩下的脏活——发 HTTP 请求、按行解析 SSE、失败重试、超时看门狗、把错误包成终止事件——全被抽进 `internal/provider/transport.go` 这一层。它的设计目标很明确：**每个 Provider 都退化成一个 `Decoder`，其余一律共享**，谁也不用自己写一遍 HTTP/SSE 处理。这直接对标了 pi 里的 `providerio`。

入口是 `StreamRequest`：

```go
func StreamRequest(ctx context.Context, cfg TransportConfig) (*AssistantMessageEventStream, error) {
	if cfg.NewRequest == nil {
		return nil, errors.New("transport: NewRequest is required")
	}
	if cfg.Decoder == nil {
		return nil, errors.New("transport: Decoder is required")
	}
	// ... client / maxRetries 默认值 ...

	// Connect once up front so a "cannot even build the stream" failure surfaces
	// as a returned error (the only early-error case per FR-13).
	resp, err := connect(ctx, client, cfg.NewRequest, maxRetries)
	if err != nil {
		return nil, err
	}

	stream := NewAssistantMessageEventStream(0)
	go pump(ctx, stream, resp, cfg.Decoder)
	return stream, nil
}
```

它的配置 `TransportConfig` 里最讲究的字段是 `NewRequest`：

```go
type TransportConfig struct {
	Client *http.Client
	// NewRequest builds a fresh *http.Request for each connection attempt. It is
	// called once per connect (initial + reconnects) so retries never replay a
	// consumed body — the caller owns idempotent request construction.
	NewRequest func(ctx context.Context) (*http.Request, error)
	Decoder Decoder
	MaxConnectRetries int
}
```

`NewRequest` 是一个**每次连接都重新构造请求的工厂函数**，而不是一个现成的 `*http.Request`。原因很实在：`http.Request` 的 body 是个只能读一次的 reader，重试时若复用同一个请求，body 早被上一次读空了。让调用方提供一个"每次造一个全新请求"的工厂，重试就永远不会重放一个已消费的 body——这把幂等请求构造的责任明确交给了调用方。

`StreamRequest` 的结构精确对应双失败模型：`connect` 先**同步地**把初始连接建起来，如果连都连不上（请求构造失败、初始连接始终建不起来），直接返回 error——这是唯一的"建不起流"早错误。一旦 `connect` 成功，就起一个 `pump` goroutine 异步驱动读取循环，`StreamRequest` 立刻返回流对象；此后一切失败都由 `pump` 包成终止事件送进流里。

### connect：只在服务器明说时重试

```go
func connect(ctx context.Context, client *http.Client, newReq func(context.Context) (*http.Request, error), maxRetries int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := newReq(ctx)
		if err != nil {
			return nil, fmt.Errorf("transport: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = classifyTransportError(err)
			if !isRetryableNetErr(err) || attempt == maxRetries {
				return nil, lastErr
			}
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == statusTooManyRequestsCF {
			wait := retryAfter(resp.Header)
			resp.Body.Close()
			lastErr = fmt.Errorf("transport: upstream %d", resp.StatusCode)
			if attempt == maxRetries {
				return nil, lastErr
			}
			if !sleepBackoff(ctx, attempt, wait) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
			resp.Body.Close()
			return nil, fmt.Errorf("transport: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, lastErr
}
```

重试策略是刻意保守的：只有服务器**明确发出可重试信号**时才重试——429（Too Many Requests）、503（Service Unavailable），以及 529（Cloudflare 的"站点过载"，某些上游也用）。网络层错误只在 `isRetryableNetErr` 判定为超时时才重试。为什么这么克制？因为重试只发生在**连接建立阶段**，此时还没有任何流被消费，重放请求绝对安全；一旦流开始，重试就意味着可能重放已消费的部分，宁可不重试。其他 4xx/5xx 直接读一段错误 body（限长 `errorBodyLimit` = 4096 字节，防止超大错误页撑爆内存）作为早错误返回。

`sleepBackoff` 优先采用服务器给的 `Retry-After`（`retryAfter` 解析秒数或 HTTP-date），否则退避到指数间隔（`1<<attempt` 秒），且退避期间尊重 `ctx` 取消。

### pump：SSE 读循环与双看门狗

真正的流处理在 `pump` 里。它做三件事：按 SSE 规则解析行、用两个看门狗防止卡死、把一切失败包成 `StreamErrorEvent`——并且**无论如何都会关闭流**（`defer stream.Close()`）。

先看两个看门狗。为什么要两个？

```go
idle := idleTimeout()
// content-stall watchdog is slightly slacker than idle (idle × stallFactor)
// so a slow but progressing stream is not killed by the stall guard.
stall := time.Duration(float64(idle) * stallFactor)
```

- **idle 看门狗**（默认 5 分钟，可用环境变量 `PIGO_STREAM_IDLE_TIMEOUT` 覆盖）：只要**一段时间内一个字节都没收到**就开火。它防的是"连接挂着但对面彻底哑了"。
- **stall（内容停滞）看门狗**（`idle × 1.2`，比 idle 略松）：防的是另一种病态——对面一直发 keep-alive 注释或空行让 idle 一直不触发，却始终不产出真正的内容事件。

两者的更新时机不同，正是它们能各司其职的关键：**收到任何一行**（哪怕是注释、空行）都重置 idle 计时器；只有**冲刷出一个真正的事件**（内容有进展）才重置 stall 计时器。于是"活着但没内容"会被 stall 逮到，"彻底静默"会被 idle 逮到。

读取本身放在一个单独的 goroutine `readLines` 里，通过 channel 把行喂给 `pump`。这样 `pump` 就能在一个 `select` 里同时等待：新行、idle 超时、stall 超时、读结束、以及 `ctx` 取消：

```go
for {
	select {
	case <-ctx.Done():
		fail("stream aborted", ctx.Err())
		return
	case <-idleTimer.C:
		fail("idle timeout: no data received", errStreamIdle)
		return
	case <-stallTimer.C:
		fail("content stall timeout", errStreamStall)
		return
	case err := <-readErr:
		if err != nil && !errors.Is(err, io.EOF) {
			fail("read error: "+classifyTransportError(err).Error(), err)
			return
		}
		// Clean EOF: flush any buffered payload, then finish the decoder.
		if !flush() {
			return
		}
		finalEvents, ferr := dec.Finish()
		if ferr != nil {
			fail("finish error: "+ferr.Error(), ferr)
			return
		}
		emit(finalEvents)
		return
	case line, ok := <-lines:
		if !ok {
			continue
		}
		resetTimer(idleTimer, idle)
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			// Blank line = event boundary: flush accumulated data.
			if !flush() {
				return
			}
			resetTimer(stallTimer, stall)
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive: ignore payload, watchdog already reset.
		case strings.HasPrefix(line, "data:"):
			dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// Non-data field (event:, id:, etc.) — ignored for our decoders.
		}
	}
}
```

这段 `select` 就是 SSE 协议的解析器。SSE 的规则是：`data:` 行累积成载荷，**空行是事件边界**，冒号开头是注释。`pump` 严格照办——`data:` 行把内容累进 `dataBuf`，空行触发 `flush()` 把累积的载荷交给解码器，注释和其他字段（`event:`、`id:`）直接忽略。`flush` 里还处理了 OpenAI 的 `[DONE]` 传输层终止符（直接跳过，不喂给解码器）：

```go
flush := func() bool {
	if dataBuf.Len() == 0 {
		return true
	}
	payload := dataBuf.String()
	dataBuf.Reset()
	if payload == "[DONE]" {
		return true
	}
	events, err := dec.Decode([]byte(payload))
	if err != nil {
		fail("decode error: "+err.Error(), err)
		return false
	}
	return emit(events)
}
```

`fail` 是双失败模型的落地点：把任何运行期错误包成一条 `StopReason=error` 的助手消息，作为 `StreamErrorEvent` 发进流里——它用 `context.Background()` 而非 `watchCtx` 来 Emit，确保即便是"因取消而失败"也能把这条终止事件送出去：

```go
fail := func(msg string, err error) {
	stream.Emit(context.Background(), StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: msg,
		},
		Err: err,
	})
}
```

还有一个容易被忽视但很重要的收尾细节：`readLines` 靠一个 `done` channel 来知道自己该停了。`pump` 一返回（无论是看门狗开火还是被取消），`defer close(done)` 就通知读 goroutine 别再往 `lines` 里发——否则那个 goroutine 会永远阻塞在 send 上，泄漏出去。这类"goroutine 生命周期对齐"的细节，是并发流处理里最容易出错的地方，也最能看出工程功力。

## 从 Provider 到线缆：两种驱动

有了统一接口、解码器和传输层，具体 Provider 的实现就变得很薄。`internal/provider/providers.go` 用两种"驱动"（driver）覆盖了所有内置 Provider——它们都实现 `Provider` 接口，都把请求交给 `StreamRequest`，区别只在"编码成哪套线上格式 + 配哪个解码器"。

### OpenAI 兼容驱动

`openAICompatDriver` 是所有 OpenAI 兼容 Provider 的共同底座：

```go
type openAICompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// requiresAuth reports whether an Authorization: Bearer header is sent.
	// Ollama (local) needs none; OpenRouter does.
	requiresAuth bool
	// extraHeaders are attached to every request (e.g. OpenRouter attribution).
	extraHeaders map[string]string
}
```

`StreamCompletion` 的骨架非常能说明这一层的分工：

```go
func (d *openAICompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if d.requiresAuth && strings.TrimSpace(req.Config.APIKey) == "" {
		// Early "cannot build the stream": reference the provider, never a value.
		return nil, fmt.Errorf("%s: missing API key", d.name)
	}
	if err := checkImageSupport(d.name, req.Model, d.models, req.Context.Messages); err != nil {
		return nil, err
	}
	body, err := encodeOpenAIRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if d.requiresAuth {
			httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)
		}
		for k, v := range d.extraHeaders {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{NewRequest: newReq, Decoder: NewOpenAIDecoder()})
}
```

它把三类"建不起流"的早错误挡在 `StreamRequest` 之前：缺密钥、模型看不见图（`checkImageSupport`）、请求 body 序列化失败。这些都在流开始之前，符合早错误契约。注意缺密钥的错误只提 Provider 名（`d.name`），**从不带密钥值**——密钥安全是这一层的红线。三关都过之后，它把一个 `newReq` 工厂和一个新鲜的 `NewOpenAIDecoder()` 交给 `StreamRequest`，剩下的全交给共享传输层。

请求 body 由 `encodeOpenAIRequest` 塑形：系统提示编成 `role:"system"` 消息、开启 `stream` 与 `include_usage`、工具编成 function schema。`encodeOpenAIMessage` 处理每类消息的映射，其中助手消息可能同时带文本内容和 `tool_calls`，工具结果编成 `role:"tool"` 且带 `tool_call_id`。多模态内容由 `openAIUserContent` 处理：无图时塌缩成纯字符串（多数网关期待的常见形态），有图时才展开成 `image_url` 数组，图以 `data:<mime>;base64,<data>` 的 data URI 承载。

### Anthropic 兼容驱动

`anthropicCompatDriver` 是对称的另一半，POST Anthropic Messages 格式、配 `AnthropicDecoder`。它比 OpenAI 版多两个可插拔点：

```go
type anthropicCompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// path is the endpoint path appended to baseURL (Bedrock's invoke path
	// embeds the model id, so it is derived per request).
	pathFor func(model string) string
	// authHeader sets provider auth on the request (never logs the value).
	authHeader func(req *http.Request, apiKey string)
}
```

`pathFor` 让 Bedrock 这种"端点路径里嵌了模型 id"的场景能自定义路径；`authHeader` 让不同 Anthropic 系 Provider 用不同的鉴权头。请求编码 `encodeAnthropicRequest` 遵守 Messages 的约定：系统提示是顶层 `system` 字段（不是一条消息），工具结果编成 `role:"user"` 携带 `tool_result` 块（Anthropic 的惯例），且**必须给 `max_tokens`**——未知时兜底 4096。

`checkImageSupport` 值得单独看一眼，它把前面 `Model.SupportsImages` 的承诺落了地：

```go
func checkImageSupport(providerName, model string, models []Model, msgs []agentcore.Message) error {
	if !contextHasImage(msgs) {
		return nil
	}
	for _, m := range models {
		if m.ID == model {
			if !m.SupportsImages {
				return fmt.Errorf("%s: model %q does not support image input", providerName, model)
			}
			return nil
		}
	}
	return nil
}
```

请求里没图就直接放行；有图且模型明确声明不支持，就报一个清楚的错误，把"图被悄悄丢弃"变成一条用户能看懂的消息。而目录里根本查不到的模型则**从宽处理**（能力未知，交给上游自己校验）——这是一种务实的容错：pigo 不假装自己知道每一个模型的能力。

### 构造器：一张表收敛所有差异

早期这些 Provider 各有一个近乎重复的构造器。现在 OpenAI 兼容那一族被收敛到一张 `openAICompatPreset` 表上，"加一个网关"退化成"加一行表项 + 一个薄薄的导出包装"：

```go
type openAICompatPreset struct {
	name         string
	defaultURL   string // "" ⇒ no default; baseURL must be supplied by the caller
	requiresAuth bool
	extraHeaders map[string]string
}
```

于是 `NewOpenRouterProvider`（参照级网关，带 OpenRouter 的归属头）、`NewOllamaProvider`（本地、免鉴权）、`NewNvidiaProvider`（托管 NIM、Bearer）、`NewOpenAICompatibleProvider`（`--protocol=openai` 的目标，无默认端点、中性名 `openai`）都只是往这张表填不同参数。Anthropic 系同理由 `newAnthropicCompat` 收敛，`anthropicAuthHeaderFor` 按注册表里的 `AuthScheme` 返回对应的鉴权头设置函数——`AuthBearer` 走 `Authorization: Bearer`，其余（含 `x-api-key`）走 `x-api-key` + `anthropic-version` 头。第 1 章那个 `resolveNamedProvider` 调用的就是这批构造器。

## 注册表、精选目录与鉴权

到这里我们已经有了"给一个端点、协议、密钥就能跑起来"的驱动。但用户敲的是 `--model` 或 `--provider`，不是端点和协议。把前者翻译成后者的知识库，是三张互相配合的表：**注册表**（谁是内置 Provider）、**精选目录**（推荐哪些模型）、**鉴权**（密钥从哪来）。

### 注册表：内置 Provider 的唯一真相源

`internal/provider/registry.go` 是所有内置 Provider 元数据的**单一真相源**（single source of truth）。每个 Provider 是一条 `ProviderSpec`：

```go
type ProviderSpec struct {
	Name           string
	EnvVars        []string          // API key 环境变量，按优先级
	DefaultBaseURL string            // 默认端点，可能是含占位符的模板
	Protocol       string            // "openai" | "anthropic"
	AuthScheme     string            // "bearer"|"x-api-key"|"aws"|"azure"|"special"
	ExtraHeaders   map[string]string
	BaseURLEnvVars []string          // 特定的 base-URL 覆盖环境变量
}
```

`providerRegistry` 是一个稳定有序的切片，列着三十多个内置 Provider——从 `anthropic`、`openai`、`deepseek`、`groq` 到 `zai`、`minimax`、`moonshotai`、`xiaomi` 各种区域变体。挑几条对照看：

```go
{
	Name:           "anthropic",
	EnvVars:        []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
	DefaultBaseURL: anthropicBaseURL, // https://api.anthropic.com/v1
	Protocol:       ProtocolAnthropic,
	AuthScheme:     AuthXAPIKey,
},
{
	Name:           "deepseek",
	EnvVars:        []string{"DEEPSEEK_API_KEY"},
	DefaultBaseURL: "https://api.deepseek.com",
	Protocol:       ProtocolOpenAI,
	AuthScheme:     AuthBearer,
},
{
	Name:           "minimax",
	EnvVars:        []string{"MINIMAX_API_KEY"},
	DefaultBaseURL: "https://api.minimax.io/anthropic",
	Protocol:       ProtocolAnthropic,
	AuthScheme:     AuthXAPIKey,
},
```

这张表的信息密度值得停一下。`anthropic` 的 `EnvVars` 有三个，按优先级排列——OAuth token 优先于静态 API key，且兼容 `CLAUDE_API_KEY` 这个别名。`minimax` 是个有意思的例子：它是国产网关，却说 **Anthropic 协议**（端点 `/anthropic` 结尾）、用 `x-api-key` 鉴权——这说明"协议"和"厂商"是正交的两个维度，一个国产模型完全可以用 Anthropic 的线上格式。注册表把这些差异全部数据化，下游的鉴权、`--provider` 解析、base-URL 覆盖全都**读**这张表，而不是各自硬编码。

注册表刻意只存环境变量的**名字**，从不存密钥值——安全边界从数据结构层面就划清了。它对外暴露三个查询：`LookupProviderSpec`（按名查，O(1)，用 init 期建好的索引 map）、`ProviderSpecs`（按显示序列全部）、`ProviderNames`（全部名字）。第 1 章 `--help` 里那段 "Supported providers" 列表就由 `ProviderNames` 实时生成，所以永远不会和代码漂移。

### 精选目录：给人看的菜单

注册表回答"哪些 Provider 存在"，`internal/provider/presets.go` 的**精选目录**回答"推荐用哪些具体模型"。它对标 pi 的模型选择器，是一份人类友好的菜单：

```go
type PresetModel struct {
	Provider    string // owning provider name
	ID          string // 传给 provider 的模型 id
	DisplayName string // 选择器里显示的友好标签
}
```

`PresetCatalog` 按 Provider 分组列出精选模型——OpenRouter 的 GPT-4o、Claude、Gemini、Llama，NVIDIA NIM 的一串托管模型，DeepSeek、Groq、Kimi、GLM、MiMo 等等，还专门列了 OpenRouter 的 `:free` 免费档。注释里点破了它和"前缀映射"的关系：

> The naive prefix-based mapping (ollama/…) still works for arbitrary ids; the preset catalog is the "menu" of vetted choices surfaced to the user.

也就是说，精选目录不是**唯一**能用的模型清单——任意合法 id 通过前缀启发式仍然能跑（第 1 章 `resolveProvider` 里 `ollama/`、`nvidia/` 前缀那段）；精选目录只是把"经过筛选、拿来即用"的组合摆到用户面前。`LookupPreset(id)` 把一个选中的 id 反查回它的归属 Provider——第 1 章 `resolveProvider` 优先级链里"先查精选目录"那一步，查的就是这里。同样地，精选目录也不带任何密钥。

### 鉴权：三层凭据解析

`internal/provider/auth.go` 的 `CredentialStore` 负责回答最后一个问题——密钥从哪来。它满足第 1 章 `newRunConfig` 里 `GetAPIKey: creds.GetAPIKey` 那个 `func(ctx, provider) string` 的形状，让循环能按 Provider 名**惰性**取密钥。解析分三层，`GetAPIKey` 的顺序写得清清楚楚：

```go
func (c *CredentialStore) GetAPIKey(ctx context.Context, provider string) string {
	// ... 读 src / override / cfgKey ...
	if src != nil {
		if tok, err := src.Token(ctx); err == nil && tok != "" {
			return tok
		}
		// Refresh failed → fall through to static layers.
	}
	if override != "" {
		return override
	}
	if env := envAPIKey(provider); env != "" {
		return env
	}
	return cfgKey
}
```

顺序是：**OAuth token（到期自动刷新）→ 显式覆盖（`--api-key`）→ 环境变量 → 配置文件**。这里有几处深思熟虑的设计。其一，OAuth 刷新失败不返回一个带密钥的错误，而是**静默回落**到静态层——空返回让调用方能继续尝试静态密钥，永不泄漏 secret。其二，环境变量解析 `envAPIKey` 把注册表当真相源：

```go
func envAPIKey(provider string) string {
	if spec, ok := LookupProviderSpec(provider); ok {
		for _, name := range spec.EnvVars {
			if v := os.Getenv(name); v != "" {
				return v
			}
		}
	}
	// Generic fallback for unknown providers or when no registered var is set.
	generic := strings.ToUpper(provider) + "_API_KEY"
	return os.Getenv(generic)
}
```

它按注册表里 `EnvVars` 的优先级挨个查（这就是为什么 `anthropic` 的 `ANTHROPIC_OAUTH_TOKEN` 会先于 `ANTHROPIC_API_KEY` 命中），查不到才回落到通用的 `<PROVIDER>_API_KEY` 约定。其三，OAuth 的 `TokenSource` 会在 token 过期前一段"余量"（默认 30 秒 `Leeway`）就视其为过期，避免刷新恰好卡在到期边界上和请求打架。整份文件的安全纪律和注册表一致：secret 值从不写进日志、从不嵌进错误消息，错误只引用 key 的名字或 Provider 名。

### 特殊鉴权：从多个环境变量拼出端点

绝大多数 Provider 靠一个 Bearer 或 x-api-key 头就够了。但 Azure OpenAI、Amazon Bedrock、Google Vertex、Cloudflare（Workers AI + AI Gateway）这几个不行——它们的端点要**从多个环境变量拼出来**，或需要非标准的凭据路径。`internal/provider/special_auth.go` 为它们提供专门的解析器。`IsSpecialAuthProvider` 先把它们识别出来：

```go
func IsSpecialAuthProvider(spec ProviderSpec) bool {
	switch spec.AuthScheme {
	case AuthAzure, AuthAWS, AuthSpecial:
		return true
	}
	return strings.HasPrefix(spec.Name, "cloudflare-")
}
```

`ResolveSpecialProvider` 按名字分派到各自的解析函数。它们的共同套路是：**先校验必需参数，缺哪个就报哪个环境变量的名字，且此处不发任何网络请求**。以 Cloudflare AI Gateway 为例：

```go
func resolveCloudflareAIGateway(_ ProviderSpec, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("CLOUDFLARE_API_KEY")) == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_API_KEY")
	}
	account := strings.TrimSpace(env("CLOUDFLARE_ACCOUNT_ID"))
	if account == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_ACCOUNT_ID")
	}
	gateway := strings.TrimSpace(env("CLOUDFLARE_GATEWAY_ID"))
	if gateway == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_GATEWAY_ID")
	}
	baseURL := strings.TrimSpace(flagBaseURL)
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://gateway.ai.cloudflare.com/v1/%s/%s/anthropic", account, gateway)
	}
	return NewAnthropicProvider(baseURL, models), nil
}
```

它要三个环境变量（key、account、gateway）才能拼出端点，缺一个就返回一条精确点名的错误——`--base-url` 覆盖优先级最高，能盖过拼出来的默认值。校验通过后，它复用前面那批标准构造器（这里是 `NewAnthropicProvider`，因为 AI Gateway 走 Anthropic 协议；Workers AI 则走 `NewOpenAICompatibleProvider`）。

Bedrock 的解析器 `resolveBedrock` 有个值得学习的"诚实"设计。PRD 明确把 AWS SigV4 请求签名列为非目标，Bedrock 只支持 `AWS_BEARER_TOKEN_BEDROCK` 这条 bearer 路径。但它没有对其他 AWS 凭据视而不见：

```go
if strings.TrimSpace(env("AWS_BEARER_TOKEN_BEDROCK")) == "" {
	hasProfile := strings.TrimSpace(env("AWS_PROFILE")) != ""
	hasStaticKeys := strings.TrimSpace(env("AWS_ACCESS_KEY_ID")) != "" &&
		strings.TrimSpace(env("AWS_SECRET_ACCESS_KEY")) != ""
	if hasProfile || hasStaticKeys {
		return nil, fmt.Errorf("amazon-bedrock: detected AWS credentials (AWS_PROFILE / AWS_ACCESS_KEY_ID) but SigV4 request signing is not supported yet; set AWS_BEARER_TOKEN_BEDROCK to use the bearer-token path")
	}
	return nil, fmt.Errorf("amazon-bedrock: missing required env var AWS_BEARER_TOKEN_BEDROCK")
}
```

如果用户设了 `AWS_PROFILE` 或静态 AK/SK，它检测得到，并回一条明确的错误：告诉你"我看到你的 AWS 凭据了，但 SigV4 还没实现，请改用 bearer token"，而不是抛一个让人摸不着头脑的鉴权失败。把未实现的能力做成一条可操作的提示，比默默失败友好得多，这个细节值得抄进自己的代码。

这样，第 1 章 `resolveProvider` 那条优先级链的下游就全部补齐了：`--provider` 命中特殊鉴权 Provider 时走 `ResolveSpecialProvider`（本节），普通 Provider 走标准构造器（上一节），`--protocol` 直接构造对应协议驱动，都没有时回落到精选目录与前缀启发式，最终默认落到 OpenRouter。整条链从用户输入一路走到"一个可以直接发流式请求的 `Provider` 对象"。

## 实验 4-1 ★：观察双失败模型与 SSE 解析 {.unnumbered}

**目标**：不联网、不用真实密钥，亲手验证本章两个最核心的机制——双失败模型（"建不起流"是返回错误，运行期失败随流而下）以及传输层的 SSE 解析与看门狗。`internal/provider` 的测试套件用 `httptest` 起本地假上游，正好把这些行为逐条钉死。

**前置**：在仓库根目录能 `go test`（无需任何环境变量或网络）。

**步骤 1**：跑传输层测试，看双失败模型的两半各自成立。

```bash
go test ./internal/provider/ -run 'Transport(DecodeErrorRidesStream|EarlyBuildFailure)' -v
```

对照 `internal/provider/transport_test.go`：`TestTransportDecodeErrorRidesStream` 喂进一条畸形 SSE（`data: {"bad":true}`），断言 `StreamRequest` **不返回错误**、而是流的最终消息 `StopReason == error`——这是"运行期失败随流而下"。`TestTransportEarlyBuildFailure` 让 `NewRequest` 直接返回错误，断言 `StreamRequest` **返回**一个 error——这是唯一的"建不起流"早错误。两个测试一正一反，正好卡住 FR-13 契约的两端。

**步骤 2**：看重试只在服务器明说时发生。

```bash
go test ./internal/provider/ -run 'TransportRetry' -v
```

`TestTransportRetryOn503` 让假上游第一次回 503（带 `Retry-After: 0`）、第二次回正常流，断言最终成功且**恰好尝试了 2 次**——连接阶段重试、不重放已消费的流。`TestTransportRetryExhausted` 让上游一直回 429，断言重试耗尽后返回早错误。

**步骤 3**：看空闲看门狗开火。

```bash
go test ./internal/provider/ -run 'TransportIdleWatchdog' -v
```

它用 `t.Setenv("PIGO_STREAM_IDLE_TIMEOUT", "100ms")` 把空闲窗口调到 100 毫秒，让上游连上后一直挂着不发数据，断言流最终以一条 `StopReason=error`、内容为空闲超时的终止消息收尾。你可以把这个环境变量改成 `"2s"` 再跑一次，感受看门狗窗口是如何被 `idleTimeout()` 读取的。

**步骤 4**：看两个解码器把线上格式还原成同一套事件。

```bash
go test ./internal/provider/ -run 'Decoder' -v
```

`TestOpenAIDecoderToolCallStream` 与 `TestAnthropicDecoderToolUseStream` 分别喂进两套协议真实形态的 SSE 分片，断言解码器把碎片拼成同一种 `AssistantMessage`（文本块 + 工具调用块、参数 JSON 完整）。把这两个测试的输入并排看，你就能直观感受到：**输入的协议天差地别，输出却收敛到同一套 `AssistantMessageEvent`**——这正是统一 Provider 接口存在的意义。

**观察点**：这一整套测试不碰网络、不需要密钥，却把本章的骨架全验了一遍。回头对照 `StreamRequest`（早错误 vs 流内错误的分叉）、`pump` 的 `select`（看门狗与 SSE 解析）、以及两个解码器的 `partial()`（协议 → 统一事件），你会发现"能被本地假上游完整测试"本身就是这套设计的一个副产品：把 HTTP/SSE/重试/看门狗抽进共享传输层、把协议差异关进无副作用的解码器之后，每一块都可独立、可确定地测试。

## 本章小结

本章把 pigo 的 Provider 层从契约到线缆彻底解剖了一遍：

- **统一契约与双失败模型**：`StreamFn` / `Provider`（`provider_interface.go`、`provider.go`）给循环一个协议无关的流式接口。返回的 `error` 只保留给"连流都建不起来"的早错误；一切运行期失败都包成终止的 `StreamErrorEvent` 随流而下，走通 `IsComplete → ExtractResult → Result` 这条既有链路。增量事件携带的是"截至此刻的完整消息快照"，让消费端无需自己拼碎片。
- **两套协议解码**：`OpenAIDecoder`（`openai.go`）解同构的 `chat.completion.chunk` 流并按 index 归并分片的工具调用；`AnthropicDecoder`（`anthropic.go`）解有类型的 Messages 事件序列。二者输入迥异，却都遵守"永不 panic、错误走返回值、`Finish` 冲刷"的同一份 `Decoder` 契约，输出同一套 `AssistantMessageEvent`。
- **共享传输驱动**：`transport.go` 把 HTTP + SSE 行解析 + 连接期重试 + 双看门狗（idle 防静默、stall 防"活着但无内容"）+ 双失败模型全部抽进 `StreamRequest`/`pump`，让每个 Provider 退化成一个 `Decoder`。`NewRequest` 工厂保证重试不重放已消费的 body，`done` channel 保证读 goroutine 不泄漏。
- **两种驱动**：`openAICompatDriver` 与 `anthropicCompatDriver`（`providers.go`）把请求编码成对应线上格式、配对应解码器、交给共享传输层；构造器收敛到一张 preset 表，`checkImageSupport` 把"模型看不见图"变成显式错误。
- **注册表、精选目录与鉴权**：`registry.go` 是内置 Provider 元数据的唯一真相源（只存环境变量名，不存密钥）；`presets.go` 是给人看的推荐菜单；`auth.go` 的 `CredentialStore` 按 OAuth → 覆盖 → 环境变量 → 配置文件三层解析密钥；`special_auth.go` 为 Azure/Bedrock/Vertex/Cloudflare 校验多参数并拼出端点，缺参数时精确点名、绝不泄漏 secret。

第 1 章一笔带过的 `resolveProvider`，到这里补齐了它下游的全部细节：从"用户想用哪个模型"一路走到"一个能直接发流式请求的 `Provider`"。下一章转向工具系统，看模型决定调用工具之后，循环如何在信任闸门的把关下把这些调用批量执行、再回填进上下文。

## 思考题

1. `StreamFn` 的契约规定"返回的 error 只用于建不起流的早错误，运行期失败一律走流内终止事件"。如果反其道而行，把网络中断也做成返回 error，第 3 章的循环在"模型已吐了半段文本才断线"的场景下会丢失什么？对照 `EventStream.Result` 的语义想一想。
2. 每个增量事件（`StreamTextEvent` 等）携带的是"截至此刻的完整消息快照"而非"本次新增的片段"。这个选择让消费端简单了，代价是什么？在什么样的负载下这个代价会变得不可忽视？
3. `pump` 为什么需要 **两个** 看门狗？构造一个只有 idle 看门狗会漏掉、必须靠 stall 看门狗才能逮住的病态服务器行为；再构造一个反过来的。（提示：注意 idle 与 stall 计时器各自的重置时机。）
4. `TransportConfig.NewRequest` 是一个"每次连接都重造请求"的工厂，而不是一个现成的 `*http.Request`。如果改成后者，`connect` 的重试路径会在何处、以何种方式出错？
5. `resolveBedrock` 检测到 `AWS_PROFILE` 或静态 AK/SK 时，没有默默失败，而是回一条"检测到凭据但 SigV4 未实现"的错误。对照 `checkImageSupport` 对"模型看不见图"的处理，总结这两处共享的是同一种什么设计原则？它对使用者的体验有何影响？
6. `minimax` 在注册表里是 `Protocol: anthropic` + `AuthScheme: x-api-key`，端点却是国产网关。结合 `anthropicAuthHeaderFor` 与 `NewAnthropicProtocolProvider`，说明"协议""鉴权""厂商"这三个维度为什么在 pigo 里是彼此正交的，以及这种正交性带来了什么扩展上的好处。



# 研究摘要：pi agent-loop / types → Go 映射

> Wayfinder ticket #2 的产出。源码精读自 `/Users/chaoyuepan/ai/pi`：
> `packages/agent/src/agent-loop.ts`、`packages/agent/src/types.ts`、
> `packages/ai/src/types.ts`、`packages/ai/src/utils/event-stream.ts`。
> 目的：为 pigo 的 US-001..006 提供 TS→Go 映射建议、控制流复刻要点与易错点。

---

## 1. 数据类型层（对应 US-001）

### 1.1 消息与内容块（discriminated union → interface + type switch）

pi 的 content 块是 tagged union（`type` 字段判别）：

| pi (TS) | 字段 | Go 建议 |
|---|---|---|
| `TextContent` | `type:"text"`, `text`, `textSignature?` | `TextContent struct` |
| `ThinkingContent` | `type:"thinking"`, `thinking`, `thinkingSignature?`, `redacted?` | `ThinkingContent struct` |
| `ToolCall` | `type:"toolCall"`, `id`, `name`, `arguments`, `thoughtSignature?` | `ToolCallContent struct` |
| `ImageContent` | `type:"image"`, `data`, `mimeType` | `ImageContent struct` |

**映射方向**：定义密封接口 `Content interface { isContent()}`，每个 struct 实现它；消费端用 `switch c := c.(type)` 分派。**不要**用 `map[string]any` + 字符串判别（丢类型安全，正是要摆脱的）。

**JSON 序列化易错点**：Go 的 `encoding/json` 不会自动按 `type` 字段反序列化到接口。需要：
- 每个 content struct 带 `Type string json:"type"` 字面量字段；
- 自定义 `UnmarshalJSON`（在容器层，如 `AssistantMessage.Content []Content`）先 peek `type` 再分派到具体 struct。会话持久化（US-024）与 provider 解析都依赖这个。

三种消息角色（`Message = UserMessage | AssistantMessage | ToolResultMessage`）：

- `UserMessage`: `role:"user"`, `content`（string 或 `[]TextContent|ImageContent`）, `timestamp`
- `AssistantMessage`: `role:"assistant"`, `content:[]（Text|Thinking|ToolCall）`, `api`, `provider`, `model`, `usage`, `stopReason`, `errorMessage?`, `timestamp`（+ `responseModel/responseId/diagnostics` 可选）
- `ToolResultMessage`: `role:"toolResult"`, `toolCallId`, `toolName`, `content:[]（Text|Image）`, `details?`, `isError`, `timestamp`

> **注意 content 的类型收窄**：assistant 的 content 可含 thinking/toolCall，user/toolResult 的 content 只能是 text/image。Go 里可以用同一个 `Content` 接口但在构造/校验时约束，或分两个接口。建议单一接口 + 运行时约束，避免接口爆炸。

**`AgentMessage` 的可扩展性**：pi 用 TS declaration merging 让 app 注入自定义消息类型（`CustomAgentMessages`）。Go 没有等价物 → 建议 `AgentMessage` 直接就是 `Message` 接口，自定义消息类型让其实现同一接口；`convertToLlm` 负责把非 LLM 消息过滤/转换掉。**不要**试图复刻 declaration merging。

### 1.2 AgentToolResult / terminate 语义

```
AgentToolResult<T> { content []（Text|Image）; details T; terminate?: bool }
```

Go：`details` 用泛型 `AgentToolResult[T any]` 或 `any`。pi 内部大量 `AgentToolResult<any>` → pigo 首版用 `any` 更省事，泛型可后加。`terminate` 是**批级**提示（见 §3.4）。

### 1.3 AgentEvent（→ US-001 事件类型全集）

10 种事件，pigo 必须全覆盖（PRD FR-24）：

`agent_start` / `agent_end{messages}` / `turn_start` / `turn_end{message, toolResults}` /
`message_start{message}` / `message_update{message, assistantMessageEvent}` / `message_end{message}` /
`tool_execution_start{toolCallId, toolName, args}` / `tool_execution_update{...partialResult}` /
`tool_execution_end{toolCallId, toolName, result, isError}`

Go：`AgentEvent interface { isAgentEvent() }` + 10 个 struct，或带 `Type` 字段的单 struct（事件多为 UI 消费，单 struct + 可空字段也可接受，但 type switch 更安全）。**建议接口 + type switch**，与 content 保持一致风格。

---

## 2. 事件流机制（对应 US-002：async generator → channel）

### 2.1 EventStream 的本质

pi 的 `EventStream<T,R>`（`event-stream.ts`）是一个"边推事件、边异步迭代、并在某个终止事件上解析出最终结果 R"的结构：

- `push(event)`：若 `isComplete(event)` 为真则记录 `extractResult(event)` 作为最终结果；派发给等待的消费者或入队。
- `end(result?)`：关闭流，唤醒所有等待者。
- `[Symbol.asyncIterator]`：消费端 `for await`。
- `result(): Promise<R>`：拿最终结果。

两个实例化：
- **agent 层**：`isComplete = (e)=> e.type==="agent_end"`，`extractResult = (e)=> e.messages`（即 `EventStream<AgentEvent, AgentMessage[]>`）。
- **provider 层**：`AssistantMessageEventStream`，`isComplete = done|error`，`extractResult` 取 `message`（done）或 `error`（error）。

### 2.2 Go 映射

**核心决策**：一个 struct 包一个 `chan AgentEvent` + 一个 `result` 兑现机制。

```go
type EventStream[T any, R any] struct {
    ch      chan T
    result  R
    resultCh chan struct{} // closed 时 result 就绪
    // ...
}
```

建议实现要点：
- **迭代**：`Events() <-chan T` 供 `for ev := range s.Events()`。async iterator → range over channel 是最自然的映射。
- **最终结果**：`Result(ctx) (R, error)`；用一个 `sync.Once` + closed channel 表示就绪。**不要**把 result 也塞进事件 channel（消费者可能不读完）。
- **push/end 的等价物**：生产者 goroutine 往 `ch` 写，写完 `close(ch)`；在 close 前把满足 `isComplete` 的事件对应的 result 存好。pi 的 `isComplete/extractResult` 回调可保留为 struct 上的 func 字段，或直接在 agent loop 里显式设置 result（更 Go-idiomatic）。
- **取消**（PRD FR-22 / US-002 验收）：贯穿 `context.Context`。生产者 goroutine `select { case ch<-ev: case <-ctx.Done(): return }`，避免消费者停读时 goroutine 泄漏。abort 时 loop 走 aborted 终态（见 §3.5）。

**易错点**：
- TS 的 async generator 是**拉模型**（消费者驱动 `next()`），channel 是**推模型**。若消费者慢，生产者会阻塞在 `ch<-`——这其实是想要的背压。但 emit 在 pi 里是 `await emit(...)`（顺序 await），Go 里 `ch<-ev` 天然顺序阻塞，语义一致。✅
- pi 的 `emit` 可能返回 Promise（listener 是 async 的），`agent_end` 的 listener settle 才算 run 结束（见 `AgentState.isStreaming` 注释）。Go 里若 emit 只是 `ch<-`，则"listener settle"语义需另想——首版可简化为"channel 关闭即结束"，把 listener 副作用留给消费方自己 sync。**记录为简化点**。

---

## 3. Agent Loop 控制流（对应 US-003/004/005/006）

### 3.1 入口（US-006 验收：两个入口）

- `agentLoop(prompts, context, config, signal?, streamFn?)`：新 prompt 启动。把 prompts 加进 context，发 `agent_start`→`turn_start`→(每个 prompt 的 message_start/message_end)，再进 `runLoop`。
- `agentLoopContinue(context, config, ...)`：从现有 context 续跑。**校验**：`messages` 非空，且最后一条 role ≠ `assistant`（否则 provider 会拒）。发 `agent_start`→`turn_start` 后进 `runLoop`。

两者都返回 `EventStream<AgentEvent, AgentMessage[]>`；内部 `runAgentLoop*` 跑完把 `newMessages` 作为 `end()` 结果。

**Go**：`func AgentLoop(...) *EventStream[AgentEvent, []AgentMessage]`；内部起 goroutine 跑 `runLoop`，完成后设置 result 并 close。`newMessages` 累积的是"本次调用新产生的消息"（prompt 版含初始 prompt，续跑版不含既有 context 消息）——**这个区分要保留**，`shouldStopAfterTurn`/结果都依赖它。

### 3.2 双层循环骨架（US-006 核心）

```
pending = getSteeringMessages() || []          // 启动前先捞（用户可能在等待时输入了）
外层 for {
    hasMoreToolCalls = true
    内层 for hasMoreToolCalls || len(pending)>0 {
        非首轮 → emit turn_start
        注入 pending（每条 emit message_start/message_end，push 进 context & newMessages），清空 pending
        msg = streamAssistantResponse(...)     // §3.3
        newMessages.push(msg)
        if msg.stopReason ∈ {error, aborted} { emit turn_end{msg,[]}; emit agent_end; return }
        toolCalls = msg.content 里的 toolCall
        toolResults = []
        hasMoreToolCalls = false
        if len(toolCalls)>0 {
            batch = (msg.stopReason=="length")
                      ? failToolCallsFromTruncatedMessage(toolCalls)   // §3.6
                      : executeToolCalls(...)                          // §3.4
            toolResults = batch.messages
            hasMoreToolCalls = !batch.terminate
            for r in toolResults { push 进 context & newMessages }
        }
        emit turn_end{msg, toolResults}
        snap = prepareNextTurn({msg,toolResults,context,newMessages})  // 可换 context/model/thinking
        应用 snap（见 §3.7）
        if shouldStopAfterTurn({...}) { emit agent_end; return }
        pending = getSteeringMessages() || []   // 每轮工具执行后重新捞 steering
    }
    // agent 本会停在这，检查 follow-up
    followUp = getFollowUpMessages() || []
    if len(followUp)>0 { pending = followUp; continue }  // 回内层
    break
}
emit agent_end{newMessages}
```

**复刻要点**：
- **首轮不发 turn_start**（入口已发过一次）——`firstTurn` 标志。Go 里同样一个 bool。
- **steering vs follow-up 的差异**：steering 在每轮工具执行后注入（agent 还在干活时插话）；follow-up 在 agent 本要停下时才注入（排队等 agent 干完）。两者都进 `pending` 走同一注入路径，但捞取时机不同。
- 内层退出条件 `hasMoreToolCalls || pending>0`：没有更多工具调用**且**没有待注入消息时退内层。

### 3.3 streamAssistantResponse（US-003）

顺序（**严格保持**）：
1. `transformContext(messages, signal)`（可选）— AgentMessage[]→AgentMessage[]，用于上下文裁剪/注入。契约：**不得抛错**，失败返回安全回退。
2. `convertToLlm(messages)` — AgentMessage[]→Message[]，过滤 UI-only 消息。同样**不得抛错**。
3. 组 `llmContext{systemPrompt, messages, tools}`。
4. **解析 API key**：`resolvedApiKey = (getApiKey ? await getApiKey(provider) : undefined) || config.apiKey`。这是处理短时 token 过期的关键（US-003/US-012、FR-15）。
5. 调 `streamFn(model, llmContext, {...config, apiKey, signal})`（默认 `streamSimple`）。
6. 遍历返回的事件流：
   - `start`：拿 `partial`，**push 进 context.messages**（占位），发 `message_start`。
   - `text_*/thinking_*/toolcall_*`：用 `event.partial` **替换** context 里最后一条，发 `message_update{assistantMessageEvent, message}`。
   - `done`/`error`：取 `response.result()` 作为 finalMessage，替换/追加进 context，发 `message_end`，返回。
7. 流自然结束（未见 done/error）也兜底取 `result()`。

**Go 映射**：
- `streamFn` = `StreamFn` 接口/函数类型，返回 `*AssistantMessageEventStream`。
- **partial 回填**：pi 靠"push 占位 + 替换最后一条"。Go 里对 `context.Messages []AgentMessage` 做 `msgs[len-1] = partial`——注意并发：loop 是单 goroutine 消费 provider 流，context.messages 只此处改，无需锁。但如果 TUI 另一 goroutine 读 messages，需要外层同步（记录为 §5 并发注意）。
- `addedPartial` bool 处理"provider 没发 start 就直接 done"的边界。**保留**。

**契约（US-003 / FR-13）**：streamFn **不通过抛错表达请求失败**，而是在流内发 error 事件 + 终态 assistant 消息（`stopReason=error/aborted` + `errorMessage`）。Go 里 `StreamFn` 签名**不返回 error**（或返回的 error 仅限"无法建流"这种极早期失败）；运行时失败编码进事件流。这是整个 provider 层的地基（US-007 会再确认）。

### 3.4 工具三段式 prepare→execute→finalize（US-004）

**prepare**（`prepareToolCall`）：
1. 查注册表 `tools.find(name)`；找不到 → immediate error result（"Tool X not found"）。
2. `prepareArguments(args)`（可选 shim，raw→schema-shaped）。pi 有个小优化：返回值 `===` 原值就不新建对象（Go 里可忽略此优化，直接用返回值）。
3. `validateToolArguments`（JSON Schema 校验，US-014）。抛错 → catch 成 immediate error result。
4. `beforeToolCall({assistantMessage, toolCall, args, context}, signal)`（可选钩子）：
   - `signal.aborted` → immediate error "Operation aborted"。
   - 返回 `{block:true}` → immediate error result（`reason` 或默认文案）。
5. 都过 → 返回 `prepared{toolCall, tool, args}`。
6. 整个 try 块的任何异常 → immediate error result。

**execute**（`executePreparedToolCall`）：
- 调 `tool.execute(id, args, signal, onUpdate)`。
- `onUpdate(partialResult)` → 发 `tool_execution_update`。pi 用 `acceptingUpdates` 标志 + 收集 update 的 Promise，execute 返回后 `await Promise.all(updateEvents)` 确保 update 事件在 end 前都发完，且 execute 结束后忽略迟到的 update。
- 抛错 → error tool result（不 panic）。

**Go 映射（execute 的 onUpdate）**：pi 的"收集 update promise 再 await 全部"是为了顺序性。Go 里 `onUpdate` 若直接 `ch<-updateEvent` 是同步阻塞的，天然有序，可**简化掉 acceptingUpdates + Promise.all**。但要防"execute 已返回后 tool 仍调 onUpdate"——用一个 `atomic.Bool` 或在 execute 返回后关闭一个 `done` channel，onUpdate 里 select 检测。**记录为简化点 + 边界防护**。

**finalize**（`finalizeExecutedToolCall`）：
- `afterToolCall({assistantMessage, toolCall, args, result, isError, context}, signal)`（可选钩子）返回覆盖：
  - `content ?? result.content`、`details ?? result.details`、`terminate ?? result.terminate`、`isError ?? isError`。
  - **字段级替换，无深合并**（FR-5）。Go 里因为没有 `??`，要显式判 nil/未设置：用**指针字段**（`*[]Content`, `*bool`）区分"未提供"与"提供了零值"。这是 Go 复刻此语义的关键——`AfterToolCallResult` 的字段必须是指针或 `*T`，否则无法区分"没返回 content"和"返回了空 content"。
  - afterToolCall 抛错 → 整个结果变 error result。

immediate 结果**跳过 execute/finalize**（不触发 afterToolCall）——注意 immediate 分支不走 finalize。

### 3.5 并行与串行（US-005）

**模式选择**（`executeToolCalls`）：`config.toolExecution==="sequential"` **或** 任一 toolCall 对应的 tool `executionMode==="sequential"` → 走串行；否则并行。默认 parallel。

**sequential**：逐个 prepare→execute→finalize，每个立即发 `tool_execution_end` + 造 tool-result 消息 + 发 message_start/end。**每步后检查 `signal.aborted`，中断则 break**。

**parallel**（保序是重点，US-005 验收）：
1. **顺序** prepare 所有 toolCall（先发 `tool_execution_start`）。immediate 结果直接入 `finalizedCalls`；prepared 的包成一个 thunk（闭包）入 `finalizedCalls`（保持在数组里的**位置**=assistant 源顺序）。
2. `Promise.all(finalizedCalls.map(执行 thunk 或原样))` → 并发执行，但结果数组**按原 index 保序**。
3. 按序造 tool-result 消息 + 发 message 事件。

**Go 映射（保序并发）**——这是 PRD 明确点名的 async 映射：
```go
results := make([]FinalizedToolCallOutcome, len(entries))
var wg sync.WaitGroup
for i, e := range entries {
    if e.immediate { results[i] = e.outcome; continue }
    wg.Add(1)
    go func(i int, e entry) {
        defer wg.Done()
        executed := executePrepared(e.prepared, ...)   // 内部发 tool_execution_update
        results[i] = finalize(executed, ...)
        emitToolExecutionEnd(results[i])               // 见下方顺序性警告
    }(i, e)
}
wg.Wait()
// 再按 i 顺序造 tool-result 消息 & 发 message 事件
```
- **保序靠按 index 回填 `results[i]`**（正是 PRD 说的"goroutine + index 回填"）。✅
- **顺序性微妙点**：pi 的 parallel 里 `tool_execution_end` 在**完成顺序**发（thunk 内部），而 tool-result **message** 事件在**源顺序**发（Promise.all 之后）。若 pigo 想 100% 复刻，`tool_execution_end` 可在 goroutine 内发（完成序），message 事件循环外按序发。但多 goroutine 并发 `emit`（ch<-）会竞争 channel——**需要 emit 加锁或改为完成序也走收集后统一发**。首版建议：**tool_execution_end 也收集后按源序发**（比 pi 少一点实时性，但 emit 无并发竞争，更安全）。**记录为可接受偏差**。
- prepare 阶段每步后 `signal.aborted` 检查 → break（与 pi 一致）。

### 3.6 截断保护 failToolCallsFromTruncatedMessage（US-006 / FR-10）

当 `stopReason==="length"`（输出被 token 上限截断），该消息的**所有** toolCall 一律判失败：逐个发 `tool_execution_start` → 造固定文案的 error result（"...hit the output token limit...re-issue with complete arguments"）→ 发 `tool_execution_end` + tool-result 消息。返回 `{messages, terminate:false}`。

**为什么**：流式 tool-call 参数用"尽力 JSON 抢救解析器"收尾，截断的消息可能产出**能 parse、能校验、但静默不完整**的参数，执行不安全。直译即可，无并发。

### 3.7 prepareNextTurn 应用 & shouldStopAfterTurn（US-006 / FR-6,7）

`prepareNextTurn` 返回 `{context?, model?, thinkingLevel?}`：
- `context ?? currentContext`
- `model ?? config.model`
- `reasoning`：`thinkingLevel===undefined` → 保持；`==="off"` → undefined；否则 → thinkingLevel。

**Go 映射**：`AgentLoopTurnUpdate` 字段用指针区分"未提供"（thinkingLevel 有三态：未提供/off/具体值——必须用 `*ThinkingLevel` 或专门的哨兵）。这又是一处**指针字段区分三态**的地方，和 §3.4 afterToolCall 同一类易错点。

`shouldStopAfterTurn` 返回 true → 发 `agent_end` 退出（在捞 steering/follow-up 之前）。

---

## 4. 钩子契约总表（US-006，全部"不得抛错"）

| 钩子 | 时机 | 作用 | 契约 |
|---|---|---|---|
| `transformContext` | 每次 LLM 调用前，convertToLlm 之前 | AgentMessage[]→AgentMessage[]，裁剪/注入 | 不抛，失败回退原值 |
| `convertToLlm` | 每次 LLM 调用前 | AgentMessage[]→Message[]，过滤 UI-only | 不抛，返回安全回退 |
| `getApiKey` | 每次 LLM 调用，解析 key | 短时 token 刷新 | 不抛，无 key 返 undefined |
| `beforeToolCall` | prepare 校验后、execute 前 | 返回 block 阻止执行 | 收 signal，自行响应 abort |
| `afterToolCall` | execute 后、发 end 事件前 | 字段级覆盖结果（无深合并） | 收 signal |
| `prepareNextTurn` | turn_end 后、决定是否再请求前 | 换 context/model/thinking | 不抛 |
| `shouldStopAfterTurn` | turn_end 后 | true 则 agent_end 退出 | 不抛 |
| `getSteeringMessages` | 每轮工具执行后 | 运行中插话 | 不抛，无则 [] |
| `getFollowUpMessages` | agent 本要停时 | 排队等干完再继续 | 不抛，无则 [] |

> pi 是 6 个"核心钩子"（PRD 列的 beforeToolCall/afterToolCall/prepareNextTurn/shouldStopAfterTurn/getSteeringMessages/getFollowUpMessages），另有 transformContext/convertToLlm/getApiKey 三个上下文/请求处理回调。pigo 都要有。

**Go 映射**：全部作为 `AgentLoopConfig` 上的 **func 字段**（nil 判断 = "未提供"，正是 PRD 说的"可选回调 → struct 中 func 字段 + nil 判断"）。调用前 `if config.BeforeToolCall != nil`。"不得抛错"契约在 Go 里对应"不 panic"——但 Go 惯例是返回 error，此处应遵循 pi：钩子签名可返回 error，loop 捕获并转成安全回退/error tool result，而非 panic 逃逸。

---

## 5. Go 化的横切注意点

1. **三态区分（最高频易错点）**：`afterToolCall` 的覆盖字段、`prepareNextTurn` 的 thinkingLevel 都需区分"未提供/零值/具体值"。TS 靠 `undefined` + `??`，Go 必须用**指针字段**（`*bool`、`*[]Content`、`*ThinkingLevel`）。定义 `AfterToolCallResult` / `AgentLoopTurnUpdate` 时统一用指针。
2. **context.messages 的就地修改**：streamAssistantResponse 里"push 占位 + 替换最后一条"是就地改 slice。单 loop goroutine 内安全；若 TUI 并发读，需外层加锁或用 channel 传快照。
3. **emit 并发**：parallel 工具执行若在多 goroutine 里 emit，channel 写会竞争——首版建议所有 emit 都在 loop 主 goroutine 串行发（工具结果收集后按序 emit），牺牲一点实时性换正确性。
4. **abort 贯穿**：`context.Context` 从入口传到 streamFn、tool.execute、各钩子。abort 时：streamFn 走 aborted 终态消息；prepare 各检查点产 "Operation aborted" error result；sequential 每步后 break。
5. **timestamp**：pi 用 `Date.now()`（ms）。Go 用 `time.Now().UnixMilli()`，保持 ms 以便会话文件跨读（US-024 决策待定，见 ticket #5）。
6. **可直译 vs 需重设计**：
   - **可近乎直译**（控制流骨架）：runLoop 双层循环、prepare/execute/finalize 分支、failToolCallsFromTruncatedMessage、prepareNextTurn 应用逻辑。
   - **需重新设计**（语言差异）：EventStream（generator→channel）、parallel 保序（Promise.all→WaitGroup+index）、三态字段（undefined→指针）、content/event union（→interface+type switch + 自定义 JSON）、declaration merging（→放弃，统一接口）。

---

## 6. 对下游 ticket / 雾区的影响

- **"核心类型精确 Go 签名"（Not yet specified）现在可成 ticket**：§1 已给出全部 struct 字段与接口设计方向，可据此起草 `internal/agent/types.go` 的精确签名 ticket（US-001）。
- **"Provider 通用 transport"** 仍需 zero 调研（ticket #3）输入 SSE/重连细节，暂不 graduate。
- **thinking 归一（ticket #10）** 与本摘要 §3.7 的 thinkingLevel 三态相关：pi 的 ThinkingLevel = off|minimal|low|medium|high|xhigh，agent 层已有归一枚举，provider 层如何映射是 #10 要定的。
- **US-002 EventStream** 的 Go 设计方向（§2.2）已足够成实现 ticket。

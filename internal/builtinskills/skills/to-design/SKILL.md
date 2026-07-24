---
name: to-design
description: "Generate a design document (design proposal) from a PRD, in the style of Go's official design proposals — Abstract / Background / Design / Rationale / Compatibility / Implementation, heavy on the 'why' and tradeoffs. Triggers on: to-design, prd-to-design, prd转设计文档, 生成设计文档, 写设计文档, design doc, design proposal, 设计提案, 技术设计文档."
user-invocable: true
---

# to-design — PRD to Design Document

Turn a PRD (or a rough idea) into a **design document** written in the style of Go's official design proposals: plain language, concrete examples, and—above all—an honest account of *why this approach and not the alternatives*.

This is **not** the same as `prd-to-spec`. A SPEC is an implementation contract (tables, endpoints, schemas) for an engineer to build against. A design document is a **decision artifact**: it argues for an approach, surfaces the tradeoffs, and lets a team agree on the same facts before anyone writes code. When the question is "*how should we build this and why*", produce a design doc; when the question is "*give me the exact contract to implement*", produce a SPEC.

> 设计哲学源自对 5 篇 Go 官方 proposal（泛型 / 错误包装 / loopvar / slog / try）的分析。核心信念：**文档的价值不取决于方案是否通过，而取决于它是否让讨论建立在同一套事实和取舍之上。**

---

## When to Use

- A PRD exists and you need to decide *how* to build it before committing to implementation
- The approach has real tradeoffs and you want them documented and debated
- The change is risky, breaking, or hard to reverse (a design doc forces the compatibility conversation early)
- Multiple people need to agree on a direction before work fans out
- You want a durable record of "why we chose X and rejected Y" — even if the proposal is later rejected

If the team just needs the concrete contract to code against, use `/prd-to-spec` instead (or run `to-design` first, then `prd-to-spec`).

---

## The Job

1. **Locate input** — find or receive the PRD (or idea)
2. **Analyze context (optional)** — scan the codebase for existing patterns, constraints, and prior art
3. **Surface the decisions** — identify the real design forks and ask clarifying questions (max 3-5)
4. **Generate the design doc** — following the structure and writing style below
5. **Review** — present for feedback, especially on the Rationale and Compatibility sections
6. **Save** — write to the agreed location

---

## Step 1: Locate Input

```
Provide the PRD (or idea) to design from:

A. File path (e.g., tasks/prd-priority-system.md)
B. GitHub Issue URL
C. Paste content directly
D. Just describe the idea — I'll design from the conversation
```

A design doc can start from a half-formed idea, not only a polished PRD. If the input is thin, lean harder on Step 3.

---

## Step 2: Analyze Context (Optional)

Skip for greenfield. Otherwise scan to ground the design in reality:

- **Existing patterns** the design should match (naming, error handling, module boundaries)
- **Prior art** — has something similar been tried or rejected here before?
- **Constraints** — compatibility promises, public APIs, data the design can't break
- **Real pain** — find the actual buggy/awkward code the design fixes, so Background can quote it

The most persuasive Background sections quote **real code from the user's own repo**, not hypotheticals.

---

## Step 3: Surface the Decisions

A design doc lives or dies on its Rationale. Before writing, find the **real forks in the road** — the points where a competent engineer could reasonably go two ways — and resolve them.

Ask only about genuine forks:

```
Design decisions to settle before I write the doc:

1. Where does this logic live?
   A. Extend the existing X
   B. New standalone component Y
   C. Let me recommend based on the codebase

2. Is this a breaking change for existing callers?
   A. Yes — needs a migration path
   B. No — purely additive
   C. Unsure — I'll analyze and flag it

3. What's the one promise this design must keep? (e.g. backward compatibility,
   latency budget, no new dependencies)
```

For every fork, also note the **option you are NOT choosing** — that becomes the Rationale.

---

## Step 4: Design Document Structure

This is the standard skeleton distilled from the 5 Go proposals. Keep section names; drop sections that genuinely don't apply (and say why if the omission is notable).

```markdown
Title: <一句话说清"做什么" —— 标题就是结论，不是名词短语>
Author(s): <作者>
Last updated: <YYYY-MM-DD>
Discussion at <issue / PR / 文档链接>   # 让文档不孤立，永远附讨论入口
Status: Draft | Under review | Accepted | Rejected

## Abstract / 摘要

一段话讲完全文：做什么、大致怎么做、以及**最重要的那个承诺**（如"向后兼容""不引入新依赖"）。
读者读完这一段就该知道全貌。把隐含的核心约束埋在这里。

## Background / 背景与动机

用**具体、可感的例子**说明"痛在哪"，而不是抽象地说"现状不好"。
- 能贴一段真实的 bug 代码 / 别扭的调用，就贴。先让读者"疼"起来。
- 量化痛点（出现频率、踩坑次数、损失），不要用形容词堆砌。
- 一句话给问题定性。

## Design / Proposal / 设计

文档主体。遵循三条：
- **从简单到复杂，渐进式教学**：从最小例子起步，复杂场景留到读者有直觉之后。
- **声明 + 示例 + 边界**三件套：每个 API/接口先给声明，再给用法片段，再划清适用边界。
- **改造前 vs 改造后对照**：能并排展示收益的，就并排展示。
能用一段可运行代码说清的，绝不用一段文字描述。

## Rationale / 理由与取舍

> Rationale = "为什么是这个方案，而不是别的"的论证。这是区分好文档和平庸文档的关键章节。

- 解释关键决策的动机。
- **主动列出被放弃的备选方案 + 放弃原因**（"我们没选 X，因为 Y"）。这比单方面论证你选的方案更可信，也避免后人重复讨论。
- 回应可预见的质疑。

## Compatibility / 兼容性

凡涉及破坏性变更，必须正面回应。
- 是不是破坏性变更？**开门见山承认**。
- 代价是什么（性能、行为变化、迁移成本）？**诚实列出**，不藏着。
- 渐进迁移路径（按模块/按文件 opt-in、灰度、特性开关）。
- 有先例佐证更好（"某系统做过类似变更，结果平淡无奇"）。

## Implementation / Transition / 实现与过渡

- 如何落地、分几步、配套什么工具。
- **用数据和工具支撑"可落地"**：实测失败率、灰度结果、自动化迁移工具，比任何"我们认为风险可控"都管用。
- 兼容老版本的过渡方案（如独立发布的兼容库）。

## Appendix / 附录（可选）

把会打断主线的细节后置：完整 API、端到端示例、FAQ。
FAQ 专门回应高频质疑（"为什么叫这个名字""为什么不用某语言的做法""和 X 有何不同"）。
```

---

## Writing Style (照搬 Go 文档的文风)

Structure is the skeleton; style is the muscle. Enforce these — they're what make the doc readable.

### Voice / 主语
- **决策用 "我们 / We"** — 把设计说成一群人可负责的选择，不是客观真理。("We propose…", "我们决定移除…")
- **行为用代码本身当主语** — "this code has a bug" / "这段代码会…"，让注意力落在程序上。
- **说理对读者用 "你 / you"** — 像面对面解释。
- **禁止无主语的被动腔** — 不写"据建议应当…""It is suggested that…"这类推卸责任的句式。

### Sentences / 句子
- **判断用短句，论证用长句**。先用一个极短的句子拍板（"这段代码有 bug。"），再用信息密集的长句铺开机制。
- 长短交替制造节奏。不要通篇绕来绕去的长句。

### Paragraphs / 段落
- **一段只讲一件事，观点放段首**（结论先行）。
- **小标题写成一句完整的论点**，而不是名词短语。
  - 写 `老代码不受影响，编译结果与之前完全一致`，而不是 `兼容性`。
  - 读者光看标题就能读完整条论证链。

### Tone / 语气
- **克制的诚实，甚至自嘲**。承认代价、承认自己也踩过坑，比形容词更有说服力。
- **强调要省着用**。全文只在最关键处加粗/斜体一次，反而最醒目。

---

## Step 5: Review & Iteration

Present the doc and steer feedback to the sections that matter most:

```
设计文档已生成。重点请看这几处：

- Rationale：被放弃的方案和理由是否站得住？有没有遗漏的备选项？
- Compatibility：破坏性和代价是否如实说清？迁移路径可行吗？
- Background：痛点是否用具体例子讲清，而不是形容词？
- 文风：标题是否是"结论"而非名词？有没有无主语的被动腔？

回复 OK 保存，或给出修改意见。
```

---

## Step 6: Save

```
设计文档保存到哪里？

A. tasks/design-[feature-name].md（紧挨 PRD，推荐）
B. docs/design/[feature-name].md
C. 自定义路径：[指定]
```

---

## Mapping: PRD → Design Doc

| PRD 部分 | Design Doc 部分 | 转化方式 |
|----------|-----------------|----------|
| Problem / 背景 | Background | 找到真实的痛点代码/场景，量化它 |
| Goals / 目标 | Abstract + Background | 提炼成"最重要的承诺"埋进摘要 |
| User Stories / 需求 | Design | 转成渐进式的设计示例 |
| Technical Considerations | Design + Rationale | 约束 → 设计决策 + 取舍论证 |
| Non-Goals | Rationale | 写成"我们没做 X，因为 Y" |
| Risks / 风险 | Compatibility + Implementation | 风险 → 兼容性代价 + 迁移/灰度方案 |
| 隐含的备选方案 | Rationale | 显式列出并解释为何不选 |

---

## Quality Criteria

A good design doc should pass these checks:

- [ ] 标题是一句"做什么"的结论，不是名词短语，且附了讨论链接
- [ ] 摘要里埋了最重要的承诺/约束
- [ ] Background 用了**具体例子或真实代码**讲痛点，而非形容词
- [ ] Design 遵循"声明 + 示例 + 边界"，并有渐进式教学
- [ ] **Rationale 主动列出了至少一个被放弃的方案及原因**（最关键的检查项）
- [ ] 凡破坏性变更，Compatibility 都正面承认并列出代价
- [ ] Implementation 用数据/工具支撑"可落地"，而非空喊"风险可控"
- [ ] 文风：决策用"我们"、行为用代码、无无主语被动腔；长短句交替；小标题是论点句
- [ ] 没有 "TBD / TODO"——要么解决，要么挪进 Open Questions

---

## Edge Cases & Fallback

| 场景 | 处理 |
|------|------|
| PRD 含糊不全 | 在 Step 3 多问，把缺失项写进 Open Questions / 假设 |
| 没有真实痛点代码可引 | 用最小可信的示例代码代替，并注明是构造的 |
| 没有备选方案可写 | 强迫思考"最朴素的做法是什么、为什么不够"——总有一个被否决的基线 |
| 不是破坏性变更 | Compatibility 一句话说明"纯增量、无破坏"，不必硬凑 |
| 方案最终被否决 | 照样写好——记录"这条路为什么走不通"本身就是高价值产物，Status 标 Rejected |
| 特性太大 | 拆成多篇 design doc（按边界），互相链接 |
| 用户只要实现契约 | 提示改用 `/prd-to-spec`，或先 to-design 再 prd-to-spec |

---

## Anti-Patterns to Avoid

- **别只论证你选的方案。** 不写被放弃的备选项，文档就少了一半价值。
- **别用形容词讲痛点。** "现状很糟"没有说服力；一段真实的 bug 代码才有。
- **别藏代价。** 性能变慢、行为变化、迁移成本——都明说，再给迁移路径。
- **别把标题写成名词。** "兼容性" → "老代码不受影响，编译结果完全一致"。
- **别用无主语的被动腔。** 决策要有人负责，主语用"我们"。
- **别写成 SPEC。** 设计文档讲"为什么这么选"和"取舍"，不是字段级的实现契约。
- **别因为方案可能被否就敷衍。** 文档质量与提案是否通过无关。

---

## Relationship to Other Skills

```
/prd  →  /to-design  →  /prd-to-spec  →  /goal  →  /review-it  →  /ship-it
 │            │               │              │
 │ 需求(what) │ 决策与取舍     │ 实现契约(how) │ 编码
 │            │ (why/which)   │
```

- **/prd** 产出 PRD（本 skill 的输入）
- **/to-design** 产出设计文档：论证方案、暴露取舍、对齐认知（本 skill）
- **/prd-to-spec** 产出实现级 SPEC：字段、接口、schema 契约
- **/code-to-spec** 从既有代码逆向出 SPEC（互补：正向 vs 逆向）

> 写设计文档的终极目的不是"说服别人同意你"，而是"让所有人在同一个事实和取舍基础上做决定"。

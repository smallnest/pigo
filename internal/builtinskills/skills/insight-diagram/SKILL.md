---
name: insight-diagram
description: 为任意项目生成 UML 图、架构图和流程图。分析代码库后让用户选择要生成的图表类型，使用 architecture-diagram skill 渲染为 HTML+SVG，保存到 docs/ 目录。适用于任何软件项目的文档可视化。
---

# Insight Diagram — 项目图表生成技能

分析任意代码库，自动生成 UML 14种图 + 架构图 + 流程图，使用 `/architecture-diagram` 渲染为 HTML+SVG。

## 图表分类与清单

### 结构性图形 (Structural Diagrams — 静态)
描述系统的物理组成和静态结构。

| 编号 | 图表类型 | 英文标识 | 关注点 |
|------|---------|---------|--------|
| 1 | 系统架构图 | architecture | 组件关系、全局视角（非UML，最常用） |
| 2 | 类图 | class | 定义类、属性、操作及关系 |
| 3 | 对象图 | object | 特定时刻的对象实例及其关系 |
| 4 | 组件图 | component | 系统组件及其依赖关系 |
| 5 | 部署图 | deployment | 物理硬件、节点及软件部署 |
| 6 | 包图 | package | 将模型元素分组组织 |
| 7 | 复合结构图 | composite-structure | 类的内部结构 |
| 8 | 剖面图 | profile | 扩展UML元模型、自定义构造型 |

### 行为性图形 (Behavioral Diagrams — 动态)
描述系统与外部参与者或系统内部的交互过程。

| 编号 | 图表类型 | 英文标识 | 关注点 |
|------|---------|---------|--------|
| 9 | 流程图 | flowchart | 主流程与分支（非UML，最常用） |
| 10 | 用例图 | usecase | 从用户角度展示系统功能 |
| 11 | 活动图 | activity | 过程的流程或步骤 |
| 12 | 状态机图 | state-machine | 对象生命周期的状态变迁 |
| 13 | 序列图 | sequence | 按时间顺序展示对象间交互 |
| 14 | 通信图 | communication | 侧重于对象间的组织关系 |
| 15 | 定时图 | timing | 侧重于状态变化的时间约束 |
| 16 | 交互概览图 | interaction-overview | 结合活动图和时序图 |
| 17 | 泳道图 | swimlane | 跨组件/角色职责流程（活动图变体） |

## 示例参考

本技能的 `examples/` 目录包含 13 个已完成的图表 HTML 文件，作为视觉样式和内容结构的参考模板。**生成任何图表前，必须先阅读对应的示例文件**，以确保风格一致、结构规范。

### 示例文件清单

| 文件 | 图表类型 | 英文标识 |
|------|---------|---------|
| `examples/architecture.html` | 系统架构图 | architecture |
| `examples/class.html` | 类图 | class |
| `examples/object.html` | 对象图 | object |
| `examples/component.html` | 组件图 | component |
| `examples/deployment.html` | 部署图 | deployment |
| `examples/flowchart.html` | 流程图 | flowchart |
| `examples/usecase.html` | 用例图 | usecase |
| `examples/activity.html` | 活动图 | activity |
| `examples/sequence.html` | 序列图 | sequence |
| `examples/communication.html` | 通信图 | communication |
| `examples/dfd.html` | 数据流图 | dfd |
| `examples/interaction-overview.html` | 交互概览图 | interaction-overview |
| `examples/swimlane.html` | 泳道图 | swimlane |

### 参考规则

1. **生成前必读**: 调用 `/architecture-diagram` 前，先用 Read 工具阅读对应类型的示例文件，从中提取：
   - SVG 布局策略（节点间距、分组方式、箭头走向）
   - 节点样式层级（核心节点 accent 高亮、普通节点实线边框、可选节点虚线边框）
   - 标注风格（阶段标签、Legend 图例、卡片摘要）
   - 信息密度（每个节点显示多少字段/属性）

2. **结构对齐**: 生成的图表应与示例保持相同的结构层次：
   - 页面顶部：标题 + 副标题 + 图表类型说明
   - 中间主体：SVG 图表区域（带浅色边框容器）
   - 底部：信息摘要卡片 + 页脚

3. **内容替换而非照搬**: 示例中的业务数据（NovaShield 风控系统）是虚构的参考案例，生成时需替换为目标项目的真实架构信息。只参考布局和样式，不复制业务内容。

4. **无对应示例的类型**: 对于包图 (package)、复合结构图 (composite-structure)、剖面图 (profile)、状态机图 (state-machine)、定时图 (timing) 这 5 种没有示例文件的图表类型，参考最相近的已有示例（如包图参考组件图，状态机图参考活动图），并沿用相同的视觉语言。

## 执行流程

### 步骤 1：分析代码库

读取项目关键文件，提取架构信息：

1. 读取项目根目录的 `CLAUDE.md`（如存在）获取项目概览
2. 读取各子目录的 `CLAUDE.md`（如存在）获取模块细节
3. 用 Glob 扫描源码文件结构（`**/*.go`, `**/*.py`, `**/*.ts` 等）
4. 读取入口文件（`main.go`, `app.py`, `index.ts` 等）识别顶层组件
5. 用 Grep 搜索关键模式：接口定义、函数签名、依赖注入、配置项

从以上信息中提炼出：
- **组件清单**: 服务、模块、外部依赖
- **关系图**: 谁调用谁、谁依赖谁、数据流向
- **核心类型**: 结构体/类、接口、枚举
- **流程**: 主业务流程、异常处理流程
- **部署**: 进程、中间件、外部服务

### 步骤 2：选择图表

使用 AskUserQuestion 让用户选择要生成的图表（multiSelect: true），分4组展示：

**第1组 — 结构性图形（静态）：**
- 系统架构图 (architecture)
- 类图 (class)
- 对象图 (object)
- 组件图 (component)

**第2组 — 结构性图形续 + 部署：**
- 部署图 (deployment)
- 包图 (package)
- 复合结构图 (composite-structure)
- 剖面图 (profile)

**第3组 — 行为性图形（动态）：**
- 流程图 (flowchart)
- 用例图 (usecase)
- 活动图 (activity)
- 状态机图 (state-machine)

**第4组 — 交互图 + 常用非UML：**
- 序列图 (sequence)
- 通信图 (communication)
- 交互概览图 (interaction-overview)
- 泳道图 (swimlane)
- 全部生成 (all)

默认推荐：architecture + sequence + flowchart

### 步骤 3：逐个生成

对每个选中的图表类型：

1. **先读示例**: 用 Read 工具阅读 `examples/<标识>.html`（如 `examples/architecture.html`），提取布局模式、节点样式、标注方式
2. 根据步骤 1 提取的架构信息，整理出该图表应展示的元素和关系
3. 调用 `/architecture-diagram` skill，传入图表类型、标题、内容描述、输出路径，**必须指定 light 风格**
4. 输出文件保存到 `docs/<标识>.html`（如 `docs/architecture.html`）
5. **生成后必须 review**: 运行几何校验脚本，按结果修正后再继续下一张（见下方「步骤 3.5」）
6. 简要报告完成状态

**生成规则：**
- **风格**: 必须使用 light Claude 风格（暖白背景 #FAF9F6、terracotta/sage/plum/rose 配色、Inter 字体、白色卡片容器），与 Anthropic Claude 品牌视觉一致
- **防遮盖**: 所有 SVG 元素（节点、箭头、标签）不得互相遮盖。具体做法：
  - 计算每个元素的边界框，确保无重叠
  - 箭头绘制在节点下方（SVG 中先画箭头再画节点）
  - 节点间留足间距（垂直最少 40px，水平最少 30px）
  - 文字不超出所在节点边界，超长文字截断或换行
  - 连接线的标签放置在线段中点偏移处，避免覆盖线段或节点
  - 如果元素过多导致图表拥挤，拆分为多个子图或缩小元素尺寸

批量生成顺序（宏观→微观）：
architecture → component → deployment → package → composite-structure → profile → class → object → usecase → flowchart → activity → state-machine → swimlane → sequence → communication → timing → interaction-overview

### 步骤 3.5：几何 review（每张图生成后必做）

生成的 SVG 常见三类几何缺陷，必须用脚本逐张校验并修正：

```bash
python3 skills/insight-diagram/scripts/review_svg.py docs/<标识>.html --min-gap 8
# 批量： python3 skills/insight-diagram/scripts/review_svg.py docs/*.html --min-gap 8
```

脚本检查（与三条核心要求一一对应）：

1. **箭头落点**：每个带箭头的端点必须恰好落在目标框/椭圆/菱形的**边缘线**上（容差 6px）。
   - `ERROR 深入框内`：端点穿入框内部 >8px → 缩短连线，让它止于边缘。
   - `WARNING 空接`：端点悬空、距最近框 >8px 且不汇入任何其它连线 → 把端点对齐到框边或汇合点。
   - 合法情形：端点落在框边、生命线、或与另一条连线交汇（分支/汇聚）——脚本不会误报。
2. **框重叠**（`ERROR`）：非嵌套的两个框在水平、垂直两个方向都有交叠 → 必须移开其中一个。嵌套（一个完全包住另一个，如分组边界框包子节点）是允许的。
3. **框间距**（`WARNING`）：投影相邻的两框净间距 < `--min-gap`（默认 8px）→ 拉开距离。

处理原则：
- **ERROR 必须修复**后再进入下一张；修完重跑脚本确认归零。
- **WARNING 逐条核对**：序列图生命线底部的消息、泳道边界、紧贴的分组等可能是设计本意，确认无误可保留；其余应调整坐标。
- 修正方式是直接编辑 `docs/<标识>.html` 里对应的 `<rect>/<line>/<path>` 坐标，而非重新生成整张图。
- 退出码：有 ERROR 返回 1，干净返回 0；CI 中可加 `--strict` 让 WARNING 也阻断。

### 步骤 4：报告

全部完成后输出：
- 生成的文件列表
- 每个图表的简要描述

## 各图表的内容指南

### 系统架构图 (architecture) — 非UML，最常用
- 展示系统顶层组件及其连接关系
- 区分内部模块与外部依赖
- 标注核心数据流方向

### 类图 (class)
- 核心类型为类节点（名称+字段+方法）
- 继承、组合、依赖关系
- 接口与实现分离
- 限制在 10-15 个核心类型

### 对象图 (object)
- 选取一个典型运行时场景
- 展示对象实例及其属性值
- 对象间的链接关系

### 组件图 (component)
- 每个组件为一个节点
- 箭头表示依赖/调用方向
- 标注接口名称

### 部署图 (deployment)
- 物理节点（服务器、容器、Serverless）
- 中间件（消息队列、缓存、数据库）
- 外部服务（第三方 API）
- 标注通信协议

### 包图 (package)
- 按模块/命名空间分组
- 包间依赖关系
- 体现分层架构

### 复合结构图 (composite-structure)
- 类/组件的内部结构
- 部件(Part)与连接器(Connector)
- 端口(Port)与接口

### 剖面图 (profile)
- 自定义构造型(Stereotype)
- 扩展元模型的标签定义(Tagged Values)
- 领域特定建模约束

### 流程图 (flowchart) — 非UML，最常用
- 主流程 + 关键分支
- 失败/异常路径
- 起止节点清晰

### 用例图 (usecase)
- 参与者（人/外部系统）
- 用例椭圆
- include/extend 关系

### 活动图 (activity)
- 阶段/步骤为活动节点
- 并行分支用 fork/join
- 决策点用菱形

### 状态机图 (state-machine)
- 对象的关键状态
- 触发状态变迁的事件
- 动作/守卫条件
- 初始态和终态

### 序列图 (sequence)
- 参与者为纵向生命线
- 水平箭头为消息调用
- 标注关键返回值
- 关注 2-5 个核心交互场景

### 通信图 (communication)
- 组件为节点，消息为连线
- 标注消息序号
- 强调协作关系而非时序

### 定时图 (timing)
- 时间轴横向展开
- 状态变化的时间约束
- 持续时间标注

### 交互概览图 (interaction-overview)
- 控制流节点内嵌交互片段
- 展示条件分支和循环
- 宏观概览各交互场景

### 泳道图 (swimlane) — 活动图变体
- 按组件/角色分泳道
- 流程步骤在对应泳道内
- 跨泳道箭头表示交互

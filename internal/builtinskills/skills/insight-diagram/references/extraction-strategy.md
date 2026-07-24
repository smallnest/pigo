# 图表类型内容提取策略

每种图表需要从代码库中提取不同维度的信息。以下是通用的提取策略，适用于任何语言/框架的项目。

## 通用提取规则

### 组件识别
- 入口文件中的初始化/注册代码
- 依赖注入容器（Wire, Spring, 等）
- 包/模块的公开接口
- 配置文件中引用的外部服务

### 关系识别
- import/require 语句
- 函数调用链（谁调用了谁）
- 接口实现关系
- 事件发布/订阅

### 数据流识别
- 函数参数和返回值
- 消息队列的 topic/producer/consumer
- API endpoint 的 request/response
- 数据库读写操作

### 流程识别
- 主循环 / 事件循环
- 中间件链 / handler 链
- 状态机转换
- 错误处理 / 重试逻辑

## 按语言的搜索模式

### Go
- 接口: `type \w+ interface`
- 结构体: `type \w+ struct`
- 函数签名: `func \([^)]+\) \w+`
- 依赖注入: `New\w+\(.*\w+Client`
- Goroutine/Channel: `go func`, `chan `
- 错误处理: `if err != nil`

### Python
- 类: `class \w+`
- 函数: `def \w+`
- 装饰器: `@\w+`（路由、依赖注入）
- 异步: `async def`, `await `
- 导入: `from .* import`, `import `

### TypeScript/JavaScript
- 类: `class \w+`
- 接口: `interface \w+`
- 导入: `import .* from`
- 路由: `app\.(get|post|put|delete)`
- 中间件: `\.use\(`

## 信息提取深度

- **架构图/组件图/部署图**: 只需包级/模块级信息，读 CLAUDE.md + 入口文件即可
- **序列图/通信图**: 需要函数调用链，读关键源码文件
- **类图/对象图**: 需要类型定义，读 types/model 文件
- **流程图/活动图/泳道图**: 需要主流程代码，读 pipeline/orchestrator/handler 文件
- **数据流图**: 需要数据结构 + 变换逻辑，读 processor/converter 文件
- **用例图**: 读 CLAUDE.md + router/api 文件

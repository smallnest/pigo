# PRD: Remote Control（手机端远程控制 CLI 会话）

## Introduction/Overview

在命令行中输入 `/remote-control` 斜杠命令后，CLI 会在本地启动一个轻量 Web 服务，并打印出访问地址与二维码。用户用手机（与电脑在同一局域网）扫码或打开链接，即可在手机浏览器里继续与 agent 交互：查看实时输出、输入新指令、以及在 agent 遇到危险操作时进行确认。

这解决的问题是：当用户离开电脑（如去开会、在沙发上、遛狗时），仍希望跟进和控制正在运行的 agent 会话，而不必守在键盘前。

本功能完全内置于现有 CLI，与当前会话共享同一份会话状态；每次输入 `/remote-control` 创建一个临时会话，CLI 进程退出即结束。

## Goals

- 输入 `/remote-control` 后，3 秒内在终端打印可访问的局域网 URL 与二维码
- 手机 Web 端可实时（延迟 < 1s）看到 CLI 会话的输出流
- 手机 Web 端可向 agent 发送新指令，效果等同于在 CLI 中直接输入
- 当 agent 需要确认危险操作时，手机端可显示确认弹窗并批准/拒绝
- 通过一次性、时效性的 token 配对保障安全，未授权者无法访问

## User Stories

### US-001: 实现 `/remote-control` 斜杠命令入口
**Description:** As a CLI user, I want to type `/remote-control` to start a remote session so that I can control the agent from my phone.

**Acceptance Criteria:**
- [ ] 在 CLI 中输入 `/remote-control` 被识别为内置命令并触发远程控制流程
- [ ] 命令执行后不阻塞主 CLI 会话，用户仍可在终端继续操作
- [ ] 未知参数或重复调用时返回明确提示（如 "remote control already running at <URL>"）
- [ ] Typecheck/lint passes

### US-002: 启动本地 Web 服务并绑定局域网地址
**Description:** As a CLI user, I want the CLI to serve a web page on my LAN so that my phone can reach it.

**Acceptance Criteria:**
- [ ] 启动 HTTP 服务并绑定到局域网可访问的网卡地址（非仅 127.0.0.1）
- [ ] 自动选择一个可用端口，端口被占用时回退到下一个可用端口
- [ ] 服务地址形如 `http://<lan-ip>:<port>`，可被同网段设备访问
- [ ] CLI 退出时服务随之关闭，端口被释放
- [ ] Typecheck/lint passes

### US-003: 在终端打印访问 URL 与二维码
**Description:** As a CLI user, I want a URL and QR code printed in my terminal so that I can open it on my phone quickly.

**Acceptance Criteria:**
- [ ] 终端打印包含配对 token 的完整访问 URL
- [ ] 终端渲染该 URL 对应的二维码（ASCII/Unicode 形式，可在标准终端扫描）
- [ ] URL 中的 token 为一次性、带过期时间
- [ ] Typecheck/lint passes

### US-004: 一次性 token 配对与鉴权
**Description:** As a security-conscious user, I want the remote link protected by a one-time, time-limited token so that only I can access the session.

**Acceptance Criteria:**
- [ ] 生成随机、高熵的一次性配对 token（至少 128 bit）
- [ ] token 有过期时间（默认 10 分钟内完成首次配对，`[Assumption]`）
- [ ] 首次配对成功后，服务端签发会话凭证（cookie/session token），后续请求凭此鉴权
- [ ] 携带无效/过期 token 的请求返回 401 并显示明确错误页
- [ ] Typecheck/lint passes
- [ ] Verify in a browser（e.g., via the `run` skill）

### US-005: 手机 Web 端实时查看会话输出
**Description:** As a user on my phone, I want to see the agent's live output so that I can follow what it's doing.

**Acceptance Criteria:**
- [ ] Web 页面通过实时通道（如 WebSocket/SSE）持续接收 CLI 会话输出
- [ ] 新输出到达时页面自动追加并滚动到底部
- [ ] 页面在手机浏览器上响应式布局，无横向滚动
- [ ] 断开连接时显示 "disconnected" 状态提示
- [ ] Typecheck/lint passes
- [ ] Verify in a browser（e.g., via the `run` skill）

### US-006: 手机 Web 端发送指令给 agent
**Description:** As a user on my phone, I want to type and send instructions so that the agent continues working based on my input.

**Acceptance Criteria:**
- [ ] Web 页面提供输入框与发送按钮
- [ ] 发送的文本被注入当前 CLI 会话，等效于在终端输入该内容
- [ ] 发送后输入框清空，且该指令回显在输出流中
- [ ] 空输入不可发送（发送按钮禁用或提示）
- [ ] Typecheck/lint passes
- [ ] Verify in a browser（e.g., via the `run` skill）

### US-007: 手机端确认危险操作
**Description:** As a user on my phone, I want to approve or reject risky actions so that the agent doesn't perform destructive operations without my consent.

**Acceptance Criteria:**
- [ ] 当 agent 触发需要确认的操作时，手机端弹出确认对话框并展示操作描述
- [ ] 用户点击 "批准" 后 agent 继续执行，点击 "拒绝" 后 agent 中止该操作
- [ ] 确认结果同步回 CLI 终端（终端显示已由远程批准/拒绝）
- [ ] 未响应时操作保持等待状态，不自动执行
- [ ] Typecheck/lint passes
- [ ] Verify in a browser（e.g., via the `run` skill）

### US-008: 临时会话生命周期管理
**Description:** As a user, I want the remote session to end cleanly when I stop it or exit the CLI so that no stale server keeps running.

**Acceptance Criteria:**
- [ ] CLI 进程退出（正常退出或 Ctrl-C）时，Web 服务、WebSocket 连接、临时 token 全部清理
- [ ] 提供停止方式（如再次输入命令或 `/remote-control stop`）主动结束远程会话
- [ ] 会话结束后再访问旧 URL 返回明确的 "session ended" 页面
- [ ] Typecheck/lint passes
- [ ] Verify in a browser（e.g., via the `run` skill）

## Functional Requirements

- FR-1: The system must register `/remote-control` as a built-in CLI slash command.
- FR-2: The system must start a local HTTP server bound to a LAN-accessible interface when the command runs.
- FR-3: The system must automatically select an available port and fall back to the next one if occupied.
- FR-4: The system must print the access URL to the terminal.
- FR-5: The system must render a scannable QR code of the access URL in the terminal.
- FR-6: The system must generate a one-time, high-entropy pairing token embedded in the URL.
- FR-7: The system must expire the pairing token after a configurable timeout.
- FR-8: The system must issue a session credential upon successful pairing and require it for subsequent requests.
- FR-9: The system must reject requests with invalid or expired tokens with an HTTP 401 response.
- FR-10: The system must stream live CLI session output to the web client over a real-time channel.
- FR-11: The system must render a responsive web page usable on mobile browsers.
- FR-12: The system must inject text submitted from the web client into the current CLI session as user input.
- FR-13: The system must present a confirmation dialog on the web client when the agent requests approval for a risky operation.
- FR-14: The system must proceed with a risky operation only after receiving an approve response from the web client.
- FR-15: The system must abort a risky operation upon a reject response from the web client.
- FR-16: The system must reflect remote approval/rejection results in the CLI terminal.
- FR-17: The system must share the active CLI session state with the remote web client (same session, not a copy).
- FR-18: The system must terminate the web server and clean up all connections and tokens when the CLI process exits.
- FR-19: The system must provide a way to stop the remote session while keeping the CLI running.

## Non-Goals (Out of Scope)

- 不支持通过公网/云端中继访问（仅同一局域网）
- 不提供 PWA、桌面安装或推送通知（本期为纯响应式 Web 页面）
- 不支持多个手机端同时控制同一会话（本期单一远程客户端，`[Assumption]`）
- 不做跨 CLI 进程的会话持久化或断线重连（进程退出即结束）
- 不支持固定密码/PIN 登录（仅一次性 token 配对）
- 不做历史会话回放或云端存档

## Design Considerations

- 手机端为单页响应式布局：顶部状态栏（连接状态）、中部输出流、底部输入框，确认弹窗为模态层
- 终端二维码建议使用 Unicode 半块字符渲染，兼顾终端宽度与可扫描性
- 复用现有 CLI 的会话渲染/输出管道，避免重复实现输出格式化

## Technical Considerations

- 实时通道优先考虑 WebSocket（双向），若实现成本高可用 SSE + POST 回退
- 需要探测本机局域网 IP（可能存在多网卡，需选择正确的可路由地址）
- Web 服务与 CLI 主循环共享同一进程与会话状态，需注意并发写入会话时的顺序与竞态
- 危险操作确认需与现有 safety/确认机制对接：远程批准应等价于本地批准
- token 与会话凭证不落盘，仅存于内存，随进程结束销毁

## Success Metrics

- 从输入 `/remote-control` 到手机成功打开页面的中位耗时 < 15 秒
- 会话输出从 CLI 到手机端的端到端延迟 < 1 秒
- 手机端发送的指令 100% 被注入到正确会话且顺序正确
- 危险操作在未收到远程批准前 0 次自动执行

## Open Questions

- 危险操作确认在手机端超时未响应时，是否需要一个默认策略（一直等待 vs. 提示回到终端处理）？
- 二维码/URL 的 token 过期时间默认值（当前假设 10 分钟）是否合适？
- 是否需要在 CLI 端显示 "已有手机端连接" 的可见提示，以防他人接入未察觉？

---
name: loop-it
description: "Automated issue loop with checkpoint/resume: fetch open GitHub issues → dependency-aware topological sort → implement each issue end-to-end → review with /review-it → document with /note-it → ship with /ship-it → repeat. Persists state to .loop-state.json for crash recovery. Triggers on: loop-it, loop issues, auto implement, 批量实现, 循环实现, 实现所有issue, 恢复循环, resume loop."
user-invocable: true
allowed-tools:
  - Bash(gh:*)
  - Bash(git:*)
  - Bash(cat:*)
---

# loop-it — 带检查点恢复的自动化 Issue 实现循环

Fetch all open GitHub issues, resolve dependency order, implement each through the full pipeline (内联实现 → `/review-it` → `/note-it` → `/ship-it`), persist progress to state file, and resume from checkpoint on crash.

> **⚠️ 关键前提：实现步骤由 agent 内联自主完成，不依赖任何外部 `/goal` 命令。**
> 本环境中不存在可调用的 `goal` 命令或 skill。因此「实现 issue」这一步**必须由 agent 内联完成**：直接读取该 issue 的标题与正文（含其引用的 PRD/SPEC 与验收条件），自主完成"理解需求 → 写/改代码 → 跑测试与 lint → 满足全部验收条件"的闭环，持续工作直到该 issue 的验收条件全部满足且测试/构建通过。**不要**尝试用 Skill 工具调用 `goal`（会报 `goal is a UI command, not a skill`），也**不要**因为找不到 `/goal` 而中止循环。`/review-it`、`/note-it`、`/ship-it` 仍是真实 skill，经 Skill 工具调用。

---

## Overview

```
前置检查 → 读取状态文件 → Fetch Issues → 构建依赖图 → 拓扑排序
                                                              |
    ┌───────────────────────────────────────────────────────────┘
    |
    v
┌──────────────── 单 Issue 循环 ────────────────┐
|                                               |
|  从检查点恢复？—— 跳过已完成/失败的             |
|                                               |
|  分支准备 (checkout main, pull, create branch) |
|        |                                      |
|  Skip/Blocked? ── 是 → 标记 skipped/blocked, 写检查点 |
|        |                                      |
|        否                                      |
|        |                                      |
|  内联实现 → 出错？→ 分类 → 恢复 → 重试        |
|        |              |                       |
|        |           失败 → 检查点, 下一个         |
|        |                                      |
|  /review-it → 有问题？→ 修复 → 重跑 review     |
|        |                                      |
|  /note-it (捕获实现笔记, best-effort)          |
|        |                                      |
|  /ship-it → 出错？→ 分类 → 恢复                |
|        |                                      |
|  分支清理 (checkout main, pull, delete branch) |
|        |                                      |
|  检查点 (标记 shipped)                         |
|        |                                      |
└────────┴──────────────────────────────────────┘
         |
         v
    全部完成 → 最终 Summary
```

---

## 前置检查

开始循环前，按顺序验证所有前提条件。任何检查失败则停止并打印错误。

### Check 1: gh CLI 认证

```bash
gh auth status
```

失败 → 打印 `❌ gh CLI 未认证。运行: gh auth login`，退出。

### Check 2: Git 仓库

```bash
git rev-parse --is-inside-work-tree
```

失败 → 打印 `❌ 不在 git 仓库中`，退出。

### Check 3: Git 工作树清洁度

```bash
git status --porcelain
```

有输出（dirty）→ 打印 `⚠️ 工作树有未提交的更改`，提供选项：
- A. `git stash` 暂存后继续
- B. 中止，让用户自行处理
- C. 强制继续（不推荐）

默认 B。

### Check 4: 在默认分支上

```bash
git branch --show-current
```

不在 main/master → 打印 `⚠️ 当前在 {branch} 分支`，提供选项：
- A. `git checkout main && git pull` 切换
- B. 继续在当前分支

### Check 5: 远程可达

```bash
git ls-remote --heads origin
```

失败 → 打印 `❌ 无法访问远程仓库。检查网络和权限`，退出。

### Check 6: 状态文件存在？

```bash
cat .loop-state.json
```

存在 → 打印进度摘要，提供选项：
- A. 从检查点恢复
- B. 从头开始（删除状态文件）
- C. 中止

---

## 状态文件

### 位置

`.loop-state.json`，放在 repo 根目录。**必须添加到 `.gitignore`**。如果文件被 git 跟踪，打印警告并建议用户添加到 `.gitignore`。

### 格式

```json
{
  "version": 1,
  "started_at": "2025-06-09T10:00:00Z",
  "updated_at": "2025-06-09T10:30:00Z",
  "repo": "owner/repo-name",
  "total_issues": 8,
  "issues": {
    "3": {
      "status": "shipped",
      "branch": "feat/issue-3-add-priority",
      "started_at": "2025-06-09T10:00:00Z",
      "completed_at": "2025-06-09T10:15:00Z",
      "attempts": 1
    },
    "4": {
      "status": "failed",
      "phase": "goal",
      "error_class": "build_failure",
      "branch": "feat/issue-4-filter-tasks",
      "started_at": "2025-06-09T10:15:00Z",
      "updated_at": "2025-06-09T10:30:00Z",
      "attempts": 3,
      "last_error": "test TestFilterPriority failed: expected 3, got 0"
    },
    "7": {
      "status": "pending"
    }
  }
}
```

### 状态值

`pending` | `in_progress` | `skipped` | `shipped` | `failed` | `blocked`

### 写入规则

- 每次状态转换后立即写入（`pending` → `in_progress`、`in_progress` → `shipped`/`failed`/`skipped` 等）
- 写入使用 `cat > .loop-state.json << 'LOOPSTATE'\n{json}\nLOOPSTATE`
- 如果状态文件已存在但内容损坏（非法 JSON），打印警告，提供从头开始或中止的选项。**绝不自动覆盖损坏文件**
- 循环完成后保留状态文件（作为记录），用户可手动删除

---

## Step 1: Fetch Issues & Build Dependency Graph

Fetch all open issues:

```bash
gh issue list --state open --json number,title,labels,body --limit 100
```

### Parse Dependencies

Read each issue body, look for patterns:
- `Dependencies: #3, #5` or `Depends on: #3`
- `depends on #3` or `requires #3` (in body text)

Build a dependency graph. Sort using topological order:

1. Issues with no dependencies first (sorted by number ascending)
2. Issues whose dependencies are all shipped/closed next
3. Blocked issues (depend on other open issues) last
4. Circular dependencies → print warning `⚠️ 循环依赖检测到: #A ↔ #B，按编号顺序处理`，break cycle by number order

If no dependency patterns found in any issue body, fall back to number-ascending sort.

Print ordered list:

```
📋 Found N open issues (topological sort):
  #1: Add priority field (无依赖)
  #3: Display indicator (依赖 #1)
  #5: Add selector (依赖 #1)
  #7: Filter view (依赖 #1, #3)
```

If no open issues → print `✅ No open issues found. Nothing to do.` and exit.

---

## Step 2: Resume or Initialize

### If `.loop-state.json` exists (from 前置检查 Check 6)

1. Read the file
2. Print progress summary:

```
📊 从检查点恢复 (上次更新: {updated_at})
   ✅ Shipped:  #1, #3
   ⏭️  Skipped:   #2 (question)
   ❌ Failed:    #4 (build_failure — 3 attempts)
   📋 Remaining: #5, #7
```

3. For each `failed` issue: ask user — retry or skip?
4. For `in_progress` issues: check if branch exists, changes exist → decide resume from current state or restart
5. Skip all `shipped`/`skipped` issues
6. Continue from first pending/retryable issue

### If no state file

1. Initialize new state file with all fetched issues as `pending`
2. Start from first issue in topological order

---

## Step 3: Process Single Issue

For each issue, print a banner:

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔄 Processing Issue #{number}: {title}
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### 3a. Branch Prep

Prepare a clean git environment for this issue:

```bash
# 确认在 main 上
git checkout main
git pull

# 创建功能分支
git checkout -b feat/issue-{N}-{short-desc}
```

Branch naming: `feat/issue-N-short-desc` or `fix/issue-N-short-desc`（与 /ship-it 保持一致）

### 3b. Skip or Implement

Read the issue title and body. Decide if it needs code implementation:

**Skip if the issue is:**
- A question / discussion / clarification
- Documentation-only (typos, wording)
- Already implemented (check codebase)
- A duplicate of another issue
- Clearly labeled `wontfix`, `question`, `discussion`, or `invalid`
- Not actionable (no clear acceptance criteria and cannot infer any)

**Skip (blocked) if the issue has unresolved dependencies:**
- Check the dependency graph from Step 1
- If any dependency issue is not `shipped` (still `pending`, `failed`, `blocked`, or not in state file) → skip as blocked
- The dependency issue itself may have failed or been skipped — in either case, this issue cannot proceed safely

When skipping:

```
⏭️  Skipping Issue #{number}: {title}
   Reason: {why}
```

When blocked:

```
🔒 Blocking Issue #{number}: {title}
   Reason: dependency #{dep_number} not shipped ({status})
```

Update state: `pending` → `skipped` or `pending` → `blocked`, write checkpoint, run **3h Branch Cleanup**, proceed to next issue.

### 3c. Implement (内联自主实现)

Update state: `pending` → `in_progress`, `phase: "implement"`, write checkpoint.

**由 agent 内联完成实现**（本环境无 `goal` 命令/skill 可调用，必须自己干）：

1. 读取该 issue 的标题与正文，提取需求与全部验收条件（Acceptance Criteria）；若正文引用了 PRD/SPEC 文件（如 `tasks/prd-*.md`），一并读取作为上下文
2. 阅读相关现有代码，遵循项目既有风格、命名与依赖约定
3. 实现/修改代码以满足全部验收条件
4. 跑项目的构建、测试与 lint（如 `go build ./...`、`go vet ./...`、`go test ./...`）
5. 持续工作直到该 issue 的验收条件**全部满足**且测试/构建/lint 通过

> 不要尝试用 Skill 工具调用 `goal`（会报 `goal is a UI command, not a skill`），也不要因找不到 `/goal` 而中止——实现就是你自己内联完成的工作。

**On success:**

```
✅ Issue #{number} implementation complete
```

Write checkpoint with `phase: "implement_done"`.

**On failure** — classify error (see 错误分类与恢复), apply recovery strategy, retry up to max attempts. If all retries exhausted:

```
⚠️  Issue #{number} failed: {error_class} after {N} attempts
   Manual intervention required.
```

Update state: `in_progress` → `failed`, write checkpoint, run **3h Branch Cleanup**, proceed to next issue.

### 3d. Review with /review-it

Write checkpoint with `phase: "review"`.

```
/review-it
```

**If review finds actionable issues:**

```
🔍 Review found N issue(s) for #{number}. Fixing...
```

Fix each accepted finding, re-run `/review-it`. Repeat until clean or max 2 review rounds.

**If review is clean:**

```
✅ Review clean for Issue #{number}
```

Write checkpoint with `phase: "review_done"`.

### 3e. Document with /note-it

After review, before ship — capture implementation notes:

```
/note-it
```

This creates `docs/issue#XXXX.html` with design decisions, deviations, tradeoffs, and open questions.

**On success:**

```
📝 Issue #{number} notes captured
```

**On failure** (can't determine issue number, etc.) — print warning but **do not block shipping**:

```
⚠️  /note-it failed for Issue #{number}: {reason}. Continuing to ship.
```

Write checkpoint with `phase: "note_done"`.

### 3f. Ship with /ship-it

```
/ship-it
```

This commits, pushes, creates PR, merges, and closes the issue.

**On success:**

```
🚀 Issue #{number} shipped successfully!
```

**On failure** — classify error (see 错误分类与恢复), apply recovery. If unresolvable:

```
⚠️  Issue #{number} ship failed: {error}. Manual merge required.
```

Update state: `in_progress` → `failed`, `phase: "ship"`, write checkpoint, run **3h Branch Cleanup**, proceed to next issue.

### 3g. Checkpoint

After successful ship, update state: `in_progress` → `shipped`, set `completed_at`, write checkpoint.

Print progress (see 进度可观测性).

### 3h. Branch Cleanup

After each issue (shipped, skipped, or failed):

```bash
# 切回 main
git checkout main
git pull

# 删除本地功能分支（仅当 shipped 时）
git branch -d feat/issue-{N}-{short-desc}
```

**For failed issues**: do NOT delete the branch. Keep it for investigation.

### Next Issue

Return to Step 3 for the next issue in topological order.

When all issues processed, print final summary:

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📊 Loop Complete — Summary
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  ✅ Shipped:   N issues  (#1, #3, ...)
  ⏭️  Skipped:   N issues  (#2 — reason, #5 — reason, ...)
  🔒 Blocked:   N issues  (#7 — depends on #4, ...)
  ❌ Failed:    N issues  (#4 — error, ...)
  📋 Total:     N issues
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

---

## 错误分类与恢复

当错误发生时，先分类，再按策略恢复。

| 错误类别 | 检测信号 | 恢复策略 | 最大重试 |
|----------|---------|---------|---------|
| build_failure | 编译错误、undefined、类型错误 | 读错误，修代码，重新构建 | 3 |
| test_failure | 断言失败、test failed | 读测试输出，修实现，重跑测试 | 3 |
| lint_failure | lint 错误、格式问题 | 自动修复 (lint --fix)，重跑 | 2 |
| merge_conflict | CONFLICT 标记 | rebase origin/main，解决冲突，push | 2 |
| ci_failure | gh pr checks 失败 | 读 CI 日志，本地修复，push | 2 |
| auth_failure | 403、401、认证错误 | 停止，告知用户重新认证 | 0 |
| rate_limit | rate limit、secondary abuse | 等待 60s，重试 | 3 |
| issue_unclear | issue 无验收条件且无法推断需求 | 跳过，标记 failed | 0 |
| network_error | timeout、connection refused | 等待 30s，重试 | 3 |
| unknown | 其他情况 | 记录完整错误，跳过 | 0 |

**恢复协议：**

1. 匹配错误类别
2. 匹配成功 → 应用恢复策略，重试最多 N 次
3. 重试全部失败 → 标记 `failed`，写检查点，继续下一个 issue
4. 无法匹配 → 标记 `failed`（error_class: `unknown`），继续
5. **绝不无限重试。绝不未经确认 force-push。**

---

## 进度可观测性

每完成一个 issue 后，打印结构化进度：

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📊 Progress: 3/8 issues (37%)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  ✅ Shipped:  #1, #3
  ⏭️  Skipped:   #2 (question), #6 (duplicate)
  🔒 Blocked:   #7 (depends on #4 — failed)
  ❌ Failed:    #4 (build_failure — 3 attempts)
  📋 Remaining: #5, #8
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

---

## Logging Rules

Every key step MUST print a log line with emoji prefix:

| Emoji | Meaning |
|-------|---------|
| 📋 | Fetch / list |
| 🔄 | Processing issue |
| ⏭️ | Skip |
| 🔒 | Blocked (dependency not shipped) |
| ✅ | Success |
| ❌ | Failure |
| 🔍 | Review |
| 📝 | Notes (/note-it) |
| 🚀 | Ship |
| ⚠️ | Warning / retry |
| 📊 | Progress / summary |

---

## Safety Guards

- **Never force-push to main/master** — always use feature branches
- **Never skip review** — always run `/review-it` before `/ship-it`
- **Never skip notes** — always run `/note-it` before `/ship-it`（best-effort，不阻塞）
- **Max retries per error class** — 参见错误分类与恢复表，不无限重试
- **Max 2 review rounds** — don't over-polish
- **Pause on CI failure** — log and continue, don't auto-override branch protection
- **Preserve issue labels** — only close issues that were actually shipped
- **Never auto-delete failed branches** — 保留供调查
- **Checkpoint at every transition** — 每次状态变更写检查点，不仅仅在 ship 时
- **State file integrity** — 损坏时警告用户，绝不自动覆盖
- **State file in .gitignore** — 提醒用户添加 `.loop-state.json`
- **Strictly sequential** — 一次只处理一个 issue（实现会修改工作树，不能并行）
- **Skip dependency-blocked issues** — 依赖的 issue 未 shipped 时标记 `blocked`
- **实现由 agent 内联完成** — 本环境无 `goal` 命令/skill 可调用；「实现 issue」必须由 agent 自己读 issue、写代码、跑测试完成。报 `goal is a UI command, not a skill` 时不要中止，直接内联实现

---

## Edge Cases

| Scenario | Handling |
|----------|----------|
| No open issues | Print "nothing to do" and exit |
| All issues are questions | Skip all, report summary |
| `gh` not authenticated | Print error, suggest `gh auth login`, exit |
| Issue has no body | Use title only to decide skip/implement |
| Issue references PRD/SPEC | 读取被引用的 PRD/SPEC 作为上下文，agent 内联实现 |
| Multiple issues depend on each other | Topological sort; dependencies already shipped first |
| Git repo is dirty before starting | 前置检查 Check 3: stash/abort/force |
| State file corrupted (invalid JSON) | 警告用户，提供从头开始或中止选项。绝不自动覆盖 |
| State file from different repo | 检测 repo 字段不匹配，警告，提供从头开始选项 |
| Issue `in_progress` from previous run | 检查分支是否存在、是否有变更 → 恢复或重新开始 |
| User aborts mid-loop | 状态文件已包含最新检查点，下次运行可恢复 |
| New issues created during loop | 不重新获取。完成当前批次后运行新 `/loop-it` |
| Circular dependencies | 打印警告，按编号顺序打破循环 |
| `/note-it` can't find issue number | 打印警告，跳过 /note-it，继续 /ship-it |
| `.loop-state.json` is git-tracked | 警告用户添加到 .gitignore，继续 |
| 误以为需要外部 `goal` 命令 | 本环境无此命令；「实现 issue」由 agent 内联完成（读 issue → 写代码 → 测试），不要中止循环 |

---

## Relationship to Other Skills

```
/loop-it
  ├── 内联实现   ← implement each issue（agent 自主读 issue、写代码、测试；非外部命令）
  ├── /review-it  ← review code before shipping（skill）
  ├── /note-it    ← capture implementation notes (best-effort)（skill）
  └── /ship-it    ← commit, PR, merge, close（skill）
```

Part of the goal-workflow pipeline:

```
/prd → /prd-to-spec → /to-issues → /loop-it (→ 内联实现 → /review-it → /note-it → /ship-it)×N
```

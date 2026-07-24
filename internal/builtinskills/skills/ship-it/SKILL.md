---
name: ship-it
description: Code commit, PR creation, merge, and issue closure workflow via GitHub CLI (gh). Triggers after a goal (GitHub Issue) implementation is complete — commit code, push branch, create PR, merge, then close the issue. Use when the user says "提交代码", "commit and merge", "创建PR", "合入", "关闭issue", "ship-it", or when a goal implementation is done and code needs to be shipped.
allowed-tools:
  - Bash(git:*)
  - Bash(gh:*)
---

# After-Goal: 代码提交、PR 合入、Issue 关闭工作流（GitHub）

完成 GitHub Issue 实现后的标准收尾流程：提交代码 → 推送分支 → 创建 PR → 合入 → 关闭 Issue。

## 前置条件

- 当前 git 仓库有已实现的代码变更
- 已知 Issue 编号（如 `#42`）
- gh CLI 已登录（`gh auth status` 可验证）

## 工作流

### Step 1: 提交代码

```bash
# 1a. 检查变更状态
git status
git diff --stat HEAD

# 1b. 暂存本次 Issue 相关的文件（不要 add 不相关的文件）
git add <files related to this issue>

# 1c. 提交，commit message 关联 Issue
git commit -m "$(cat <<'EOF'
{简要描述} (#issue-number)

{可选的详细说明}
EOF
)"
```

**关键规则：**
- commit message 中包含 `#issue-number` 以关联 Issue
- 只暂存当前 Issue 相关的文件，不要混入其他变更

### Step 2: 推送分支

```bash
# 如果还在 main/master 上，先创建功能分支
git checkout -b {branch-name}  # 如已在功能分支则跳过

# 推送到远程
git push -u origin {branch-name}
```

分支命名建议：`feat/issue-42-short-desc` 或 `fix/issue-42-short-desc`

### Step 3: 创建 PR

```bash
gh pr create \
  --title "{简要描述}" \
  --body "$(cat <<'EOF'
## Summary
- 实现内容概述

Closes #{issue-number}

## Test plan
- [ ] 测试项 1
- [ ] 测试项 2
EOF
)"
```

**关键规则：**
- PR body 中写 `Closes #N` 或 `Fixes #N`，合入后 GitHub 自动关闭 Issue
- title 简洁，不超过 70 字符

### Step 4: 合入 PR

```bash
# 4a. 查看 PR 状态（确认 checks 通过）
gh pr checks

# 4b. 合入（默认 merge commit，可选 --squash 或 --rebase）
gh pr merge --squash --delete-branch
```

**参数说明：**
- `--squash`: 压缩为单个 commit 合入（推荐）
- `--rebase`: rebase 合入
- `--merge`: 普通 merge commit
- `--delete-branch`: 合入后删除远程分支

### Step 5: 添加实现总结评论

PR 合入后，始终在 Issue 上添加实现总结评论，方便后续直接从 Issue 回溯代码变更。

```bash
gh issue comment {issue-number} --body "$(cat <<'EOF'
## 实现总结
- **核心变更**：{从 PR body 提取的实现摘要}
- **PR**: #{pr-number}
- **Commit**: {hash}
EOF
)"
```

**关键规则：**
- 无论是 auto-close 还是手动 close，都必须添加此评论
- 评论内容从 PR body 的 Summary 部分提取，保持简洁（3-5 条 bullet）
- 附加 PR 编号和 commit hash，方便直接跳转

### Step 6: 手动关闭 Issue（仅当未自动关闭时）

如果 PR body 中已写 `Closes #N`，合入后 Issue 会自动关闭，跳过此步。否则手动关闭：

```bash
gh issue close {issue-number} --reason completed
```

## 错误处理

| 场景 | 处理方式 |
|------|---------|
| `gh pr checks` 有失败项 | 查看失败原因，修复后追加 commit 推送 |
| PR 有 merge conflict | `git fetch origin main && git rebase origin/main`，解决冲突后 force push |
| `gh pr merge` 被 branch protection 阻止 | 确认 required reviews 已满足，或请 reviewer approve |
| Issue 合入后未自动关闭 | 确认 PR body 包含 `Closes #N`，或执行 Step 6 手动 `gh issue close` |

## 完整示例

```bash
# 创建分支并提交
git checkout -b feat/issue-42-case-model
git add cases/case.go cases/case_test.go
git commit -m "$(cat <<'EOF'
Add Case data model and Markdown read/write (#42)

Define Case struct with YAML frontmatter + Markdown body
serialization. Provide WriteCase/ReadCase/ListCases/UpdateCase.
EOF
)"

# 推送
git push -u origin feat/issue-42-case-model

# 创建 PR
gh pr create \
  --title "Add Case data model and Markdown read/write" \
  --body "$(cat <<'EOF'
## Summary
- Define Case struct with YAML frontmatter + Markdown body
- Implement WriteCase/ReadCase/ListCases/UpdateCase functions
- Add comprehensive test coverage

Closes #42

## Test plan
- [x] Unit tests pass
- [x] go vet / lint clean
EOF
)"

# 确认 checks 通过后合入
gh pr checks
gh pr merge --squash --delete-branch

# 添加实现总结评论
gh issue comment 42 --body "$(cat <<'EOF'
## 实现总结
- **核心变更**：Define Case struct with YAML frontmatter + Markdown body
- **核心变更**：Implement WriteCase/ReadCase/ListCases/UpdateCase
- **PR**: #43
- **Commit**: abc1234
EOF
)"

# 切回主分支
git checkout main
git pull
```

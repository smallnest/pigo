---
name: review-it
description: "Code review closeout for Claude Code, Codex, OpenCode, DeepSeek TUI, and Antigravity CLI: local dirty changes, branch vs main, parallel tests."
---

# CC Review

Run automated code review as a closeout check before committing or shipping. Works across multiple AI coding agents.

Use when:
- user asks for code review / review-it / autoreview
- after non-trivial code edits, before final/commit/ship
- reviewing a local branch or PR branch after fixes

## Supported Agents

| Agent | Review Command | Notes |
|-------|---------------|-------|
| Claude Code | `/review` | Built-in, works on uncommitted changes or diff |
| Codex | `codex review` | Pass diff file or let it auto-detect |
| OpenCode | `/review` | Same as Claude Code |
| DeepSeek TUI | `/review` or manual diff review | Pass diff content for analysis |
| Antigravity CLI | `/code-review` | Built-in slash command, auto-detects diff |

## Contract

- Treat review output as advisory. Never blindly apply it.
- Verify every finding by reading the real code path and adjacent files.
- Read dependency docs/source/types when the finding depends on external behavior.
- Reject unrealistic edge cases, speculative risks, broad rewrites, and fixes that over-complicate the codebase.
- Prefer small fixes at the right ownership boundary; no refactor unless it clearly improves the bug class.
- Keep going until review returns no accepted/actionable findings.
- If a review-triggered fix changes code, rerun focused tests and rerun review.
- Stop as soon as the review comes back clean with no actionable findings.
- If rejecting a finding as intentional/not worth fixing, add a brief inline code comment only when it explains a real invariant or ownership decision that future reviewers should know.
- Do not push just to review. Push only when the user requested push/ship/PR update.

## Review Focus

请 review 当前 diff。不要只看语法和明显 bug，请重点检查以下维度，最后按严重程度排序：

1. **隐藏副作用 (Hidden Side Effects)** — 变更是否在非显而易见的地方产生级联影响？是否修改了共享状态、全局变量、或外部依赖的行为？
2. **破坏兼容性 (Breaking Compatibility)** — 是否改变了 API 签名、数据结构、配置文件格式、或命令行接口？现有调用方是否会受影响？
3. **边界情况 (Edge Cases)** — null/空值/空集合、极大/极小值、并发/竞态条件、异常路径是否被正确处理？
4. **性能风险 (Performance Risks)** — 是否引入了不必要的循环嵌套、N+1 查询、大对象分配、阻塞 I/O、或锁竞争？
5. **安全风险 (Security Risks)** — 是否存在注入、越权、敏感信息泄露、不安全的反序列化、或依赖版本漏洞？
6. **命名误导 (Naming Misleading)** — 变量/函数/类型名称是否与实际行为不一致？是否存在名不副实或语义模糊的命名？
7. **测试不足 (Insufficient Testing)** — 关键路径、边界条件、错误处理是否缺少测试覆盖？现有测试是否真正验证了期望行为？
8. **未来维护成本 (Future Maintenance Cost)** — 是否引入了不必要的抽象、重复代码、隐式耦合、或难以追踪的控制流？后来者是否容易理解和修改？

## Pick Target

### Claude Code / OpenCode / DeepSeek TUI

Dirty local work (default — `/review` works on uncommitted changes):

```
/review
```

Branch/PR work — review all changes against base:

First generate a diff, then review it:

```bash
git diff origin/main...HEAD > /tmp/review-it.diff
```

Then review the diff file with a focused prompt:

```
/review the changes in /tmp/review-it.diff against origin/main
```

If an open PR exists, use its actual base:

```bash
base=$(gh pr view --json baseRefName --jq .baseRefName)
git diff "origin/$base"...HEAD > /tmp/review-it.diff
```

### Antigravity CLI (`agy`)

Dirty local work:

```
/code-review
```

Branch/PR work — review all changes against base:

```bash
git diff origin/main...HEAD > /tmp/review-it.diff
```

Then pass the diff to the review command:

```
/code-review the changes in /tmp/review-it.diff against origin/main
```

If an open PR exists, use its actual base:

```bash
base=$(gh pr view --json baseRefName --jq .baseRefName)
git diff "origin/$base"...HEAD > /tmp/review-it.diff
```

### Codex

```bash
# Review uncommitted changes
codex review

# Review branch diff
git diff origin/main...HEAD > /tmp/review-it.diff
codex review /tmp/review-it.diff
```

## Parallel Closeout

Format first if formatting can change line locations. Then it's OK to run tests and review in parallel:

```bash
scripts/review-it --parallel-tests "<focused test command>"
```

Tradeoff: tests may force code changes that stale the review. If tests or review lead to code edits, rerun the affected tests and rerun review until no accepted/actionable findings remain.

## Uncommitted vs Branch Review

Choose the right mode:

- **Uncommitted changes** (staged/unstaged): use `/review` directly (Antigravity: `/code-review`, Codex: `codex review`)
- **Committed, not pushed**: use `git diff origin/main...HEAD` + review
- **Pushed/PR**: same as committed, against the PR base
- **Clean working tree**: skip review if there's truly nothing to review

## Helper

Bundled helper script for parallel test + review orchestration:

```bash
~/.claude/skills/review-it/scripts/review-it --help
```

The helper:
- Detects which agent is running (Claude Code, Antigravity CLI, Codex) via `--agent auto`
- Detects whether to use uncommitted review or branch diff review
- For branch mode: generates diff against `origin/main` (or PR base), then triggers review
- Supports `--parallel-tests` for concurrent test + review execution
- Supports `--dry-run` for checking what command would be used
- Prints `review-it clean: no accepted/actionable findings reported` when review is clean

## Final Report

Include:
- review target (uncommitted / branch / PR base)
- tests/proof run
- findings accepted/rejected, briefly why
- the clean review result, or why a remaining finding was consciously rejected

Do not run another review solely to improve the final report wording. If review exited clean with no actionable findings, report that as clean.
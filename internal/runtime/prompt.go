// This file implements system-prompt assembly (US-021, #40), the pigo port of
// pi's prompt construction. A run's system prompt is built from three layers,
// in order:
//
//  1. a base instruction (who the agent is and how it should behave),
//  2. an environment block (working directory, OS/arch, current date), and
//  3. every AGENTS.md found on the path from a root directory down to the
//     working directory, concatenated general-to-specific.
//
// The AGENTS.md ordering mirrors zero/pi's monorepo behavior: a repo-root
// AGENTS.md states broad conventions, and a nested package's AGENTS.md refines
// them, so the more specific file appears later and takes precedence in the
// model's reading.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// agentsFileName is the per-directory instruction file injected into the system
// prompt, general-to-specific from the root down to the working directory.
const agentsFileName = "AGENTS.md"

// PromptConfig configures system-prompt assembly. The zero value is usable: it
// produces the base instruction plus an environment block for the process
// working directory, with no AGENTS.md injection.
type PromptConfig struct {
	// BaseInstruction is the leading text of the system prompt. When empty,
	// DefaultBaseInstruction is used.
	BaseInstruction string
	// WorkingDir is the directory the run operates in. When empty, the process
	// working directory (os.Getwd) is used. It anchors both the environment
	// block and the lower bound of the AGENTS.md walk.
	WorkingDir string
	// Root bounds the AGENTS.md walk at its top. AGENTS.md files are injected for
	// every directory from Root down to WorkingDir, inclusive. When empty, only
	// WorkingDir's own AGENTS.md (if any) is considered — no ancestor walk.
	Root string
	// Now supplies the timestamp for the environment block. When nil, time.Now
	// is used. Injected for deterministic tests.
	Now func() time.Time
	// ReadFile reads a file's contents. When nil, os.ReadFile is used. Injected
	// for tests so AGENTS.md layout can be faked without touching disk.
	ReadFile func(path string) ([]byte, error)
}

// DefaultBaseInstruction is the leading system-prompt text used when
// PromptConfig.BaseInstruction is empty.
const DefaultBaseInstruction = "You are pigo, a helpful coding agent. " +
	"Use the available tools to inspect files and accomplish the user's request precisely and concisely."

// BuildSystemPrompt assembles the full system prompt from cfg: base instruction,
// environment block, then AGENTS.md files ordered general-to-specific from Root
// down to WorkingDir. Missing AGENTS.md files are skipped silently; only a
// present-but-unreadable file (a real I/O error other than not-exist) is
// reported.
func BuildSystemPrompt(cfg PromptConfig) (string, error) {
	base := cfg.BaseInstruction
	if base == "" {
		base = DefaultBaseInstruction
	}
	wd := cfg.WorkingDir
	if wd == "" {
		if cwd, err := os.Getwd(); err == nil {
			wd = cwd
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	readFile := cfg.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}

	var b strings.Builder
	b.WriteString(base)

	b.WriteString("\n\nEnvironment:\n")
	fmt.Fprintf(&b, "- Working directory: %s\n", wd)
	fmt.Fprintf(&b, "- OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "- Date: %s", now().Format("2006-01-02"))

	dirs := agentsDirChain(cfg.Root, wd)
	for _, dir := range dirs {
		path := filepath.Join(dir, agentsFileName)
		data, err := readFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n\n# Project instructions (%s)\n%s", path, content)
	}

	return b.String(), nil
}

// agentsDirChain returns the directories whose AGENTS.md should be injected, in
// general-to-specific order (root first, working directory last). When root is
// empty or is not an ancestor of wd, only wd is returned. When wd is empty, the
// chain is empty.
func agentsDirChain(root, wd string) []string {
	if wd == "" {
		return nil
	}
	wd = filepath.Clean(wd)
	if root == "" {
		return []string{wd}
	}
	root = filepath.Clean(root)

	// Walk up from wd to root, collecting each directory, then reverse so the
	// root comes first (general → specific). If root is never reached, wd is not
	// under root, so fall back to wd alone.
	var up []string
	cur := wd
	for {
		up = append(up, cur)
		if cur == root {
			// Reverse in place: root-first.
			for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
				up[i], up[j] = up[j], up[i]
			}
			return up
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached filesystem root without hitting `root`: wd not under root.
			return []string{wd}
		}
		cur = parent
	}
}

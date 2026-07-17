// This file implements plugin discovery and lifecycle management (US-016, #132):
// finding plugin executables under a config directory, loading each, and
// aggregating their tools. Loading is fault-tolerant — one plugin that fails to
// start or handshake is logged and skipped so the rest still load.
package plugin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Manager owns a set of loaded plugins and their aggregated tools. It is not safe
// for concurrent modification; load once at startup, then read.
type Manager struct {
	plugins []*Plugin
}

// Discover finds and loads every plugin under dir. A plugin is any executable
// regular file directly inside dir (non-executable files and subdirectories are
// ignored). Each plugin is launched and handshaked; a failure is written to
// warnLog (when non-nil) and that plugin is skipped. A missing dir is not an
// error — it yields an empty Manager. pluginStderr, when non-nil, receives every
// plugin's stderr.
func Discover(dir string, warnLog, pluginStderr io.Writer) (*Manager, error) {
	m := &Manager{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil // no plugins directory → no plugins
		}
		return nil, fmt.Errorf("plugin: read dir %q: %w", dir, err)
	}
	// Deterministic load order for stable tool ordering and diagnostics.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !isExecutable(info.Mode()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, err := Load(path, nil, pluginStderr)
		if err != nil {
			if warnLog != nil {
				fmt.Fprintf(warnLog, "pigo: plugin %q failed to load: %v\n", e.Name(), err)
			}
			continue
		}
		m.plugins = append(m.plugins, p)
	}
	return m, nil
}

// isExecutable reports whether the file mode has any execute bit set.
func isExecutable(mode os.FileMode) bool {
	return mode&0o111 != 0
}

// Tools returns the aggregated tools of every loaded plugin, in load order.
func (m *Manager) Tools() []agentcore.AgentTool {
	var out []agentcore.AgentTool
	for _, p := range m.plugins {
		out = append(out, p.Tools()...)
	}
	return out
}

// Plugins returns the loaded plugins (for command aggregation and diagnostics).
func (m *Manager) Plugins() []*Plugin { return m.plugins }

// Subscribers reports whether any loaded plugin subscribes to the given event
// type. It lets a caller skip building an event payload when nobody is listening
// (US-017, #133).
func (m *Manager) Subscribers(eventType string) bool {
	for _, p := range m.plugins {
		if p.Subscribes(eventType) {
			return true
		}
	}
	return false
}

// DispatchEvent delivers one lifecycle event to every plugin subscribed to its
// type (US-017, #133). Delivery is best-effort and isolated: each plugin's send
// is bounded by eventTimeout, and a delivery failure to one plugin (timeout,
// dead process) is written to warnLog when non-nil and does not stop delivery to
// the others. It never blocks the agent loop beyond the per-plugin timeout.
func (m *Manager) DispatchEvent(params EventParams, warnLog io.Writer) {
	for _, p := range m.plugins {
		if !p.Subscribes(params.Type) {
			continue
		}
		if err := p.SendEvent(params); err != nil && warnLog != nil {
			fmt.Fprintf(warnLog, "pigo: plugin %q event %q: %v\n", p.Manifest.Name, params.Type, err)
		}
	}
}

// Close shuts down every loaded plugin, returning the first error encountered
// (all plugins are attempted regardless).
func (m *Manager) Close() error {
	var firstErr error
	for _, p := range m.plugins {
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Package clipboard writes text to the system clipboard by shelling out to the
// platform's clipboard utility (US-009, #125). It deliberately avoids any cgo or
// third-party dependency: it probes for the standard command-line tools
// (pbcopy on macOS, wl-copy / xclip / xsel on Linux) and pipes text to the first
// one found. When no utility is available it reports ErrUnavailable so the
// caller can degrade gracefully (e.g. print the text instead).
package clipboard

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// ErrUnavailable is returned by Copy when no supported clipboard utility is
// found on the host, so the caller can fall back to printing the content.
var ErrUnavailable = errors.New("clipboard: no clipboard utility available")

// candidate is a clipboard-writing command: the executable plus the args that
// make it read the payload from stdin.
type candidate struct {
	name string
	args []string
}

// candidates returns the clipboard-write commands to try, in priority order,
// for the current OS. On macOS pbcopy is always present; on Linux the Wayland
// tool is preferred, then the two common X11 tools.
func candidates() []candidate {
	switch runtime.GOOS {
	case "darwin":
		return []candidate{{name: "pbcopy"}}
	case "windows":
		return []candidate{{name: "clip"}}
	default: // linux and other unixes
		return []candidate{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	}
}

// Copy writes text to the system clipboard using the first available platform
// utility. It returns ErrUnavailable if none is found (so callers can fall back
// to printing), or the underlying exec error if a utility was found but failed.
func Copy(text string) error {
	for _, c := range candidates() {
		path, err := exec.LookPath(c.name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return err
		}
		return nil
	}
	return ErrUnavailable
}

// Available reports whether a clipboard utility is present, without writing
// anything. Useful for a caller that wants to phrase its output differently when
// it knows the copy will fail.
func Available() bool {
	for _, c := range candidates() {
		if _, err := exec.LookPath(c.name); err == nil {
			return true
		}
	}
	return false
}

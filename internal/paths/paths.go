// Package paths resolves the standard locations used by CodexTools MCP tools
// on Windows and Linux.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RuntimeDirName is the folder that holds a tool's compiled binary inside an
// installed tool directory (<tool>/binary-runtime/<tool>-mcp[.exe]).
const RuntimeDirName = "binary-runtime"

// ToolRoot returns the installed tool directory: the folder containing the
// executable, or its parent when the executable lives in binary-runtime.
func ToolRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	return RootFromExecutable(exe), nil
}

// RootFromExecutable is ToolRoot for an explicit executable path.
func RootFromExecutable(exe string) string {
	dir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(dir), RuntimeDirName) {
		return filepath.Dir(dir)
	}
	return dir
}

// ConfigDir returns the per-user configuration directory for app:
// %APPDATA%\<app> on Windows, $XDG_CONFIG_HOME/<app> (default ~/.config/<app>) on Linux.
// The directory is not created.
func ConfigDir(app string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, app), nil
}

// DataDir returns the per-user data directory for app:
// %LOCALAPPDATA%\<app> on Windows, $XDG_DATA_HOME/<app> (default ~/.local/share/<app>) on Linux.
// The directory is not created.
func DataDir(app string) (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, app), nil
		}
		return ConfigDir(app)
	}
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, app), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", app), nil
}

// ExpandHome replaces a leading "~" (alone or followed by / or \) with the
// user's home directory. Other paths are returned unchanged.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p[1:]), nil
}

// CodexHome returns $CODEX_HOME, defaulting to %USERPROFILE%\.codex or ~/.codex.
func CodexHome() (string, error) {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return ExpandHome(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

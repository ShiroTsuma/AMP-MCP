package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootFromExecutable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ssh")
	for exe, want := range map[string]string{
		filepath.Join(root, "binary-runtime", "ssh-mcp"): root,
		filepath.Join(root, "Binary-Runtime", "ssh-mcp"): root,
		filepath.Join(root, "ssh-mcp"):                   root,
	} {
		if got := RootFromExecutable(exe); got != want {
			t.Errorf("RootFromExecutable(%q) = %q, want %q", exe, got, want)
		}
	}
	if _, err := ToolRoot(); err != nil {
		t.Fatal(err)
	}
}

func TestExpandHomeAndCodexHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"~":           home,
		"~/a/b":       filepath.Join(home, "a", "b"),
		"/x/~":        "/x/~",
		"~other/file": "~other/file",
	} {
		if got, _ := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("CODEX_HOME", "")
	if got, _ := CodexHome(); got != filepath.Join(home, ".codex") {
		t.Errorf("default CodexHome = %q", got)
	}
	t.Setenv("CODEX_HOME", "~/custom")
	if got, _ := CodexHome(); got != filepath.Join(home, "custom") {
		t.Errorf("CodexHome = %q", got)
	}
}

func TestUserDirs(t *testing.T) {
	c, err := ConfigDir("tool")
	if err != nil || filepath.Base(c) != "tool" {
		t.Fatalf("ConfigDir = %q, %v", c, err)
	}
	d, err := DataDir("tool")
	if err != nil || filepath.Base(d) != "tool" {
		t.Fatalf("DataDir = %q, %v", d, err)
	}
}

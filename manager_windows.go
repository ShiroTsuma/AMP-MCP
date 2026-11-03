//go:build windows

package main

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

//go:embed Connection-Manager.ps1
var managerScript []byte

func openManager() error {
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "AMP-MCP")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	p := filepath.Join(dir, "Connection-Manager.ps1")
	if e = os.WriteFile(p, managerScript, 0600); e != nil {
		return e
	}
	// Break away from a supervising job when allowed so ending the agent session does not close the window.
	for _, flags := range []uint32{0x08000000 | 0x01000000, 0x08000000} { // CREATE_NO_WINDOW (+ CREATE_BREAKAWAY_FROM_JOB)
		cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", p, "-ConfigPath", configPath(), "-Executable", exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if e = cmd.Start(); e == nil {
			go cmd.Wait()
			return nil
		}
	}
	return e
}

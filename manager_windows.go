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
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", p, "-ConfigPath", configPath(), "-Executable", exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if e = cmd.Start(); e != nil {
		return e
	}
	go cmd.Wait()
	return nil
}

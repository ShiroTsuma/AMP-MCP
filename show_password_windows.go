//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func showUserPassword(filename string) error {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "AMP-MCP")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	script := filepath.Join(dir, "Show-User-Password.ps1")
	body := `param([string]$CredentialPath)
$ErrorActionPreference='Stop'
Add-Type -AssemblyName System.Windows.Forms
$data=Get-Content -LiteralPath $CredentialPath -Raw | ConvertFrom-Json
$secure=ConvertTo-SecureString $data.passwordDpapi
$cred=New-Object System.Management.Automation.PSCredential($data.username,$secure)
$form=New-Object System.Windows.Forms.Form
$form.Text='AMP user credential';$form.Width=560;$form.Height=170
$label=New-Object System.Windows.Forms.Label
$label.Text=('User: '+$data.username+' | Connection: '+$data.connection);$label.Left=15;$label.Top=15;$label.Width=520
$text=New-Object System.Windows.Forms.TextBox
$text.Left=15;$text.Top=50;$text.Width=510;$text.ReadOnly=$true;$text.Text=$cred.GetNetworkCredential().Password
$form.Controls.AddRange(@($label,$text))
[void]$form.ShowDialog()
$text.Clear();$cred=$null;$secure.Dispose()
`
	if e := os.WriteFile(script, []byte(body), 0600); e != nil {
		return e
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", script, "-CredentialPath", filename)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if e := cmd.Start(); e != nil {
		return e
	}
	go cmd.Wait()
	return nil
}

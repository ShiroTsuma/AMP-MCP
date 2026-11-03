//go:build windows

package main

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A connections.json written by the former PowerShell manager (ConvertFrom-
// SecureString, ConvertTo-Json) opens without migration, and editing another
// field keeps the stored ciphertext byte for byte.
func TestLegacyPowerShellConnectionsOpen(t *testing.T) {
	tempStore(t)
	ps := func(s string) string {
		out, e := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"ConvertFrom-SecureString (ConvertTo-SecureString '"+s+"' -AsPlainText -Force)").Output()
		if e != nil {
			t.Skip("Windows PowerShell unavailable")
		}
		return strings.TrimSpace(string(out))
	}
	token, password := ps("legacy-token-1"), ps("legacy-password-2")
	legacy := `{
    "version":  1,
    "connections":  {
                        "AMP - MCP":  {
                                          "url":  "https://amp.example.test",
                                          "username":  "mcp",
                                          "caCert":  "",
                                          "timeoutSeconds":  30,
                                          "instanceId":  "",
                                          "serviceTokenDpapi":  "` + token + `"
                                      },
                        "game":  {
                                     "url":  "https://amp.example.test",
                                     "username":  "player",
                                     "caCert":  "",
                                     "timeoutSeconds":  20,
                                     "instanceId":  "00000000-0000-0000-0000-000000000001",
                                     "passwordDpapi":  "` + password + `"
                                 }
                    }
}`
	if e := os.WriteFile(configPath(), append([]byte{0xef, 0xbb, 0xbf}, legacy...), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	if p, e := decryptToken(c.Connections["AMP - MCP"].Token); e != nil || p != "legacy-token-1" {
		t.Fatal("legacy token does not open")
	}
	if p, e := decryptToken(c.Connections["game"].Password); e != nil || p != "legacy-password-2" {
		t.Fatal("legacy password does not open")
	}
	recs, e := managerConfig().Load(context.Background())
	if e != nil || len(recs) != 2 || recs[1]["authentication"] != "password" || recs[1]["timeoutSeconds"] != "20" {
		t.Fatalf("panel records %v %v", len(recs), e)
	}
	p := openTestPanel(t, managerConfig())
	form := url.Values{"_key": {"game"}, "_op": {"save"}, "name": {"game"}, "url": {"https://amp.example.test"}, "username": {"player"},
		"authentication": {"password"}, "instanceId": {"00000000-0000-0000-0000-000000000001"}, "timeoutSeconds": {"25"}}
	if code, body := p.post("/submit", form); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	c, _ = loadConfig()
	if c.Connections["game"].Password != password || c.Connections["game"].TimeoutSeconds != 25 || c.Connections["AMP - MCP"].Token != token {
		t.Fatal("ciphertext changed or edit lost")
	}
}

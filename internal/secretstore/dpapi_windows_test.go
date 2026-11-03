//go:build windows

package secretstore

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestDPAPIRoundTripBothFormats(t *testing.T) {
	const secret = "pä$$wörd \U0001F511"
	for _, f := range []DPAPIFormat{FormatBase64, FormatPowerShellHex} {
		s, err := Default("test-tool", Options{DPAPIFormat: f})
		if err != nil || s.Kind() != BackendDPAPI {
			t.Fatalf("Default: %v", err)
		}
		sealed, err := s.Seal([]byte(secret), "ignored")
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Open(sealed)
		if err != nil || string(got) != secret {
			t.Fatalf("format %d: %q %v", f, got, err)
		}
	}
}

type testBlob struct {
	Size uint32
	Data *byte
}

// Replica of db-browser/credentials_windows.go cryptSecret + protectSecret.
func dbBrowserProtect(t *testing.T, secret []byte) string {
	ent := []byte("CodexTools credentials v1")
	in, entropy, out := testBlob{uint32(len(secret)), &secret[0]}, testBlob{uint32(len(ent)), &ent[0]}, testBlob{}
	ok, _, _ := syscall.NewLazyDLL("crypt32.dll").NewProc("CryptProtectData").Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)), 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		t.Fatal("CryptProtectData failed")
	}
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, int(out.Size)))
}

func powershell(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		t.Skipf("powershell unavailable: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestDPAPILegacyCompatibility(t *testing.T) {
	b64, _ := Default("db-browser", Options{DPAPIFormat: FormatBase64})
	ps, _ := Default("amp", Options{DPAPIFormat: FormatPowerShellHex})
	samples := []struct {
		name   string
		store  Store
		sealed string
	}{
		{"db-browser go", b64, dbBrowserProtect(t, []byte("db-pass"))},
		{"db-browser Credential-Manager.ps1", b64, powershell(t, `Add-Type -AssemblyName System.Security; [Convert]::ToBase64String([Security.Cryptography.ProtectedData]::Protect([Text.Encoding]::UTF8.GetBytes('db-pass'), [Text.Encoding]::UTF8.GetBytes('CodexTools credentials v1'), 'CurrentUser'))`)},
		{"amp ConvertFrom-SecureString", ps, powershell(t, `ConvertTo-SecureString 'db-pass' -AsPlainText -Force | ConvertFrom-SecureString`)},
		{"dpapi: prefix", b64, "dpapi:" + dbBrowserProtect(t, []byte("db-pass"))},
	}
	for _, c := range samples {
		got, err := c.store.Open(c.sealed)
		if err != nil || string(got) != "db-pass" {
			t.Errorf("%s: %q %v", c.name, got, err)
		}
	}
	// Seal output stays readable by the PowerShell side.
	sealed, _ := ps.Seal([]byte("from-go"), "")
	got := powershell(t, `$s = ConvertTo-SecureString '`+sealed+`'; [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($s))`)
	if got != "from-go" {
		t.Errorf("PowerShell read %q", got)
	}
	if _, err := b64.Open("secretservice:abc"); err == nil {
		t.Error("secretservice ref opened on Windows")
	}
}

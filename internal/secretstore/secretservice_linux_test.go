//go:build linux

package secretstore

import (
	"errors"
	"testing"
)

func TestSecretServiceRoundTrip(t *testing.T) {
	ss, err := newSecretService("mcptools-test")
	if err != nil {
		t.Skipf("no Secret Service on the session bus: %v", err)
	}
	ref, err := ss.Seal([]byte("ss-secret"), "unit test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ss.Open(ref)
	if err != nil || string(got) != "ss-secret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if err = ss.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if _, err = ss.Open(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestLinuxRejectsDPAPI(t *testing.T) {
	s, err := Default("x", Options{Backend: BackendVault, ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Open("AQAAANCMnd8BFdERjHoAwE/Cl+s="); err == nil {
		t.Fatal("DPAPI blob opened on Linux")
	}
}

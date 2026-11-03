package secretstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func init() { scryptN = 1 << 10 } // fast KDF in tests

func vaultStore(t *testing.T) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Default("test-tool", Options{Backend: BackendVault, ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return s, filepath.Join(dir, VaultFileName)
}

func TestVaultRoundTrip(t *testing.T) {
	t.Setenv(PassphraseEnv, "")
	s, file := vaultStore(t)
	if s.Kind() != BackendVault || !VaultLocked(s) {
		t.Fatal("new vault store should be a locked vault")
	}
	if _, err := s.Seal([]byte("x"), "a"); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("locked seal err = %v", err)
	}
	if err := UnlockVault(s, "correct horse"); err != nil {
		t.Fatal(err)
	}
	ref, err := s.Seal([]byte("s3cret"), "prod-db")
	if err != nil || !strings.HasPrefix(ref, "vault:") {
		t.Fatalf("ref %q err %v", ref, err)
	}
	got, err := s.Open(ref)
	if err != nil || string(got) != "s3cret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	raw, _ := os.ReadFile(file)
	if bytes.Contains(raw, []byte("s3cret")) {
		t.Fatal("plaintext in vault file")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
			t.Fatalf("vault mode %v", fi.Mode().Perm())
		}
	}
	// A second store for the same tool shares the unlocked vault.
	s2, _ := Default("test-tool", Options{Backend: BackendVault, ConfigDir: filepath.Dir(file)})
	if got, err = s2.Open(ref); err != nil || string(got) != "s3cret" {
		t.Fatalf("second store open = %q, %v", got, err)
	}
	if err = s.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Open(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
}

func TestVaultWrongPassphraseAndEnv(t *testing.T) {
	t.Setenv(PassphraseEnv, "")
	s, file := vaultStore(t)
	if err := UnlockVault(s, "right"); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.Seal([]byte("v"), "l")
	vaultAt(file).Lock()
	if err := UnlockVault(s, "wrong"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase err = %v", err)
	}
	if _, err := s.Open(ref); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("locked open err = %v", err)
	}
	t.Setenv(PassphraseEnv, "wrong")
	if _, err := s.Open(ref); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("env wrong passphrase err = %v", err)
	}
	t.Setenv(PassphraseEnv, "right")
	if got, err := s.Open(ref); err != nil || string(got) != "v" {
		t.Fatalf("env unlock open = %q, %v", got, err)
	}
}

func TestVaultTamperDetection(t *testing.T) {
	t.Setenv(PassphraseEnv, "")
	s, file := vaultStore(t)
	if err := UnlockVault(s, "pw"); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.Seal([]byte("value"), "label")
	id := strings.TrimPrefix(ref, "vault:")
	other := "0" + id[1:]
	if other == id {
		other = "1" + id[1:]
	}
	orig, _ := os.ReadFile(file)
	cases := []struct {
		name   string
		ref    string
		mutate func(*vaultFile)
	}{
		{"ciphertext", ref, func(f *vaultFile) { f.Items[id].Data[len(f.Items[id].Data)-1] ^= 1 }},
		{"label", ref, func(f *vaultFile) { it := f.Items[id]; it.Label = "other"; f.Items[id] = it }},
		{"moved to another id", "vault:" + other, func(f *vaultFile) { f.Items[other] = f.Items[id]; delete(f.Items, id) }},
	}
	for _, c := range cases {
		var f vaultFile
		if err := json.Unmarshal(orig, &f); err != nil {
			t.Fatal(err)
		}
		c.mutate(&f)
		b, _ := json.Marshal(f)
		if err := os.WriteFile(file, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Open(c.ref); err == nil {
			t.Errorf("%s: tampered item opened", c.name)
		}
	}
	os.WriteFile(file, orig, 0o600)
	if got, err := s.Open(ref); err != nil || string(got) != "value" {
		t.Fatalf("restored open = %q, %v", got, err)
	}
}

func TestOpenEmptyAndUnknownBackend(t *testing.T) {
	s, _ := vaultStore(t)
	if _, err := s.Open(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty ref err = %v", err)
	}
	if _, err := Default("x", Options{Backend: "nope"}); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/scrypt"
)

// Vault is the passphrase-protected file backend (<configDir>/secrets.vault).
// Each item is AES-256-GCM encrypted with a key derived by scrypt from the
// passphrase; the item id and label are bound as additional data, so edits to
// any field of the file are detected on Open.
type Vault struct {
	path string
	mu   sync.Mutex
	key  []byte // nil while locked
}

type vaultFile struct {
	Version int                  `json:"version"`
	KDF     string               `json:"kdf"`
	N       int                  `json:"n"`
	R       int                  `json:"r"`
	P       int                  `json:"p"`
	Salt    []byte               `json:"salt"`
	Check   []byte               `json:"check"` // GCM(nonce||ct) of vaultCheck: verifies the passphrase
	Items   map[string]vaultItem `json:"items"`
}

type vaultItem struct {
	Label string `json:"label"`
	Data  []byte `json:"data"` // nonce || ciphertext+tag
}

const vaultCheck = "mcptools vault v1"

// scrypt cost: ~32 MiB, ~100 ms per unlock. Variables so tests can go faster.
var scryptN, scryptR, scryptP = 1 << 15, 8, 1

var (
	vaultsMu sync.Mutex
	vaults   = map[string]*Vault{}
)

// vaultAt returns the process-wide Vault for path, so an unlock done by the
// panel is seen by every Store of the same tool.
func vaultAt(path string) *Vault {
	vaultsMu.Lock()
	defer vaultsMu.Unlock()
	if v := vaults[path]; v != nil {
		return v
	}
	v := &Vault{path: path}
	vaults[path] = v
	return v
}

func (v *Vault) Kind() string { return BackendVault }

// Unlock derives the key from passphrase. If the vault file does not exist it
// is created with a fresh salt, so the first unlock sets the passphrase.
func (v *Vault) Unlock(passphrase string) error {
	if passphrase == "" {
		return errors.New("vault passphrase must not be empty")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.unlock(passphrase)
}

// unlock is Unlock with v.mu held.
func (v *Vault) unlock(passphrase string) error {
	f, err := v.read()
	if errors.Is(err, os.ErrNotExist) {
		f = &vaultFile{Version: 1, KDF: "scrypt", N: scryptN, R: scryptR, P: scryptP, Salt: random(32), Items: map[string]vaultItem{}}
		key, err := f.derive(passphrase)
		if err != nil {
			return err
		}
		if f.Check, err = seal(key, []byte(vaultCheck), "check"); err != nil {
			return err
		}
		if err = v.write(f); err != nil {
			return err
		}
		v.key = key
		return nil
	}
	if err != nil {
		return err
	}
	key, err := f.derive(passphrase)
	if err != nil {
		return err
	}
	check, err := open(key, f.Check, "check")
	if err != nil || subtle.ConstantTimeCompare(check, []byte(vaultCheck)) != 1 {
		return ErrWrongPassphrase
	}
	v.key = key
	return nil
}

// Lock forgets the key.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.key)
	v.key = nil
}

func (v *Vault) locked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.ensureKey() != nil
}

// ensureKey unlocks from the environment if needed. Caller holds v.mu.
func (v *Vault) ensureKey() error {
	if v.key != nil {
		return nil
	}
	pass := os.Getenv(PassphraseEnv)
	if pass == "" {
		return ErrVaultLocked
	}
	if err := v.unlock(pass); err != nil {
		return fmt.Errorf("%s: %w", PassphraseEnv, err)
	}
	return nil
}

func (v *Vault) Seal(plain []byte, label string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.ensureKey(); err != nil {
		return "", err
	}
	// Re-read so items written by another process since unlock are kept.
	// shortcut: no cross-process file lock; two processes sealing at the same instant can lose one item. Add a lock file if tools start sharing a vault concurrently.
	f, err := v.read()
	if err != nil {
		return "", err
	}
	id := newID()
	data, err := seal(v.key, plain, aad(id, label))
	if err != nil {
		return "", err
	}
	f.Items[id] = vaultItem{Label: label, Data: data}
	if err = v.write(f); err != nil {
		return "", err
	}
	return BackendVault + ":" + id, nil
}

func (v *Vault) Open(sealed string) ([]byte, error) {
	id, ok := strings.CutPrefix(sealed, BackendVault+":")
	if !ok {
		return nil, ErrUnsupported
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.ensureKey(); err != nil {
		return nil, err
	}
	f, err := v.read()
	if err != nil {
		return nil, err
	}
	it, ok := f.Items[id]
	if !ok {
		return nil, ErrNotFound
	}
	plain, err := open(v.key, it.Data, aad(id, it.Label))
	if err != nil {
		return nil, errors.New("vault item failed authentication (file modified or corrupted)")
	}
	return plain, nil
}

func (v *Vault) Delete(sealed string) error {
	id, ok := strings.CutPrefix(sealed, BackendVault+":")
	if !ok {
		return ErrUnsupported
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.read()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, ok := f.Items[id]; !ok {
		return nil
	}
	delete(f.Items, id)
	return v.write(f)
}

func (v *Vault) read() (*vaultFile, error) {
	b, err := os.ReadFile(v.path)
	if err != nil {
		return nil, err
	}
	var f vaultFile
	if err = json.Unmarshal(b, &f); err != nil || f.Version != 1 || f.KDF != "scrypt" {
		return nil, fmt.Errorf("unreadable vault file %s", v.path)
	}
	if f.Items == nil {
		f.Items = map[string]vaultItem{}
	}
	return &f, nil
}

// write replaces the vault atomically (temp file + rename) with mode 0600.
func (v *Vault) write(f *vaultFile) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(v.path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), v.path)
}

func (f *vaultFile) derive(passphrase string) ([]byte, error) {
	if len(f.Salt) < 16 {
		return nil, errors.New("vault file has no salt")
	}
	return scrypt.Key([]byte(passphrase), f.Salt, f.N, f.R, f.P, 32)
}

func aad(id, label string) string { return "item\x00" + id + "\x00" + label }

func seal(key, plain []byte, ad string) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := random(g.NonceSize())
	return g.Seal(nonce, nonce, plain, []byte(ad)), nil
}

func open(key, data []byte, ad string) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(data) < g.NonceSize() {
		return nil, errors.New("short ciphertext")
	}
	return g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(ad))
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

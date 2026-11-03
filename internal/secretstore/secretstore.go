// Package secretstore seals small per-tool secrets (passwords, API tokens) so
// that configuration files never contain plaintext.
//
// Backends and the strings Seal returns:
//
//	Windows       DPAPI CurrentUser   un-prefixed base64 or hex (legacy formats, see DPAPIFormat)
//	Linux + D-Bus Secret Service      "secretservice:<id>"
//	Linux headless encrypted vault    "vault:<id>"  (<configDir>/secrets.vault, AES-256-GCM, scrypt)
//
// Open dispatches on the prefix, so a store can still open refs produced by
// another backend of the same tool (for example vault refs after a desktop
// keyring appears). Un-prefixed strings are treated as DPAPI (Windows only).
package secretstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"amp-mcp/internal/paths"
)

// Backend names, also returned by Store.Kind.
const (
	BackendDPAPI         = "dpapi"
	BackendSecretService = "secretservice"
	BackendVault         = "vault"
)

// DPAPIFormat selects the on-disk encoding of Windows DPAPI secrets. Both are
// CurrentUser scope with CRYPTPROTECT_UI_FORBIDDEN, so existing values open
// without migration.
type DPAPIFormat int

const (
	// FormatBase64 is raw secret bytes, optional entropy, standard base64.
	// Used by db-browser (credentials_windows.go protectSecret/unprotectSecret and
	// Credential-Manager.ps1, entropy LegacyEntropy).
	FormatBase64 DPAPIFormat = iota
	// FormatPowerShellHex is UTF-16LE text, no entropy, lowercase hex: the
	// PowerShell ConvertFrom-SecureString format. Used by amp
	// (amp/upstream/credentials_windows.go encryptCredential/decryptToken).
	// Secrets must be valid UTF-8 text in this format.
	FormatPowerShellHex
)

// LegacyEntropy is the DPAPI entropy db-browser and its PowerShell credential
// window use. It is the default entropy for FormatBase64.
const LegacyEntropy = "CodexTools credentials v1"

// PassphraseEnv lets a headless Linux host unlock the vault without the panel.
const PassphraseEnv = "MCP_SECRETS_PASSPHRASE"

// VaultFileName is the vault file inside Options.ConfigDir.
const VaultFileName = "secrets.vault"

// Errors callers may test with errors.Is.
var (
	ErrVaultLocked     = errors.New("secret vault is locked: open this tool's configuration panel to unlock it, or set " + PassphraseEnv + " for the MCP process")
	ErrWrongPassphrase = errors.New("wrong vault passphrase")
	ErrNotFound        = errors.New("secret not found")
	ErrUnsupported     = errors.New("secret reference cannot be opened on this system")
)

// Store seals and opens secrets for one tool.
type Store interface {
	// Seal protects plain and returns a string safe to keep in a config file.
	// label is a human-readable hint (e.g. connection name); DPAPI ignores it.
	Seal(plain []byte, label string) (string, error)
	// Open returns the plaintext for a string returned by Seal.
	Open(sealed string) ([]byte, error)
	// Delete removes the secret from its backing store. For DPAPI the secret
	// lives only in the config file, so Delete is a no-op.
	Delete(sealed string) error
	// Kind is the backend used by Seal (Backend* constants).
	Kind() string
}

// Options configures Default. The zero value is valid.
type Options struct {
	// Backend forces a backend; "" picks DPAPI on Windows, Secret Service on
	// Linux when available, otherwise the vault. BackendVault works on any OS.
	Backend string
	// DPAPIFormat selects the Windows encoding (default FormatBase64).
	DPAPIFormat DPAPIFormat
	// Entropy for FormatBase64; nil means LegacyEntropy. Ignored by FormatPowerShellHex.
	Entropy []byte
	// ConfigDir holds secrets.vault; default paths.ConfigDir(app).
	ConfigDir string
}

// Default returns the store for app (a short tool id such as "db-browser").
func Default(app string, opts Options) (Store, error) {
	if app == "" {
		return nil, errors.New("secretstore: app name required")
	}
	if opts.ConfigDir == "" {
		dir, err := paths.ConfigDir(app)
		if err != nil {
			return nil, err
		}
		opts.ConfigDir = dir
	}
	r := &router{app: app, opts: opts}
	switch opts.Backend {
	case BackendVault:
		r.primary = r.vault()
	case BackendDPAPI, BackendSecretService:
		s, err := platformStore(app, opts, opts.Backend)
		if err != nil {
			return nil, err
		}
		r.primary = s
	case "":
		s, err := platformStore(app, opts, "")
		if err != nil {
			return nil, err
		}
		if s == nil {
			s = r.vault()
		}
		r.primary = s
	default:
		return nil, fmt.Errorf("secretstore: unknown backend %q", opts.Backend)
	}
	return r, nil
}

// UnlockVault unlocks (or, if the vault file does not exist yet, creates) the
// vault behind s. It is a no-op for stores that do not use a vault.
func UnlockVault(s Store, passphrase string) error {
	if r, ok := s.(*router); ok && r.primary.Kind() == BackendVault {
		return r.vault().Unlock(passphrase)
	}
	return nil
}

// VaultLocked reports whether s seals into a vault that still needs a
// passphrase (the env var is tried first). Panels use it to show the unlock prompt.
func VaultLocked(s Store) bool {
	r, ok := s.(*router)
	return ok && r.primary.Kind() == BackendVault && r.vault().locked()
}

// router seals with the primary backend and opens by ref prefix.
type router struct {
	app     string
	opts    Options
	primary Store
}

func (r *router) Kind() string { return r.primary.Kind() }

func (r *router) Seal(plain []byte, label string) (string, error) {
	return r.primary.Seal(plain, label)
}

func (r *router) backendFor(sealed string) (Store, error) {
	switch {
	case strings.HasPrefix(sealed, BackendVault+":"):
		return r.vault(), nil
	case strings.HasPrefix(sealed, BackendSecretService+":"):
		if r.primary.Kind() == BackendSecretService {
			return r.primary, nil
		}
		return platformStore(r.app, r.opts, BackendSecretService)
	case sealed == "":
		return nil, ErrNotFound
	default:
		if r.primary.Kind() == BackendDPAPI {
			return r.primary, nil
		}
		return platformStore(r.app, r.opts, BackendDPAPI)
	}
}

func (r *router) Open(sealed string) ([]byte, error) {
	s, err := r.backendFor(sealed)
	if err != nil {
		return nil, err
	}
	return s.Open(sealed)
}

func (r *router) Delete(sealed string) error {
	s, err := r.backendFor(sealed)
	if err != nil {
		return err
	}
	return s.Delete(sealed)
}

func (r *router) vault() *Vault {
	return vaultAt(filepath.Join(r.opts.ConfigDir, VaultFileName))
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

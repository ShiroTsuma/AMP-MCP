package main

import (
	"path/filepath"

	"amp-mcp/internal/secretstore"
)

// secrets seals credentials next to the configuration. On Windows this is
// CurrentUser DPAPI in the PowerShell ConvertFrom-SecureString format, so
// existing connections.json files open unchanged. On Linux it is the Secret
// Service when a session keyring exists, otherwise <config dir>/secrets.vault.
func secrets() (secretstore.Store, error) {
	return secretstore.Default("AMP-MCP", secretstore.Options{
		DPAPIFormat: secretstore.FormatPowerShellHex,
		ConfigDir:   filepath.Dir(configPath()),
	})
}

func encryptCredential(s string) (string, error) {
	st, e := secrets()
	if e != nil {
		return "", e
	}
	return st.Seal([]byte(s), "AMP-MCP credential")
}

func decryptToken(s string) (string, error) {
	st, e := secrets()
	if e != nil {
		return "", e
	}
	b, e := st.Open(s)
	if e != nil {
		return "", e
	}
	defer clear(b)
	return string(b), nil
}

// forgetCredential removes a replaced keyring or vault item; DPAPI is a no-op.
func forgetCredential(s string) {
	if st, e := secrets(); e == nil && s != "" {
		_ = st.Delete(s)
	}
}

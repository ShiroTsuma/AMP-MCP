package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"amp-mcp/internal/paths"
)

type Connection struct {
	URL            string `json:"url"`
	Username       string `json:"username"`
	Token          string `json:"serviceTokenDpapi"`
	Password       string `json:"passwordDpapi,omitempty"`
	CACert         string `json:"caCert,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	InstanceID     string `json:"instanceId,omitempty"`
	// draft holds an unsaved plaintext credential for the panel Test action only.
	draft string
}
type Config struct {
	Version     int                   `json:"version"`
	Connections map[string]Connection `json:"connections"`
}

func configPath() string {
	if p := os.Getenv("AMP_MCP_CONFIG"); p != "" {
		return p
	}
	// %LOCALAPPDATA%AMP-MCP on Windows, $XDG_DATA_HOME/AMP-MCP (~/.local/share/AMP-MCP) on Linux.
	dir, e := paths.DataDir("AMP-MCP")
	if e != nil {
		dir = "AMP-MCP"
	}
	return filepath.Join(dir, "connections.json")
}
func loadConfig() (Config, error) {
	var c Config
	b, e := os.ReadFile(configPath())
	if e != nil {
		return c, errors.New("no connections configured; run amp-mcp configure (or amp_open_connection_manager) and save a connection")
	}
	e = json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &c)
	if e != nil || c.Version != 1 {
		return c, errors.New("invalid connection configuration version")
	}
	return c, nil
}

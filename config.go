package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Connection struct {
	URL            string `json:"url"`
	Username       string `json:"username"`
	Token          string `json:"serviceTokenDpapi"`
	Password       string `json:"passwordDpapi,omitempty"`
	CACert         string `json:"caCert,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	InstanceID     string `json:"instanceId,omitempty"`
}
type Config struct {
	Version     int                   `json:"version"`
	Connections map[string]Connection `json:"connections"`
}

func configPath() string {
	if p := os.Getenv("AMP_MCP_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "AMP-MCP", "connections.json")
}
func loadConfig() (Config, error) {
	var c Config
	b, e := os.ReadFile(configPath())
	if e != nil {
		return c, errors.New("open the connection manager and configure a connection")
	}
	e = json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &c)
	if e != nil || c.Version != 1 {
		return c, errors.New("invalid connection configuration version")
	}
	return c, nil
}
